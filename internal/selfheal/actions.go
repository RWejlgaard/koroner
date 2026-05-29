/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package selfheal

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
)

// executor performs one heal action against the cluster and returns a short
// human-readable detail string describing what changed. dryRun is handled by
// the engine; executors always mutate when called.
type executor func(ctx context.Context, c client.Client, obit *koronerv1alpha1.Obituary, p koronerv1alpha1.SelfHealPolicy) (detail string, err error)

// executors routes an action to its handler. Returning a nil executor lets the
// engine record a Failed HealRecord rather than panicking on an unknown enum.
func executors(action koronerv1alpha1.SelfHealAction) executor {
	switch action {
	case koronerv1alpha1.SelfHealDeletePod:
		return deletePod
	case koronerv1alpha1.SelfHealRestartWorkload:
		return restartWorkload
	case koronerv1alpha1.SelfHealBumpMemory:
		return bumpMemory
	case koronerv1alpha1.SelfHealRollbackDeployment:
		return rollbackDeployment
	default:
		return nil
	}
}

// deletePod removes the last-recorded dead pod so its controller (Deployment,
// StatefulSet, etc.) recreates it. Safe on its own; meaningful when the
// crash is transient.
func deletePod(ctx context.Context, c client.Client, obit *koronerv1alpha1.Obituary, _ koronerv1alpha1.SelfHealPolicy) (string, error) {
	podName := obit.Status.LastPod
	if podName == "" {
		return "", fmt.Errorf("no LastPod recorded on obituary")
	}
	var pod corev1.Pod
	key := client.ObjectKey{Namespace: obit.Spec.Subject.Namespace, Name: podName}
	if err := c.Get(ctx, key, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Sprintf("pod %s already gone", podName), nil
		}
		return "", err
	}
	if err := c.Delete(ctx, &pod); err != nil {
		return "", err
	}
	return fmt.Sprintf("deleted pod %s", podName), nil
}

// restartWorkload sets the standard restartedAt annotation on the owning
// workload's pod template. Matches `kubectl rollout restart`. Supports the
// three workload kinds that have a mutable pod template: Deployment,
// StatefulSet, DaemonSet.
func restartWorkload(ctx context.Context, c client.Client, obit *koronerv1alpha1.Obituary, _ koronerv1alpha1.SelfHealPolicy) (string, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	switch obit.Spec.Subject.Kind {
	case "Deployment":
		return patchDeploymentTemplate(ctx, c, obit, func(dep *appsv1.Deployment) {
			ensureAnnotations(&dep.Spec.Template.ObjectMeta)
			dep.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = now
		})
	case "StatefulSet":
		return patchStatefulSetTemplate(ctx, c, obit, func(ss *appsv1.StatefulSet) {
			ensureAnnotations(&ss.Spec.Template.ObjectMeta)
			ss.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = now
		})
	case "DaemonSet":
		return patchDaemonSetTemplate(ctx, c, obit, func(ds *appsv1.DaemonSet) {
			ensureAnnotations(&ds.Spec.Template.ObjectMeta)
			ds.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = now
		})
	default:
		return "", fmt.Errorf("RestartWorkload not supported for kind %s", obit.Spec.Subject.Kind)
	}
}

// bumpMemory multiplies every container's memory limit (and request) by
// MemoryFactor, capped by MemoryMaxLimit when set. Acts on workloads with a
// mutable pod template: Deployment, StatefulSet, DaemonSet. Pods (immutable)
// and Job/CronJob (no useful mutation) are filtered out upstream by the
// engine's per-kind capability map.
func bumpMemory(ctx context.Context, c client.Client, obit *koronerv1alpha1.Obituary, p koronerv1alpha1.SelfHealPolicy) (string, error) {
	factor, err := parseFactor(p.MemoryFactor)
	if err != nil {
		return "", err
	}
	var cap *resource.Quantity
	if p.MemoryMaxLimit != nil && *p.MemoryMaxLimit != "" {
		q, err := resource.ParseQuantity(*p.MemoryMaxLimit)
		if err != nil {
			return "", fmt.Errorf("invalid memoryMaxLimit %q: %w", *p.MemoryMaxLimit, err)
		}
		cap = &q
	}

	switch obit.Spec.Subject.Kind {
	case "Deployment":
		return patchDeploymentTemplate(ctx, c, obit, func(dep *appsv1.Deployment) {
			bumpPodSpecMemory(&dep.Spec.Template.Spec, factor, cap)
		})
	case "StatefulSet":
		return patchStatefulSetTemplate(ctx, c, obit, func(ss *appsv1.StatefulSet) {
			bumpPodSpecMemory(&ss.Spec.Template.Spec, factor, cap)
		})
	case "DaemonSet":
		return patchDaemonSetTemplate(ctx, c, obit, func(ds *appsv1.DaemonSet) {
			bumpPodSpecMemory(&ds.Spec.Template.Spec, factor, cap)
		})
	default:
		return "", fmt.Errorf("BumpMemory not supported for kind %s", obit.Spec.Subject.Kind)
	}
}

// rollbackDeployment finds the immediately-prior ReplicaSet by revision and
// copies its pod template back onto the Deployment. Mirrors what
// `kubectl rollout undo` does in modern clusters.
func rollbackDeployment(ctx context.Context, c client.Client, obit *koronerv1alpha1.Obituary, _ koronerv1alpha1.SelfHealPolicy) (string, error) {
	if obit.Spec.Subject.Kind != "Deployment" {
		return "", fmt.Errorf("RollbackDeployment only supports Deployment, got %s", obit.Spec.Subject.Kind)
	}
	ns := obit.Spec.Subject.Namespace
	name := obit.Spec.Subject.Name

	var dep appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &dep); err != nil {
		return "", err
	}
	prev, prevRev, err := previousReplicaSet(ctx, c, &dep)
	if err != nil {
		return "", err
	}
	if prev == nil {
		return "", fmt.Errorf("no prior ReplicaSet to roll back to")
	}
	currentRev := dep.Annotations["deployment.kubernetes.io/revision"]

	if _, err := patchDeploymentTemplate(ctx, c, obit, func(d *appsv1.Deployment) {
		// Preserve labels on the Deployment-owned template but swap spec.
		template := prev.Spec.Template.DeepCopy()
		template.Labels = d.Spec.Template.Labels
		d.Spec.Template.Spec = template.Spec
	}); err != nil {
		return "", err
	}
	return fmt.Sprintf("rolled back deployment %s revision %s -> %s", name, currentRev, prevRev), nil
}

// previousReplicaSet returns the ReplicaSet at revision N-1 for the given
// Deployment, plus its revision string. Returns (nil, "", nil) when only one
// revision exists.
func previousReplicaSet(ctx context.Context, c client.Client, dep *appsv1.Deployment) (*appsv1.ReplicaSet, string, error) {
	var rsList appsv1.ReplicaSetList
	if err := c.List(ctx, &rsList, client.InNamespace(dep.Namespace)); err != nil {
		return nil, "", err
	}

	type owned struct {
		rs  *appsv1.ReplicaSet
		rev int64
	}
	var owns []owned
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		if !ownedBy(rs.OwnerReferences, dep.UID) {
			continue
		}
		revStr := rs.Annotations["deployment.kubernetes.io/revision"]
		rev, _ := strconv.ParseInt(revStr, 10, 64)
		owns = append(owns, owned{rs: rs, rev: rev})
	}
	if len(owns) < 2 {
		return nil, "", nil
	}
	sort.Slice(owns, func(i, j int) bool { return owns[i].rev > owns[j].rev })
	// Index 0 is the current revision; index 1 is the immediate predecessor.
	prev := owns[1].rs
	return prev, strconv.FormatInt(owns[1].rev, 10), nil
}

func ownedBy(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, r := range refs {
		if r.UID == uid {
			return true
		}
	}
	return false
}

// patchDeploymentTemplate retries on conflict so rapid reconciles don't lose
// the heal write. mutate is called with the freshest object each retry.
func patchDeploymentTemplate(ctx context.Context, c client.Client, obit *koronerv1alpha1.Obituary, mutate func(*appsv1.Deployment)) (string, error) {
	ns := obit.Spec.Subject.Namespace
	name := obit.Spec.Subject.Name
	var detail string
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var dep appsv1.Deployment
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &dep); err != nil {
			return err
		}
		before := summarizeMemory(dep.Spec.Template.Spec.Containers)
		mutate(&dep)
		after := summarizeMemory(dep.Spec.Template.Spec.Containers)
		detail = diffDetail(before, after, "deployment "+name)
		return c.Update(ctx, &dep)
	})
	return detail, err
}

func patchStatefulSetTemplate(ctx context.Context, c client.Client, obit *koronerv1alpha1.Obituary, mutate func(*appsv1.StatefulSet)) (string, error) {
	ns := obit.Spec.Subject.Namespace
	name := obit.Spec.Subject.Name
	var detail string
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var ss appsv1.StatefulSet
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &ss); err != nil {
			return err
		}
		before := summarizeMemory(ss.Spec.Template.Spec.Containers)
		mutate(&ss)
		after := summarizeMemory(ss.Spec.Template.Spec.Containers)
		detail = diffDetail(before, after, "statefulset "+name)
		return c.Update(ctx, &ss)
	})
	return detail, err
}

func patchDaemonSetTemplate(ctx context.Context, c client.Client, obit *koronerv1alpha1.Obituary, mutate func(*appsv1.DaemonSet)) (string, error) {
	ns := obit.Spec.Subject.Namespace
	name := obit.Spec.Subject.Name
	var detail string
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var ds appsv1.DaemonSet
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &ds); err != nil {
			return err
		}
		before := summarizeMemory(ds.Spec.Template.Spec.Containers)
		mutate(&ds)
		after := summarizeMemory(ds.Spec.Template.Spec.Containers)
		detail = diffDetail(before, after, "daemonset "+name)
		return c.Update(ctx, &ds)
	})
	return detail, err
}

func ensureAnnotations(meta *metav1.ObjectMeta) {
	if meta.Annotations == nil {
		meta.Annotations = map[string]string{}
	}
}

// bumpPodSpecMemory multiplies each container's memory limit (and request when
// present) by factor, never exceeding cap.
func bumpPodSpecMemory(spec *corev1.PodSpec, factor float64, cap *resource.Quantity) {
	for i := range spec.Containers {
		c := &spec.Containers[i]
		if c.Resources.Limits == nil {
			c.Resources.Limits = corev1.ResourceList{}
		}
		if cur, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			c.Resources.Limits[corev1.ResourceMemory] = scaledQuantity(cur, factor, cap)
		}
		if c.Resources.Requests != nil {
			if cur, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
				c.Resources.Requests[corev1.ResourceMemory] = scaledQuantity(cur, factor, cap)
			}
		}
	}
}

// scaledQuantity returns q*factor, clamped at cap when cap is non-nil.
// Bytes are rounded up to the nearest MiB to keep output readable.
func scaledQuantity(q resource.Quantity, factor float64, cap *resource.Quantity) resource.Quantity {
	bytes := float64(q.Value()) * factor
	out := *resource.NewQuantity(roundUpToMiB(int64(bytes)), resource.BinarySI)
	if cap != nil && out.Cmp(*cap) > 0 {
		out = *cap
	}
	return out
}

func roundUpToMiB(n int64) int64 {
	const mib = 1024 * 1024
	if n%mib == 0 {
		return n
	}
	return ((n / mib) + 1) * mib
}

// parseFactor reads the factor string from policy. Defensive: an unparseable
// value falls back to 1.5 rather than failing the whole heal attempt.
func parseFactor(s *string) (float64, error) {
	if s == nil || *s == "" {
		return 1.5, nil
	}
	v, err := strconv.ParseFloat(*s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memoryFactor %q: %w", *s, err)
	}
	if v <= 1.0 {
		return 0, fmt.Errorf("memoryFactor must be > 1.0, got %v", v)
	}
	return v, nil
}

// summarizeMemory returns "container=limit" pairs for the detail string.
func summarizeMemory(containers []corev1.Container) string {
	var parts []string
	for _, c := range containers {
		if lim, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			parts = append(parts, fmt.Sprintf("%s=%s", c.Name, lim.String()))
		}
	}
	return strings.Join(parts, ",")
}

func diffDetail(before, after, target string) string {
	if before == after {
		return fmt.Sprintf("patched %s", target)
	}
	return fmt.Sprintf("patched %s memory %s -> %s", target, before, after)
}
