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

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
	"github.com/RWejlgaard/koroner/internal/forensics"
)

// PodReconciler investigates pod deaths and records Obituaries.
type PodReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	Collector         *forensics.Collector
	Narrator          forensics.Narrator
	OperatorNamespace string
}

// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=pods/log,verbs=get
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=replicasets;deployments;statefulsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs;cronjobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=koroner.pez.sh,resources=koronerconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=koroner.pez.sh,resources=obituaries,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=koroner.pez.sh,resources=obituaries/status,verbs=get;update;patch

// Reconcile inspects a Pod, decides whether a death episode occurred, and if so
// gathers evidence and records an Obituary.
func (r *PodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		// Pod already gone - nothing to investigate. The obituary, if any,
		// survives independently.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	cfg := resolveConfig(ctx, r.Client, pod.Namespace, r.OperatorNamespace)
	if cfg.Watch.PodCrashes == nil || !*cfg.Watch.PodCrashes {
		return ctrl.Result{}, nil
	}
	if !namespaceWatched(ctx, r.Client, cfg, pod.Namespace) {
		return ctrl.Result{}, nil
	}

	death, found := detectPodDeath(&pod, cfg)
	if !found {
		return ctrl.Result{}, nil
	}

	// Resolve owner topology and diagnose up front - both are cheap (no log
	// fetch) and we need the cause + workload identity to find the obituary.
	owners := r.Collector.OwnerChain(ctx, pod.Namespace, pod.OwnerReferences)
	events, err := r.Collector.Events(ctx, pod.Namespace, string(pod.UID))
	if err != nil {
		log.Error(err, "collecting events")
	}
	verdict := forensics.Diagnose(death.evidence(events))

	subject := workloadSubject(&pod, owners)
	key := issueKey(subject, death.container, verdict.Cause)
	episode := fmt.Sprintf("%s/%d", pod.UID, death.restartCount)

	// Gate the expensive evidence (logs, metrics) behind the episode check: if
	// this exact crash is already counted, do nothing.
	name := obituaryName(subject.Name, key)
	var existing koronerv1alpha1.Obituary
	if gerr := r.Get(ctx, client.ObjectKey{Namespace: subject.Namespace, Name: name}, &existing); gerr == nil {
		if existing.Status.LastEpisode == episode {
			return ctrl.Result{}, nil
		}
	} else if !apierrors.IsNotFound(gerr) {
		return ctrl.Result{}, gerr
	}

	log.Info("recording death", "subject", subject.Kind+"/"+subject.Name,
		"cause", verdict.Cause, "pod", pod.Name, "episode", episode)

	status := r.buildStatus(ctx, &pod, death, verdict, events, owners, cfg)
	_, _, err = upsertObituary(ctx, r.Client, recordedDeath{
		subject:  subject,
		issueKey: key,
		episode:  episode,
		lastPod:  pod.Name,
		now:      metav1.Now(),
		status:   status,
	})
	return ctrl.Result{}, err
}

// workloadSubject identifies the entity an obituary is grouped under: the
// root owning workload (e.g. the Deployment behind a Pod) when one exists,
// otherwise the pod itself.
func workloadSubject(pod *corev1.Pod, owners []koronerv1alpha1.OwnerRecord) koronerv1alpha1.Subject {
	if top := forensics.TopOwner(owners); top.Name != "" {
		return koronerv1alpha1.Subject{
			APIVersion: top.APIVersion,
			Kind:       top.Kind,
			Name:       top.Name,
			Namespace:  pod.Namespace,
		}
	}
	return koronerv1alpha1.Subject{
		APIVersion: "v1",
		Kind:       "Pod",
		Name:       pod.Name,
		Namespace:  pod.Namespace,
		UID:        string(pod.UID),
	}
}

// podDeath captures the salient facts of a detected death so evidence
// collection and diagnosis can share them.
type podDeath struct {
	container      string
	restartCount   int32
	exitCode       *int32
	signal         *int32
	oomKilled      bool
	terminationMsg string
	terminatedRsn  string
	waitingReason  string
	evicted        bool
}

// detectPodDeath scans container statuses for a terminal/crash signal. It
// returns the death and true when one is found.
func detectPodDeath(pod *corev1.Pod, cfg koronerv1alpha1.KoronerConfigSpec) (podDeath, bool) {
	// Pod-level eviction (e.g. node pressure). Gated behind the evictions flag.
	if pod.Status.Reason == "Evicted" {
		if cfg.Watch.Evictions != nil && *cfg.Watch.Evictions {
			return podDeath{container: "", evicted: true}, true
		}
	}

	threshold := koronerv1alpha1.DefaultRestartThreshold
	if cfg.RestartThreshold != nil {
		threshold = *cfg.RestartThreshold
	}

	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		d := podDeath{container: cs.Name, restartCount: cs.RestartCount}

		// Currently terminated with a failure.
		if t := cs.State.Terminated; t != nil && (t.ExitCode != 0 || t.Reason == "OOMKilled") {
			fillTermination(&d, t)
			return d, true
		}
		// Stuck crash-looping.
		if w := cs.State.Waiting; w != nil {
			switch w.Reason {
			case "CrashLoopBackOff":
				d.waitingReason = w.Reason
				if lt := cs.LastTerminationState.Terminated; lt != nil {
					fillTermination(&d, lt)
				}
				return d, true
			case "ImagePullBackOff", "ErrImagePull":
				d.waitingReason = w.Reason
				return d, true
			}
		}
		// Restarting too often, even if momentarily Running.
		if cs.RestartCount >= threshold {
			if lt := cs.LastTerminationState.Terminated; lt != nil {
				fillTermination(&d, lt)
			}
			return d, true
		}
	}
	return podDeath{}, false
}

func fillTermination(d *podDeath, t *corev1.ContainerStateTerminated) {
	code := t.ExitCode
	d.exitCode = &code
	if t.Signal != 0 {
		sig := t.Signal
		d.signal = &sig
	}
	d.terminatedRsn = t.Reason
	d.terminationMsg = t.Message
	if t.Reason == "OOMKilled" {
		d.oomKilled = true
	}
}

// evidence translates a detected death (plus the event timeline) into the
// k8s-agnostic input the diagnosis layer consumes.
func (d podDeath) evidence(events []koronerv1alpha1.EventRecord) forensics.Evidence {
	e := forensics.Evidence{
		OOMKilled:         d.oomKilled,
		ExitCode:          d.exitCode,
		Signal:            d.signal,
		TerminationReason: d.terminatedRsn,
		WaitingReason:     d.waitingReason,
		RestartCount:      d.restartCount,
		Events:            events,
	}
	if d.evicted {
		e.PodPhaseReason = "Evicted"
	}
	return e
}

// buildStatus assembles the Obituary status for a new occurrence. The verdict,
// events and owners are already computed by the caller; this fetches the
// remaining (expensive) evidence - previous-container logs and optional
// metrics - and packages everything.
func (r *PodReconciler) buildStatus(
	ctx context.Context,
	pod *corev1.Pod,
	death podDeath,
	verdict forensics.Verdict,
	events []koronerv1alpha1.EventRecord,
	owners []koronerv1alpha1.OwnerRecord,
	cfg koronerv1alpha1.KoronerConfigSpec,
) koronerv1alpha1.ObituaryStatus {
	log := logf.FromContext(ctx)

	var logTail string
	if death.container != "" {
		tail := int64(koronerv1alpha1.DefaultLogTailLines)
		if cfg.LogTailLines != nil {
			tail = int64(*cfg.LogTailLines)
		}
		var err error
		if logTail, err = r.Collector.PreviousLogs(ctx, pod.Namespace, pod.Name, death.container, tail); err != nil {
			log.V(1).Info("could not collect logs", "error", err.Error())
		}
	}

	status := koronerv1alpha1.ObituaryStatus{
		Phase:         koronerv1alpha1.PhaseComplete,
		CauseOfDeath:  verdict.Cause,
		Confidence:    verdict.Confidence,
		Summary:       verdict.Summary,
		ExitCode:      death.exitCode,
		Signal:        death.signal,
		OOMKilled:     death.oomKilled,
		RestartCount:  death.restartCount,
		LogTail:       logTail,
		EventTimeline: events,
		Owners:        owners,
	}

	if cfg.Prometheus.Enabled && cfg.Prometheus.URL != "" {
		mc := forensics.MetricsConfig{URL: cfg.Prometheus.URL, Queries: cfg.Prometheus.Queries}
		if snap, merr := r.Collector.CollectMetrics(ctx, mc, pod.Namespace, pod.Name, time.Now()); merr != nil {
			log.V(1).Info("metrics collection failed", "error", merr.Error())
		} else {
			status.Metrics = snap
		}
	}

	runNarrator(ctx, r.Narrator, verdict, &status)
	return status
}

// SetupWithManager wires the reconciler to watch Pods.
func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Narrator == nil {
		r.Narrator = forensics.NoopNarrator{}
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Named("pod").
		Complete(r)
}
