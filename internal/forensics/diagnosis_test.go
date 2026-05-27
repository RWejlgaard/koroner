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

package forensics

import (
	"testing"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
)

func i32(v int32) *int32 { return &v }

func TestDiagnose(t *testing.T) {
	cases := []struct {
		name      string
		evidence  Evidence
		wantCause string
		wantConf  koronerv1alpha1.Confidence
	}{
		{
			name:      "OOM by flag",
			evidence:  Evidence{OOMKilled: true},
			wantCause: CauseOutOfMemory, wantConf: koronerv1alpha1.ConfidenceHigh,
		},
		{
			name:      "OOM by reason",
			evidence:  Evidence{TerminationReason: "OOMKilled"},
			wantCause: CauseOutOfMemory, wantConf: koronerv1alpha1.ConfidenceHigh,
		},
		{
			name: "liveness probe failure",
			evidence: Evidence{Events: []koronerv1alpha1.EventRecord{
				{Reason: "Unhealthy", Message: "Liveness probe failed: connection refused"},
			}},
			wantCause: CauseProbeFailure, wantConf: koronerv1alpha1.ConfidenceHigh,
		},
		{
			name:      "evicted",
			evidence:  Evidence{PodPhaseReason: "Evicted"},
			wantCause: CauseNodePressure, wantConf: koronerv1alpha1.ConfidenceHigh,
		},
		{
			name:      "image pull",
			evidence:  Evidence{WaitingReason: "ImagePullBackOff"},
			wantCause: CauseImagePullFailed, wantConf: koronerv1alpha1.ConfidenceHigh,
		},
		{
			name:      "job failed",
			evidence:  Evidence{JobFailed: true},
			wantCause: CauseJobFailed, wantConf: koronerv1alpha1.ConfidenceHigh,
		},
		{
			name:      "crashloop",
			evidence:  Evidence{WaitingReason: "CrashLoopBackOff", RestartCount: 5},
			wantCause: CauseRepeatedCrash, wantConf: koronerv1alpha1.ConfidenceHigh,
		},
		{
			name:      "sigkill exit 137",
			evidence:  Evidence{ExitCode: i32(137)},
			wantCause: CauseRepeatedCrash, wantConf: koronerv1alpha1.ConfidenceMedium,
		},
		{
			name:      "app error",
			evidence:  Evidence{ExitCode: i32(1)},
			wantCause: CauseApplicationErr, wantConf: koronerv1alpha1.ConfidenceMedium,
		},
		{
			name:      "unknown",
			evidence:  Evidence{},
			wantCause: CauseUnknown, wantConf: koronerv1alpha1.ConfidenceLow,
		},
		{
			name:      "OOM wins over crashloop",
			evidence:  Evidence{OOMKilled: true, WaitingReason: "CrashLoopBackOff"},
			wantCause: CauseOutOfMemory, wantConf: koronerv1alpha1.ConfidenceHigh,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Diagnose(tc.evidence)
			if got.Cause != tc.wantCause {
				t.Errorf("cause = %q, want %q", got.Cause, tc.wantCause)
			}
			if got.Confidence != tc.wantConf {
				t.Errorf("confidence = %q, want %q", got.Confidence, tc.wantConf)
			}
			if got.Summary == "" {
				t.Error("summary is empty")
			}
		})
	}
}
