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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
	"github.com/RWejlgaard/koroner/internal/forensics"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := koronerv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// newObit builds a high-confidence, repeated-OOM obituary that would pass all
// engine gates by default. Tests mutate fields to flip individual gates.
func newObit() *koronerv1alpha1.Obituary {
	return &koronerv1alpha1.Obituary{
		ObjectMeta: metav1.ObjectMeta{Name: "deploy-x-hash", Namespace: "team-a"},
		Spec: koronerv1alpha1.ObituarySpec{
			Subject: koronerv1alpha1.Subject{Kind: "Deployment", Name: "deploy-x", Namespace: "team-a"},
		},
		Status: koronerv1alpha1.ObituaryStatus{
			Phase:        koronerv1alpha1.PhaseComplete,
			CauseOfDeath: forensics.CauseOutOfMemory,
			Confidence:   koronerv1alpha1.ConfidenceHigh,
			Occurrences:  5,
		},
	}
}

// defaultedSpec returns a KoronerConfigSpec with SelfHeal enabled and all
// fields filled in via the public WithDefaults helper.
func defaultedSpec(t *testing.T, p koronerv1alpha1.SelfHealPolicy) koronerv1alpha1.KoronerConfigSpec {
	t.Helper()
	p.Enabled = true
	spec := koronerv1alpha1.KoronerConfigSpec{SelfHeal: p}
	return spec.WithDefaults()
}

// loadObit re-fetches the obituary from the fake client so tests can assert
// on the persisted HealAttempts rather than the in-memory copy.
func loadObit(t *testing.T, c client.Client, obit *koronerv1alpha1.Obituary) *koronerv1alpha1.Obituary {
	t.Helper()
	var fresh koronerv1alpha1.Obituary
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: obit.Namespace, Name: obit.Name}, &fresh); err != nil {
		t.Fatal(err)
	}
	return &fresh
}

func TestEngineNoOpWhenDisabled(t *testing.T) {
	obit := newObit()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(obit).WithStatusSubresource(obit).Build()
	e := NewEngine(c)

	e.MaybeHeal(context.Background(), obit, (&koronerv1alpha1.KoronerConfigSpec{}).WithDefaults())

	if got := loadObit(t, c, obit); len(got.Status.HealAttempts) != 0 {
		t.Fatalf("expected no heal attempts when disabled, got %d", len(got.Status.HealAttempts))
	}
}

func TestEngineRecordsDryRunByDefault(t *testing.T) {
	obit := newObit()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(obit).WithStatusSubresource(obit).Build()
	e := NewEngine(c)
	cfg := defaultedSpec(t, koronerv1alpha1.SelfHealPolicy{})

	e.MaybeHeal(context.Background(), obit, cfg)

	got := loadObit(t, c, obit)
	if len(got.Status.HealAttempts) != 1 {
		t.Fatalf("want 1 attempt, got %d", len(got.Status.HealAttempts))
	}
	rec := got.Status.HealAttempts[0]
	if rec.Outcome != koronerv1alpha1.HealDryRun {
		t.Fatalf("want DryRun, got %s", rec.Outcome)
	}
	if rec.Action != koronerv1alpha1.SelfHealBumpMemory {
		t.Fatalf("want BumpMemory action, got %s", rec.Action)
	}
}

func TestEngineGatesOnConfidence(t *testing.T) {
	obit := newObit()
	obit.Status.Confidence = koronerv1alpha1.ConfidenceMedium
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(obit).WithStatusSubresource(obit).Build()
	e := NewEngine(c)
	cfg := defaultedSpec(t, koronerv1alpha1.SelfHealPolicy{})

	e.MaybeHeal(context.Background(), obit, cfg)

	if got := loadObit(t, c, obit); len(got.Status.HealAttempts) != 0 {
		t.Fatalf("expected silent skip on low confidence, got %d records", len(got.Status.HealAttempts))
	}
}

func TestEngineGatesOnOccurrences(t *testing.T) {
	obit := newObit()
	obit.Status.Occurrences = 1
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(obit).WithStatusSubresource(obit).Build()
	e := NewEngine(c)
	cfg := defaultedSpec(t, koronerv1alpha1.SelfHealPolicy{})

	e.MaybeHeal(context.Background(), obit, cfg)

	if got := loadObit(t, c, obit); len(got.Status.HealAttempts) != 0 {
		t.Fatalf("expected silent skip below min occurrences, got %d", len(got.Status.HealAttempts))
	}
}

func TestEngineRecordsSkipWhenNoRuleMatches(t *testing.T) {
	obit := newObit()
	obit.Status.CauseOfDeath = forensics.CauseUnknown
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(obit).WithStatusSubresource(obit).Build()
	e := NewEngine(c)
	cfg := defaultedSpec(t, koronerv1alpha1.SelfHealPolicy{})

	e.MaybeHeal(context.Background(), obit, cfg)

	got := loadObit(t, c, obit)
	if len(got.Status.HealAttempts) != 1 {
		t.Fatalf("want 1 skipped record, got %d", len(got.Status.HealAttempts))
	}
	if got.Status.HealAttempts[0].Outcome != koronerv1alpha1.HealSkipped {
		t.Fatalf("want Skipped, got %s", got.Status.HealAttempts[0].Outcome)
	}
}

func TestEngineSkipsPodOOM(t *testing.T) {
	// OOM on a bare Pod has no useful action: BumpMemory can't be applied to
	// a Pod's immutable spec, and deleting won't fix the limit. Engine must
	// record a clean Skipped rather than try the impossible.
	obit := newObit()
	obit.Spec.Subject.Kind = "Pod"
	// newObit already sets CauseOfDeath=OutOfMemory.
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(obit).WithStatusSubresource(obit).Build()
	e := NewEngine(c)

	e.MaybeHeal(context.Background(), obit, defaultedSpec(t, koronerv1alpha1.SelfHealPolicy{}))

	got := loadObit(t, c, obit)
	if len(got.Status.HealAttempts) != 1 {
		t.Fatalf("want 1 skipped record, got %d", len(got.Status.HealAttempts))
	}
	rec := got.Status.HealAttempts[0]
	if rec.Outcome != koronerv1alpha1.HealSkipped {
		t.Fatalf("want Skipped, got %s", rec.Outcome)
	}
	if rec.Action != "" {
		t.Fatalf("want empty action on Skip, got %s", rec.Action)
	}
}

func TestEngineHealsPodCrashLoop(t *testing.T) {
	// CrashLoop on a bare Pod kicks via DeletePod under the kind-specific
	// rule override. With DryRun on (default) the record is DryRun, not Applied.
	obit := newObit()
	obit.Spec.Subject.Kind = "Pod"
	obit.Status.CauseOfDeath = forensics.CauseRepeatedCrash
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(obit).WithStatusSubresource(obit).Build()
	e := NewEngine(c)

	e.MaybeHeal(context.Background(), obit, defaultedSpec(t, koronerv1alpha1.SelfHealPolicy{}))

	got := loadObit(t, c, obit)
	if len(got.Status.HealAttempts) != 1 {
		t.Fatalf("want 1 record, got %d", len(got.Status.HealAttempts))
	}
	rec := got.Status.HealAttempts[0]
	if rec.Action != koronerv1alpha1.SelfHealDeletePod {
		t.Fatalf("want DeletePod, got %s", rec.Action)
	}
	if rec.Outcome != koronerv1alpha1.HealDryRun {
		t.Fatalf("want DryRun outcome, got %s", rec.Outcome)
	}
}

func TestEngineSkipsForJobSubject(t *testing.T) {
	// Job subjects have an immutable pod template - no action makes sense.
	obit := newObit()
	obit.Spec.Subject.Kind = "Job"
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(obit).WithStatusSubresource(obit).Build()
	e := NewEngine(c)

	e.MaybeHeal(context.Background(), obit, defaultedSpec(t, koronerv1alpha1.SelfHealPolicy{}))

	got := loadObit(t, c, obit)
	if len(got.Status.HealAttempts) != 1 || got.Status.HealAttempts[0].Outcome != koronerv1alpha1.HealSkipped {
		t.Fatalf("want one Skipped record for Job subject, got %+v", got.Status.HealAttempts)
	}
}

func TestEngineStatefulSetCrashLoopRestarts(t *testing.T) {
	// StatefulSet has no ReplicaSet history, so the per-kind override maps
	// CrashLoopBackOff to RestartWorkload instead of RollbackDeployment. With
	// DryRun on the engine records the chosen action without mutating.
	obit := newObit()
	obit.Spec.Subject.Kind = "StatefulSet"
	obit.Status.CauseOfDeath = forensics.CauseRepeatedCrash
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(obit).WithStatusSubresource(obit).Build()
	e := NewEngine(c)

	e.MaybeHeal(context.Background(), obit, defaultedSpec(t, koronerv1alpha1.SelfHealPolicy{}))

	got := loadObit(t, c, obit)
	if len(got.Status.HealAttempts) != 1 {
		t.Fatalf("want 1 record, got %d", len(got.Status.HealAttempts))
	}
	rec := got.Status.HealAttempts[0]
	if rec.Action != koronerv1alpha1.SelfHealRestartWorkload {
		t.Fatalf("want RestartWorkload, got %s", rec.Action)
	}
	if rec.Outcome != koronerv1alpha1.HealDryRun {
		t.Fatalf("want DryRun outcome, got %s", rec.Outcome)
	}
}

func TestEngineRateLimitsAcrossCalls(t *testing.T) {
	obit := newObit()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(obit).WithStatusSubresource(obit).Build()
	e := NewEngine(c)
	dryRun := false
	maxHeals := int32(2)
	// Use a stub decider so we don't actually mutate workloads (none exist in
	// the fake client) - the rate limiter still consumes budget.
	e.deciderFor = func(context.Context, string, koronerv1alpha1.SelfHealPolicy) Decider {
		return stubDecider{decision: Decision{Action: koronerv1alpha1.SelfHealDeletePod, Reason: "stub", DecidedBy: "rule"}}
	}
	cfg := defaultedSpec(t, koronerv1alpha1.SelfHealPolicy{DryRun: &dryRun, MaxHealsPerHour: &maxHeals})

	// First two calls reserve budget (Apply will fail because pod doesn't
	// exist, but rate budget is still consumed). Third call is rate-limited.
	for i := 0; i < 3; i++ {
		e.MaybeHeal(context.Background(), obit, cfg)
	}

	got := loadObit(t, c, obit)
	if n := len(got.Status.HealAttempts); n != 3 {
		t.Fatalf("want 3 records, got %d", n)
	}
	last := got.Status.HealAttempts[2]
	if last.Outcome != koronerv1alpha1.HealSkipped || last.Reason != "rate limit exceeded for namespace" {
		t.Fatalf("third call should be rate-limited, got outcome=%s reason=%q", last.Outcome, last.Reason)
	}
}
