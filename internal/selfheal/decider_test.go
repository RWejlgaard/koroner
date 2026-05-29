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
	"strings"
	"testing"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
	"github.com/RWejlgaard/koroner/internal/forensics"
)

func TestRuleDeciderDeploymentMapping(t *testing.T) {
	allowed := koronerv1alpha1.DefaultSelfHealActions()
	cases := []struct {
		cause string
		want  koronerv1alpha1.SelfHealAction
	}{
		{forensics.CauseOutOfMemory, koronerv1alpha1.SelfHealBumpMemory},
		{forensics.CauseRepeatedCrash, koronerv1alpha1.SelfHealRollbackDeployment},
		{forensics.CauseProbeFailure, koronerv1alpha1.SelfHealRestartWorkload},
		{forensics.CauseApplicationErr, koronerv1alpha1.SelfHealDeletePod},
	}
	for _, tc := range cases {
		t.Run(tc.cause, func(t *testing.T) {
			obit := obitFor("Deployment", tc.cause)
			d := RuleDecider{}.Decide(context.Background(), obit, allowed)
			if d.Action != tc.want {
				t.Fatalf("got %q, want %q", d.Action, tc.want)
			}
			if d.DecidedBy != "rule" {
				t.Fatalf("DecidedBy = %q, want rule", d.DecidedBy)
			}
		})
	}
}

// TestRuleDeciderPodMapping covers the per-kind override that lets bare Pods
// self-heal via DeletePod for "kick-the-instance" causes while still
// declining to act on causes DeletePod can't fix (OOM, ImagePull).
func TestRuleDeciderPodMapping(t *testing.T) {
	allowed := []koronerv1alpha1.SelfHealAction{koronerv1alpha1.SelfHealDeletePod}
	heals := []string{
		forensics.CauseRepeatedCrash,
		forensics.CauseApplicationErr,
		forensics.CauseProbeFailure,
		forensics.CauseNodePressure,
	}
	for _, cause := range heals {
		t.Run("heal/"+cause, func(t *testing.T) {
			d := RuleDecider{}.Decide(context.Background(), obitFor("Pod", cause), allowed)
			if d.Action != koronerv1alpha1.SelfHealDeletePod {
				t.Fatalf("got %q, want DeletePod", d.Action)
			}
		})
	}
	skips := []string{forensics.CauseOutOfMemory, forensics.CauseImagePullFailed}
	for _, cause := range skips {
		t.Run("skip/"+cause, func(t *testing.T) {
			d := RuleDecider{}.Decide(context.Background(), obitFor("Pod", cause), allowed)
			if d.Action != "" {
				t.Fatalf("expected Skip for Pod/%s (DeletePod can't fix it), got %q", cause, d.Action)
			}
		})
	}
}

// TestRuleDeciderStatefulSetUsesRestart confirms StatefulSet CrashLoop maps
// to RestartWorkload instead of RollbackDeployment (no ReplicaSet history).
func TestRuleDeciderStatefulSetUsesRestart(t *testing.T) {
	allowed := koronerv1alpha1.DefaultSelfHealActions()
	d := RuleDecider{}.Decide(context.Background(), obitFor("StatefulSet", forensics.CauseRepeatedCrash), allowed)
	if d.Action != koronerv1alpha1.SelfHealRestartWorkload {
		t.Fatalf("got %q, want RestartWorkload", d.Action)
	}
}

func TestRuleDeciderSkipsUnknownCause(t *testing.T) {
	d := RuleDecider{}.Decide(context.Background(), obitFor("Deployment", forensics.CauseUnknown), koronerv1alpha1.DefaultSelfHealActions())
	if d.Action != "" {
		t.Fatalf("expected skip, got action %q", d.Action)
	}
}

func TestRuleDeciderSkipsDisallowedAction(t *testing.T) {
	// OOM maps to BumpMemory; if BumpMemory is not in the allowlist, skip.
	d := RuleDecider{}.Decide(context.Background(), obitFor("Deployment", forensics.CauseOutOfMemory), []koronerv1alpha1.SelfHealAction{koronerv1alpha1.SelfHealDeletePod})
	if d.Action != "" {
		t.Fatalf("expected skip when BumpMemory disallowed, got %q", d.Action)
	}
	if !strings.Contains(d.Reason, "not in operator allowlist") {
		t.Fatalf("reason should mention operator allowlist, got %q", d.Reason)
	}
}

// TestRuleDeciderKindUnsupportedHasDistinctReason exercises the bug where the
// engine pre-filtered the allowlist by kind, so a Pod+OOM with BumpMemory in
// the operator allowlist surfaced the misleading "not in operator allowlist"
// instead of "not supported on kind Pod".
func TestRuleDeciderKindUnsupportedHasDistinctReason(t *testing.T) {
	d := RuleDecider{}.Decide(
		context.Background(),
		obitFor("Pod", forensics.CauseOutOfMemory),
		koronerv1alpha1.DefaultSelfHealActions(), // BumpMemory IS allowlisted
	)
	if d.Action != "" {
		t.Fatalf("expected skip, got %q", d.Action)
	}
	if !strings.Contains(d.Reason, "not supported on kind Pod") {
		t.Fatalf("reason should mention kind support, got %q", d.Reason)
	}
	if strings.Contains(d.Reason, "operator allowlist") {
		t.Fatalf("reason should NOT mention allowlist (operator allowlisted it), got %q", d.Reason)
	}
}

func obitFor(kind, cause string) *koronerv1alpha1.Obituary {
	return &koronerv1alpha1.Obituary{
		Spec:   koronerv1alpha1.ObituarySpec{Subject: koronerv1alpha1.Subject{Kind: kind}},
		Status: koronerv1alpha1.ObituaryStatus{CauseOfDeath: cause},
	}
}

func TestCompositeFallsBackOnSkip(t *testing.T) {
	primary := stubDecider{decision: Skip("no idea", "rule")}
	fallback := stubDecider{decision: Decision{Action: koronerv1alpha1.SelfHealDeletePod, DecidedBy: "llm"}}
	c := CompositeDecider{Primary: primary, Fallback: fallback}
	d := c.Decide(context.Background(), &koronerv1alpha1.Obituary{}, koronerv1alpha1.DefaultSelfHealActions())
	if d.Action != koronerv1alpha1.SelfHealDeletePod || d.DecidedBy != "llm" {
		t.Fatalf("composite did not fall back: %+v", d)
	}
}

func TestCompositeUsesPrimaryWhenItActs(t *testing.T) {
	primary := stubDecider{decision: Decision{Action: koronerv1alpha1.SelfHealBumpMemory, DecidedBy: "rule"}}
	fallback := stubDecider{decision: Decision{Action: koronerv1alpha1.SelfHealDeletePod, DecidedBy: "llm"}}
	c := CompositeDecider{Primary: primary, Fallback: fallback}
	d := c.Decide(context.Background(), &koronerv1alpha1.Obituary{}, koronerv1alpha1.DefaultSelfHealActions())
	if d.Action != koronerv1alpha1.SelfHealBumpMemory || d.DecidedBy != "rule" {
		t.Fatalf("composite used fallback when primary succeeded: %+v", d)
	}
}

type stubDecider struct{ decision Decision }

func (s stubDecider) Decide(context.Context, *koronerv1alpha1.Obituary, []koronerv1alpha1.SelfHealAction) Decision {
	return s.decision
}

func TestParseDecisionJSON(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantAction koronerv1alpha1.SelfHealAction
		wantSkip   bool
	}{
		{"clean", `{"action":"BumpMemory","reason":"oom"}`, koronerv1alpha1.SelfHealBumpMemory, false},
		{"fenced", "```json\n{\"action\":\"DeletePod\",\"reason\":\"flake\"}\n```", koronerv1alpha1.SelfHealDeletePod, false},
		{"decline", `{"action":"","reason":"unclear"}`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action, _, err := parseDecisionJSON(tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if action != tc.wantAction {
				t.Fatalf("got %q, want %q", action, tc.wantAction)
			}
		})
	}
}

func TestParseDecisionJSONRejectsGarbage(t *testing.T) {
	if _, _, err := parseDecisionJSON("no json here"); err == nil {
		t.Fatal("expected error for input with no JSON")
	}
}
