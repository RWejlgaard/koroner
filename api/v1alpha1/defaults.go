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

package v1alpha1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Default policy values applied when a KoronerConfig is absent or leaves
// fields unset. Reconcilers read through these helpers so behaviour is sane
// out of the box.
const (
	DefaultRestartThreshold  int32         = 3
	DefaultLogTailLines      int32         = 200
	DefaultObituaryRetention time.Duration = 7 * 24 * time.Hour
)

// DefaultKoronerConfigSpec returns the policy used when no KoronerConfig exists.
func DefaultKoronerConfigSpec() KoronerConfigSpec {
	return (&KoronerConfigSpec{}).WithDefaults()
}

// WithDefaults returns a copy of the spec with all unset fields filled in.
func (s *KoronerConfigSpec) WithDefaults() KoronerConfigSpec {
	out := *s
	out.Watch.PodCrashes = orTrue(s.Watch.PodCrashes)
	out.Watch.JobFailures = orTrue(s.Watch.JobFailures)
	out.Watch.RolloutFailures = orFalse(s.Watch.RolloutFailures)
	out.Watch.Evictions = orFalse(s.Watch.Evictions)
	if out.RestartThreshold == nil {
		v := DefaultRestartThreshold
		out.RestartThreshold = &v
	}
	if out.LogTailLines == nil {
		v := DefaultLogTailLines
		out.LogTailLines = &v
	}
	if out.ObituaryRetention == nil {
		out.ObituaryRetention = &metav1.Duration{Duration: DefaultObituaryRetention}
	}
	return out
}

func orTrue(b *bool) *bool {
	if b == nil {
		v := true
		return &v
	}
	return b
}

func orFalse(b *bool) *bool {
	if b == nil {
		v := false
		return &v
	}
	return b
}
