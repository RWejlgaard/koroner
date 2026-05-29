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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
	"github.com/RWejlgaard/koroner/internal/forensics"
	"github.com/RWejlgaard/koroner/internal/selfheal"
)

// episodeKeyLabel records the death-episode key on each Obituary so a given
// death produces exactly one record, idempotently across requeues.
const episodeKeyLabel = "koroner.pez.sh/episode-hash"

// resolveConfig finds the effective policy for a namespace: a KoronerConfig in
// the subject's own namespace wins; otherwise the "default" config in the
// operator's namespace; otherwise built-in defaults. The returned spec always
// has every field populated.
func resolveConfig(ctx context.Context, c client.Client, subjectNamespace, operatorNamespace string) koronerv1alpha1.KoronerConfigSpec {
	var list koronerv1alpha1.KoronerConfigList
	if err := c.List(ctx, &list, client.InNamespace(subjectNamespace)); err == nil && len(list.Items) > 0 {
		return list.Items[0].Spec.WithDefaults()
	}
	if operatorNamespace != "" && operatorNamespace != subjectNamespace {
		var def koronerv1alpha1.KoronerConfig
		key := client.ObjectKey{Namespace: operatorNamespace, Name: "default"}
		if err := c.Get(ctx, key, &def); err == nil {
			return def.Spec.WithDefaults()
		}
	}
	return koronerv1alpha1.DefaultKoronerConfigSpec()
}

// namespaceWatched reports whether a namespace passes the config's selector.
// An empty selector matches everything.
func namespaceWatched(ctx context.Context, c client.Client, spec koronerv1alpha1.KoronerConfigSpec, namespace string) bool {
	if spec.NamespaceSelector == nil {
		return true
	}
	sel, err := metav1.LabelSelectorAsSelector(spec.NamespaceSelector)
	if err != nil {
		return true // a malformed selector shouldn't silently drop deaths
	}
	if sel.Empty() {
		return true
	}
	var ns corev1.Namespace
	if err := c.Get(ctx, client.ObjectKey{Name: namespace}, &ns); err != nil {
		return false
	}
	return sel.Matches(labels.Set(ns.Labels))
}

// obituaryName derives a deterministic, DNS-1123-safe name from the subject
// name and the death-episode key, so the same death always maps to the same
// object.
func obituaryName(subjectName, episodeKey string) string {
	sum := sha256.Sum256([]byte(episodeKey))
	hash := hex.EncodeToString(sum[:])[:10]
	prefix := sanitizeName(subjectName)
	if len(prefix) > 40 {
		prefix = prefix[:40]
	}
	return fmt.Sprintf("%s-%s", strings.TrimRight(prefix, "-"), hash)
}

var nonDNS = regexp.MustCompile(`[^a-z0-9-]`)

func sanitizeName(s string) string {
	s = strings.ToLower(s)
	s = nonDNS.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// issueKey builds the stable identity of a "known issue": one obituary per
// (workload, container, cause). All restarts of a pod and all recreated pods
// of the same workload that die the same way roll up into this one key.
func issueKey(subject koronerv1alpha1.Subject, container, cause string) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", subject.Namespace, subject.Kind, subject.Name, container, cause)
}

// recordedDeath bundles everything the upsert needs about one death episode.
type recordedDeath struct {
	subject  koronerv1alpha1.Subject
	issueKey string
	// episode is the per-instance discriminator ("<podUID>/<restartCount>") used
	// to avoid counting the same crash twice.
	episode string
	// lastPod is the dead pod whose evidence the status reflects.
	lastPod string
	now     metav1.Time
	status  koronerv1alpha1.ObituaryStatus
}

// upsertObituary records a death as a known issue: it creates the obituary on
// first sight, or bumps its occurrence count and refreshes its evidence on a
// repeat. Returns whether a new obituary was created and whether an existing
// one was bumped (both false means the episode was already counted).
func upsertObituary(ctx context.Context, c client.Client, d recordedDeath) (created, bumped bool, err error) {
	name := obituaryName(d.subject.Name, d.issueKey)
	key := client.ObjectKey{Namespace: d.subject.Namespace, Name: name}

	var existing koronerv1alpha1.Obituary
	getErr := c.Get(ctx, key, &existing)
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return false, false, getErr
	}

	if apierrors.IsNotFound(getErr) {
		obit := &koronerv1alpha1.Obituary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: d.subject.Namespace,
				Labels:    map[string]string{episodeKeyLabel: shortHash(d.issueKey)},
			},
			Spec: koronerv1alpha1.ObituarySpec{
				Subject:         d.subject,
				TimeOfDeath:     &d.now,
				DeathEpisodeKey: d.issueKey,
			},
		}
		// IMPORTANT: no ownerReference to the deceased - the obituary must
		// outlive the corpse, so it must not be GC'd when the pod is reaped.
		if cerr := c.Create(ctx, obit); cerr != nil {
			if apierrors.IsAlreadyExists(cerr) {
				// Lost a race; fall through to the bump path on next reconcile.
				return false, false, nil
			}
			return false, false, cerr
		}
		obit.Status = d.status
		obit.Status.Occurrences = 1
		obit.Status.FirstSeen = &d.now
		obit.Status.LastSeen = &d.now
		obit.Status.LastEpisode = d.episode
		obit.Status.LastPod = d.lastPod
		return true, false, c.Status().Update(ctx, obit)
	}

	// Already seen this exact death episode - nothing to do.
	if existing.Status.LastEpisode == d.episode {
		return false, false, nil
	}

	// A genuinely new occurrence of a known issue: bump and refresh, retrying
	// on conflict because crash-loops reconcile rapidly.
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh koronerv1alpha1.Obituary
		if gerr := c.Get(ctx, key, &fresh); gerr != nil {
			return gerr
		}
		if fresh.Status.LastEpisode == d.episode {
			return nil // counted by a concurrent reconcile
		}
		prevCount := fresh.Status.Occurrences
		firstSeen := fresh.Status.FirstSeen

		fresh.Status = d.status
		fresh.Status.Occurrences = prevCount + 1
		if firstSeen != nil {
			fresh.Status.FirstSeen = firstSeen
		} else {
			fresh.Status.FirstSeen = &d.now
		}
		fresh.Status.LastSeen = &d.now
		fresh.Status.LastEpisode = d.episode
		fresh.Status.LastPod = d.lastPod
		return c.Status().Update(ctx, &fresh)
	})
	return false, err == nil, err
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// runNarrator invokes the configured Narrator (no-op by default) and stores the
// result. Kept here so both reconcilers narrate identically.
func runNarrator(ctx context.Context, n forensics.Narrator, v forensics.Verdict, status *koronerv1alpha1.ObituaryStatus) {
	if n == nil {
		return
	}
	if text, err := n.Narrate(ctx, v, status); err == nil {
		status.Narrative = text
	} else {
		logf.FromContext(ctx).V(1).Info("narrator failed", "error", err.Error())
	}
}

// resolveNarrator returns the narrator for one obituary write. If the policy
// is disabled or its secret cannot be read, the fallback (typically a
// NoopNarrator) is used so a missing/misconfigured key never blocks recording
// the death itself.
func resolveNarrator(
	ctx context.Context,
	c client.Client,
	policy koronerv1alpha1.NarratorPolicy,
	subjectNamespace string,
	fallback forensics.Narrator,
) forensics.Narrator {
	log := logf.FromContext(ctx)
	if !policy.Enabled {
		return fallback
	}
	if policy.Provider == "" {
		log.V(1).Info("narrator enabled without provider; falling back")
		return fallback
	}
	if policy.APIKeySecretRef == nil || policy.APIKeySecretRef.Name == "" {
		log.V(1).Info("narrator enabled without apiKeySecretRef; falling back")
		return fallback
	}
	ref := policy.APIKeySecretRef
	ns := ref.Namespace
	if ns == "" {
		ns = subjectNamespace
	}
	key := ref.Key
	if key == "" {
		key = forensics.DefaultSecretKey(policy.Provider)
	}
	if key == "" {
		log.V(1).Info("narrator: no secret key resolvable for provider", "provider", policy.Provider)
		return fallback
	}

	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &secret); err != nil {
		log.V(1).Info("narrator: cannot read API key secret", "secret", ns+"/"+ref.Name, "error", err.Error())
		return fallback
	}
	apiKey := strings.TrimSpace(string(secret.Data[key]))
	if apiKey == "" {
		log.V(1).Info("narrator: secret key empty", "secret", ns+"/"+ref.Name, "key", key)
		return fallback
	}

	n, err := forensics.NewLLMNarrator(forensics.LLMConfig{
		Provider: policy.Provider,
		Model:    policy.Model,
		APIKey:   apiKey,
		BaseURL:  policy.BaseURL,
	})
	if err != nil {
		log.V(1).Info("narrator: build failed", "error", err.Error())
		return fallback
	}
	return n
}

// HealDeciderFactory returns a decider factory suitable for selfheal.Engine.
// When the SelfHealLLMPolicy is enabled and its secret can be read, it chains
// rule -> LLM (rule first because the rule map covers the common, predictable
// causes faster than an HTTP call). Otherwise just the rule decider.
func HealDeciderFactory(c client.Client) func(ctx context.Context, ns string, p koronerv1alpha1.SelfHealPolicy) selfheal.Decider {
	return func(ctx context.Context, subjectNamespace string, p koronerv1alpha1.SelfHealPolicy) selfheal.Decider {
		rule := selfheal.RuleDecider{}
		llm := resolveSelfHealLLM(ctx, c, p.LLM, subjectNamespace)
		if llm == nil {
			return rule
		}
		return selfheal.CompositeDecider{Primary: rule, Fallback: llm}
	}
}

// resolveSelfHealLLM materialises an LLM-backed Decider when the policy is
// enabled and fully configured. Returns nil for any missing piece - the
// engine then falls back to the rule decider, never silently failing.
func resolveSelfHealLLM(
	ctx context.Context,
	c client.Client,
	policy koronerv1alpha1.SelfHealLLMPolicy,
	subjectNamespace string,
) selfheal.Decider {
	log := logf.FromContext(ctx)
	if !policy.Enabled {
		return nil
	}
	if policy.Provider == "" || policy.APIKeySecretRef == nil || policy.APIKeySecretRef.Name == "" {
		log.V(1).Info("selfheal llm enabled but provider/secret missing; falling back to rule")
		return nil
	}
	ref := policy.APIKeySecretRef
	ns := ref.Namespace
	if ns == "" {
		ns = subjectNamespace
	}
	key := ref.Key
	if key == "" {
		key = forensics.DefaultSecretKey(policy.Provider)
	}
	if key == "" {
		return nil
	}
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &secret); err != nil {
		log.V(1).Info("selfheal llm: cannot read API key secret", "secret", ns+"/"+ref.Name, "error", err.Error())
		return nil
	}
	apiKey := strings.TrimSpace(string(secret.Data[key]))
	if apiKey == "" {
		return nil
	}
	d, err := selfheal.NewLLMDecider(selfheal.LLMDeciderConfig{
		Provider: policy.Provider,
		Model:    policy.Model,
		APIKey:   apiKey,
		BaseURL:  policy.BaseURL,
	})
	if err != nil {
		log.V(1).Info("selfheal llm: build failed", "error", err.Error())
		return nil
	}
	return d
}
