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

// Package selfheal evaluates a completed Obituary against the SelfHealPolicy
// and, when all gates pass, picks and executes a remediation action. Every
// attempt - including dry-runs and skips - is appended to the Obituary's
// HealAttempts audit trail.
package selfheal

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
)

// Engine evaluates and executes self-heal actions. One Engine is shared across
// reconcilers; the rate limiter inside is goroutine-safe.
type Engine struct {
	client      client.Client
	rate        *rateLimiter
	deciderFor  func(ctx context.Context, ns string, p koronerv1alpha1.SelfHealPolicy) Decider
	maxAttempts int
}

// Option mutates an Engine at construction. Used so callers can inject a
// custom decider factory in tests.
type Option func(*Engine)

// WithDeciderFactory lets a caller supply a per-call decider. The default
// builds a RuleDecider; production wiring chains it with an LLM decider when
// LLM is configured.
func WithDeciderFactory(f func(ctx context.Context, ns string, p koronerv1alpha1.SelfHealPolicy) Decider) Option {
	return func(e *Engine) { e.deciderFor = f }
}

// WithMaxAttempts caps the length of an Obituary's HealAttempts audit trail.
// Older entries are trimmed once the cap is exceeded. Default 20.
func WithMaxAttempts(n int) Option {
	return func(e *Engine) { e.maxAttempts = n }
}

// NewEngine constructs the heal engine. The default decider factory returns a
// plain RuleDecider; callers that want LLM support should pass a factory that
// wraps RuleDecider with an LLM-backed CompositeDecider.
func NewEngine(c client.Client, opts ...Option) *Engine {
	e := &Engine{
		client:      c,
		rate:        newRateLimiter(),
		deciderFor:  func(_ context.Context, _ string, _ koronerv1alpha1.SelfHealPolicy) Decider { return RuleDecider{} },
		maxAttempts: 20,
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// MaybeHeal is the entry point reconcilers call after upserting an Obituary.
// It is a no-op when self-heal is disabled or any gate fails. When a gate
// fails for an observable reason (confidence too low, etc.) it is silent;
// when a decision is made it always writes a HealRecord so users can see
// what happened.
func (e *Engine) MaybeHeal(ctx context.Context, obit *koronerv1alpha1.Obituary, cfg koronerv1alpha1.KoronerConfigSpec) {
	log := logf.FromContext(ctx).WithValues("obituary", obit.Name, "subject", obit.Spec.Subject.Kind+"/"+obit.Spec.Subject.Name)
	p := cfg.SelfHeal
	if !p.Enabled {
		return
	}

	// Silent gates: things that aren't worth a HealRecord because they will
	// flip true on a future occurrence without operator action.
	if obit.Status.Phase != koronerv1alpha1.PhaseComplete {
		return
	}
	if min := derefInt32(p.MinOccurrences, koronerv1alpha1.DefaultSelfHealMinOccurrences); obit.Status.Occurrences < min {
		return
	}
	if boolOr(p.RequireHighConfidence, true) && obit.Status.Confidence != koronerv1alpha1.ConfidenceHigh {
		return
	}
	if !e.namespaceAllowed(ctx, p, obit.Spec.Subject.Namespace) {
		return
	}

	// Past the silent gates: decide an action.
	allowed := p.Actions
	if len(allowed) == 0 {
		allowed = koronerv1alpha1.DefaultSelfHealActions()
	}
	// Upfront guard: if no action in the allowlist is even executable against
	// this subject kind (e.g. operator only allowed BumpMemory but subject is
	// a Job), record a clear Skipped without bothering the decider. The
	// decider itself sees the full operator allowlist - it does the per-rule
	// kind-support and allowlist checks separately so the error messages
	// stay unambiguous.
	if len(applicableActions(obit.Spec.Subject.Kind, allowed)) == 0 {
		e.appendRecord(ctx, obit, koronerv1alpha1.HealRecord{
			Time:      now(),
			Outcome:   koronerv1alpha1.HealSkipped,
			Reason:    "no allowlisted action is supported on kind " + obit.Spec.Subject.Kind,
			DecidedBy: "engine",
		})
		return
	}
	decision := e.deciderFor(ctx, obit.Spec.Subject.Namespace, p).Decide(ctx, obit, allowed)
	if decision.Action == "" {
		e.appendRecord(ctx, obit, koronerv1alpha1.HealRecord{
			Time:      now(),
			Outcome:   koronerv1alpha1.HealSkipped,
			Reason:    decision.Reason,
			DecidedBy: decision.DecidedBy,
		})
		return
	}

	// Rate limit is checked after deciding so Skipped decisions don't burn
	// budget; Applied and DryRun do, since both represent a chosen action.
	if !e.rate.reserve(obit.Spec.Subject.Namespace, int(derefInt32(p.MaxHealsPerHour, koronerv1alpha1.DefaultSelfHealMaxHealsPerHour))) {
		e.appendRecord(ctx, obit, koronerv1alpha1.HealRecord{
			Time:      now(),
			Action:    decision.Action,
			Outcome:   koronerv1alpha1.HealSkipped,
			Reason:    "rate limit exceeded for namespace",
			DecidedBy: decision.DecidedBy,
		})
		return
	}

	if boolOr(p.DryRun, true) {
		e.appendRecord(ctx, obit, koronerv1alpha1.HealRecord{
			Time:      now(),
			Action:    decision.Action,
			Outcome:   koronerv1alpha1.HealDryRun,
			Detail:    fmt.Sprintf("dry-run: would execute %s", decision.Action),
			Reason:    decision.Reason,
			DecidedBy: decision.DecidedBy,
		})
		return
	}

	exec := executors(decision.Action)
	if exec == nil {
		e.appendRecord(ctx, obit, koronerv1alpha1.HealRecord{
			Time:      now(),
			Action:    decision.Action,
			Outcome:   koronerv1alpha1.HealFailed,
			Reason:    "no executor registered for action " + string(decision.Action),
			DecidedBy: decision.DecidedBy,
		})
		return
	}

	detail, err := exec(ctx, e.client, obit, p)
	if err != nil {
		log.V(1).Info("self-heal failed", "action", decision.Action, "error", err.Error())
		e.appendRecord(ctx, obit, koronerv1alpha1.HealRecord{
			Time:      now(),
			Action:    decision.Action,
			Outcome:   koronerv1alpha1.HealFailed,
			Detail:    detail,
			Reason:    err.Error(),
			DecidedBy: decision.DecidedBy,
		})
		return
	}
	log.Info("self-heal applied", "action", decision.Action, "detail", detail)
	e.appendRecord(ctx, obit, koronerv1alpha1.HealRecord{
		Time:      now(),
		Action:    decision.Action,
		Outcome:   koronerv1alpha1.HealApplied,
		Detail:    detail,
		Reason:    decision.Reason,
		DecidedBy: decision.DecidedBy,
	})
}

// namespaceAllowed evaluates the self-heal-specific NamespaceSelector against
// the subject namespace's labels. A nil/empty selector matches everything.
func (e *Engine) namespaceAllowed(ctx context.Context, p koronerv1alpha1.SelfHealPolicy, namespace string) bool {
	if p.NamespaceSelector == nil {
		return true
	}
	sel, err := metav1.LabelSelectorAsSelector(p.NamespaceSelector)
	if err != nil || sel.Empty() {
		return true
	}
	var ns corev1.Namespace
	if err := e.client.Get(ctx, client.ObjectKey{Name: namespace}, &ns); err != nil {
		return false
	}
	return sel.Matches(labels.Set(ns.Labels))
}

// appendRecord adds a HealRecord to the Obituary's status under optimistic
// concurrency control. The trail is trimmed to maxAttempts so a chronic
// failure doesn't bloat the object indefinitely.
func (e *Engine) appendRecord(ctx context.Context, obit *koronerv1alpha1.Obituary, rec koronerv1alpha1.HealRecord) {
	log := logf.FromContext(ctx)
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh koronerv1alpha1.Obituary
		if err := e.client.Get(ctx, client.ObjectKey{Namespace: obit.Namespace, Name: obit.Name}, &fresh); err != nil {
			return err
		}
		attempts := append(fresh.Status.HealAttempts, rec)
		if e.maxAttempts > 0 && len(attempts) > e.maxAttempts {
			attempts = attempts[len(attempts)-e.maxAttempts:]
		}
		fresh.Status.HealAttempts = attempts
		fresh.Status.LastHealAt = rec.Time
		return e.client.Status().Update(ctx, &fresh)
	})
	if err != nil {
		log.V(1).Info("failed to record heal attempt", "error", err.Error())
	}
}

func derefInt32(p *int32, dflt int32) int32 {
	if p == nil {
		return dflt
	}
	return *p
}

func boolOr(p *bool, dflt bool) bool {
	if p == nil {
		return dflt
	}
	return *p
}

func now() *metav1.Time {
	t := metav1.NewTime(time.Now())
	return &t
}
