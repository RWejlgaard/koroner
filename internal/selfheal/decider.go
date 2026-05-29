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

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
	"github.com/RWejlgaard/koroner/internal/forensics"
)

// Decision is the output of a Decider: an action to attempt (Skip if none)
// plus a short reason that ends up on the HealRecord.
type Decision struct {
	Action    koronerv1alpha1.SelfHealAction
	Reason    string
	DecidedBy string // "rule" or "llm"
}

// Skip is a Decision that tells the engine to record a Skipped HealRecord.
func Skip(reason, decidedBy string) Decision {
	return Decision{Reason: reason, DecidedBy: decidedBy}
}

// Decider picks an action for a known cause-of-death. Implementations may
// return Skip when no allowed action applies.
type Decider interface {
	Decide(ctx context.Context, obit *koronerv1alpha1.Obituary, allowed []koronerv1alpha1.SelfHealAction) Decision
}

// ruleMap is the cause-of-death to action mapping for the standard case: a
// Deployment subject with the full action menu available. Kind-specific maps
// (kindRuleMap) override individual entries where the generic recommendation
// doesn't apply.
var ruleMap = map[string]koronerv1alpha1.SelfHealAction{
	forensics.CauseOutOfMemory:     koronerv1alpha1.SelfHealBumpMemory,
	forensics.CauseRepeatedCrash:   koronerv1alpha1.SelfHealRollbackDeployment,
	forensics.CauseProbeFailure:    koronerv1alpha1.SelfHealRestartWorkload,
	forensics.CauseApplicationErr:  koronerv1alpha1.SelfHealDeletePod,
	forensics.CauseImagePullFailed: koronerv1alpha1.SelfHealRollbackDeployment,
	forensics.CauseNodePressure:    koronerv1alpha1.SelfHealDeletePod,
}

// kindRuleMap overrides ruleMap for subject kinds whose capability set rules
// out the generic recommendation. Entries here are tried first; a missing
// entry falls through to ruleMap.
//
//   - Pod (bare): the only available action is DeletePod, which only makes
//     sense for "kick the failing instance" causes - it can't fix OOM or
//     ImagePull errors, so those stay unmapped and Skipped.
//   - StatefulSet / DaemonSet: no ReplicaSet revision history, so CrashLoop
//     falls back to a rolling restart rather than a rollback.
var kindRuleMap = map[string]map[string]koronerv1alpha1.SelfHealAction{
	"Pod": {
		forensics.CauseRepeatedCrash:  koronerv1alpha1.SelfHealDeletePod,
		forensics.CauseApplicationErr: koronerv1alpha1.SelfHealDeletePod,
		forensics.CauseProbeFailure:   koronerv1alpha1.SelfHealDeletePod,
		forensics.CauseNodePressure:   koronerv1alpha1.SelfHealDeletePod,
	},
	"StatefulSet": {
		forensics.CauseRepeatedCrash:   koronerv1alpha1.SelfHealRestartWorkload,
		forensics.CauseImagePullFailed: koronerv1alpha1.SelfHealRestartWorkload,
	},
	"DaemonSet": {
		forensics.CauseRepeatedCrash:   koronerv1alpha1.SelfHealRestartWorkload,
		forensics.CauseImagePullFailed: koronerv1alpha1.SelfHealRestartWorkload,
	},
}

// ruleFor returns the recommended action for the given subject kind and cause,
// preferring a kind-specific override over the generic map. The bool reports
// whether any rule produced a recommendation.
func ruleFor(kind, cause string) (koronerv1alpha1.SelfHealAction, bool) {
	if overrides, ok := kindRuleMap[kind]; ok {
		if action, ok := overrides[cause]; ok {
			return action, true
		}
	}
	action, ok := ruleMap[cause]
	return action, ok
}

// RuleDecider maps cause-of-death to a fixed action via ruleMap, with
// per-kind overrides via kindRuleMap. Predictable, testable, no external
// dependencies.
type RuleDecider struct{}

// Decide picks an action from the rule map and validates it against both the
// subject kind's capability set and the operator's allowlist. The two checks
// produce distinct Skip reasons so users can tell why nothing happened.
func (RuleDecider) Decide(_ context.Context, obit *koronerv1alpha1.Obituary, allowed []koronerv1alpha1.SelfHealAction) Decision {
	cause := obit.Status.CauseOfDeath
	kind := obit.Spec.Subject.Kind
	action, ok := ruleFor(kind, cause)
	if !ok {
		return Skip("no rule for cause "+cause+" on kind "+kind, "rule")
	}
	if !kindSupports(kind, action) {
		return Skip("rule action "+string(action)+" not supported on kind "+kind, "rule")
	}
	if !actionAllowed(action, allowed) {
		return Skip("rule action "+string(action)+" not in operator allowlist", "rule")
	}
	return Decision{Action: action, Reason: "rule: " + kind + "/" + cause + " -> " + string(action), DecidedBy: "rule"}
}

// kindSupports reports whether action is in the per-kind capability set.
func kindSupports(kind string, action koronerv1alpha1.SelfHealAction) bool {
	_, ok := supportedActionsForKind(kind)[action]
	return ok
}

// CompositeDecider runs primary first; if it returns Skip and fallback is
// non-nil, runs fallback. Used to chain rule + LLM in either order.
type CompositeDecider struct {
	Primary  Decider
	Fallback Decider
}

// Decide implements Decider with a primary/fallback chain.
func (c CompositeDecider) Decide(ctx context.Context, obit *koronerv1alpha1.Obituary, allowed []koronerv1alpha1.SelfHealAction) Decision {
	if c.Primary != nil {
		d := c.Primary.Decide(ctx, obit, allowed)
		if d.Action != "" {
			return d
		}
	}
	if c.Fallback != nil {
		return c.Fallback.Decide(ctx, obit, allowed)
	}
	return Skip("no decider produced an action", "rule")
}

func actionAllowed(a koronerv1alpha1.SelfHealAction, allowed []koronerv1alpha1.SelfHealAction) bool {
	for _, x := range allowed {
		if x == a {
			return true
		}
	}
	return false
}
