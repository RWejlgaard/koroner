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

// Package forensics gathers evidence about dead workloads and renders a
// heuristic cause of death. The diagnosis layer is intentionally free of
// controller-runtime coupling so it can be unit-tested in isolation.
package forensics

import (
	"fmt"
	"strings"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
)

// Cause-of-death verdicts produced by Diagnose.
const (
	CauseOutOfMemory     = "OutOfMemory"
	CauseRepeatedCrash   = "RepeatedCrash"
	CauseProbeFailure    = "ProbeFailure"
	CauseNodePressure    = "NodePressure"
	CauseImagePullFailed = "ImagePullFailure"
	CauseApplicationErr  = "ApplicationError"
	CauseJobFailed       = "JobFailed"
	CauseUnknown         = "Unknown"
)

// Evidence is the collected, k8s-agnostic input to a diagnosis. Collectors
// populate it; Diagnose reads it. Pointers distinguish "unset" from "zero".
type Evidence struct {
	// OOMKilled is true when the kernel OOM killer terminated the container.
	OOMKilled bool
	// ExitCode of the terminated container, if known.
	ExitCode *int32
	// Signal that terminated the container, if known.
	Signal *int32
	// TerminationReason is the container state reason, e.g. "Error", "OOMKilled".
	TerminationReason string
	// WaitingReason is the container waiting reason, e.g. "CrashLoopBackOff",
	// "ImagePullBackOff".
	WaitingReason string
	// RestartCount observed at time of death.
	RestartCount int32
	// PodPhaseReason is the pod-level status reason, e.g. "Evicted".
	PodPhaseReason string
	// JobFailed indicates a batch Job exhausted its backoffLimit.
	JobFailed bool
	// Events distilled from the deceased's Kubernetes events.
	Events []koronerv1alpha1.EventRecord
}

// Verdict is the structured result of a diagnosis.
type Verdict struct {
	Cause      string
	Confidence koronerv1alpha1.Confidence
	Summary    string
}

// Diagnose maps collected evidence to a cause of death. Ordering matters:
// the most specific, highest-confidence signals are checked first.
func Diagnose(e Evidence) Verdict {
	switch {
	case e.OOMKilled || e.TerminationReason == "OOMKilled":
		return Verdict{CauseOutOfMemory, koronerv1alpha1.ConfidenceHigh,
			"Container terminated by the kernel OOM killer (out of memory)."}

	case hasProbeFailure(e.Events):
		return Verdict{CauseProbeFailure, koronerv1alpha1.ConfidenceHigh,
			"Container killed after a liveness/readiness probe failed repeatedly."}

	case e.PodPhaseReason == "Evicted":
		return Verdict{CauseNodePressure, koronerv1alpha1.ConfidenceHigh,
			"Pod evicted from its node, likely due to resource pressure."}

	case e.WaitingReason == "ImagePullBackOff" || e.WaitingReason == "ErrImagePull":
		return Verdict{CauseImagePullFailed, koronerv1alpha1.ConfidenceHigh,
			"Container image could not be pulled."}

	case e.JobFailed:
		return Verdict{CauseJobFailed, koronerv1alpha1.ConfidenceHigh,
			"Job exhausted its backoff limit without a successful completion."}

	case e.WaitingReason == "CrashLoopBackOff":
		return Verdict{CauseRepeatedCrash, koronerv1alpha1.ConfidenceHigh,
			fmt.Sprintf("Container is crash-looping (%d restarts).", e.RestartCount)}

	case e.Signal != nil && *e.Signal == 9, exitCodeIs(e, 137):
		return Verdict{CauseRepeatedCrash, koronerv1alpha1.ConfidenceMedium,
			"Container received SIGKILL (exit 137); possible OOM or forced kill."}

	case e.ExitCode != nil && *e.ExitCode != 0:
		return Verdict{CauseApplicationErr, koronerv1alpha1.ConfidenceMedium,
			fmt.Sprintf("Application exited with non-zero code %d.", *e.ExitCode)}

	default:
		return Verdict{CauseUnknown, koronerv1alpha1.ConfidenceLow,
			"Cause of death could not be determined from available evidence."}
	}
}

func exitCodeIs(e Evidence, code int32) bool {
	return e.ExitCode != nil && *e.ExitCode == code
}

func hasProbeFailure(events []koronerv1alpha1.EventRecord) bool {
	for _, ev := range events {
		if ev.Reason == "Unhealthy" && strings.Contains(strings.ToLower(ev.Message), "liveness") {
			return true
		}
	}
	return false
}
