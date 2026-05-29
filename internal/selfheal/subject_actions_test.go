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
	"reflect"
	"testing"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
)

func TestApplicableActionsPerKind(t *testing.T) {
	all := koronerv1alpha1.DefaultSelfHealActions()
	cases := []struct {
		kind string
		want []koronerv1alpha1.SelfHealAction
	}{
		{"Deployment", all},
		{"StatefulSet", []koronerv1alpha1.SelfHealAction{
			koronerv1alpha1.SelfHealDeletePod,
			koronerv1alpha1.SelfHealRestartWorkload,
			koronerv1alpha1.SelfHealBumpMemory,
		}},
		{"DaemonSet", []koronerv1alpha1.SelfHealAction{
			koronerv1alpha1.SelfHealDeletePod,
			koronerv1alpha1.SelfHealRestartWorkload,
			koronerv1alpha1.SelfHealBumpMemory,
		}},
		{"Pod", []koronerv1alpha1.SelfHealAction{koronerv1alpha1.SelfHealDeletePod}},
		{"Job", nil},
		{"CronJob", nil},
		{"Mystery", nil},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			got := applicableActions(tc.kind, all)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApplicableActionsPreservesAllowlistOrder(t *testing.T) {
	// Operator's allowlist intentionally in a different order than the rule
	// defaults; filtering for a Deployment should preserve that order.
	allow := []koronerv1alpha1.SelfHealAction{
		koronerv1alpha1.SelfHealBumpMemory,
		koronerv1alpha1.SelfHealRollbackDeployment,
		koronerv1alpha1.SelfHealDeletePod,
	}
	got := applicableActions("Deployment", allow)
	if !reflect.DeepEqual(got, allow) {
		t.Fatalf("filter scrambled order: got %v, want %v", got, allow)
	}
}

func TestApplicableActionsRespectsAllowlist(t *testing.T) {
	// Even where the kind supports BumpMemory, omitting it from the allowlist
	// must keep it out of the filtered menu.
	allow := []koronerv1alpha1.SelfHealAction{koronerv1alpha1.SelfHealDeletePod}
	got := applicableActions("Deployment", allow)
	if !reflect.DeepEqual(got, allow) {
		t.Fatalf("filter widened allowlist: got %v, want %v", got, allow)
	}
}
