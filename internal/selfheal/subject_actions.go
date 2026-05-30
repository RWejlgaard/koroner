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

import koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"

// supportedActionsForKind reports which actions are meaningful for an
// Obituary whose subject is the given Kind. Kept as a pure function so the
// engine and tests can both consult it without instantiating anything.
//
// Rationale per kind:
//   - Deployment: full menu; rollback uses ReplicaSet history.
//   - StatefulSet / DaemonSet: same pod-template patches as Deployment but
//     no rollback (no ReplicaSet history; manual revisions are out of scope).
//   - Pod (bare, no controller): only DeletePod - nothing to recreate it,
//     but the user opted in, so respect their intent.
//   - Job / CronJob: nothing safe; their pod templates are immutable
//     post-creation and there is no useful "restart" semantics here.
func supportedActionsForKind(kind string) map[koronerv1alpha1.SelfHealAction]struct{} {
	switch kind {
	case "Deployment":
		return actionSet(
			koronerv1alpha1.SelfHealDeletePod,
			koronerv1alpha1.SelfHealRestartWorkload,
			koronerv1alpha1.SelfHealBumpMemory,
			koronerv1alpha1.SelfHealRollbackDeployment,
		)
	case "StatefulSet", "DaemonSet":
		return actionSet(
			koronerv1alpha1.SelfHealDeletePod,
			koronerv1alpha1.SelfHealRestartWorkload,
			koronerv1alpha1.SelfHealBumpMemory,
		)
	case "Pod":
		return actionSet(koronerv1alpha1.SelfHealDeletePod)
	default:
		return nil
	}
}

// applicableActions intersects the operator-configured allowlist with the
// actions actually executable against the given subject kind. The intersection
// preserves the input order so deterministic deciders see a stable menu.
func applicableActions(kind string, allowed []koronerv1alpha1.SelfHealAction) []koronerv1alpha1.SelfHealAction {
	supported := supportedActionsForKind(kind)
	if len(supported) == 0 {
		return nil
	}
	out := make([]koronerv1alpha1.SelfHealAction, 0, len(allowed))
	for _, a := range allowed {
		if _, ok := supported[a]; ok {
			out = append(out, a)
		}
	}
	return out
}

func actionSet(actions ...koronerv1alpha1.SelfHealAction) map[koronerv1alpha1.SelfHealAction]struct{} {
	m := make(map[koronerv1alpha1.SelfHealAction]struct{}, len(actions))
	for _, a := range actions {
		m[a] = struct{}{}
	}
	return m
}
