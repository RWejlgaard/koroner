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
	"context"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
)

// Narrator turns a completed structured investigation into a human-readable
// post-mortem narrative. Phase 1 ships a no-op; an LLM-backed implementation
// can be dropped in later without touching the reconcilers, which already call
// through this interface.
type Narrator interface {
	// Narrate returns prose describing the death. An empty string means
	// "no narrative" and is valid.
	Narrate(ctx context.Context, verdict Verdict, status *koronerv1alpha1.ObituaryStatus) (string, error)
}

// NoopNarrator leaves the narrative empty. It is the default.
type NoopNarrator struct{}

// Narrate implements Narrator by producing nothing.
func (NoopNarrator) Narrate(context.Context, Verdict, *koronerv1alpha1.ObituaryStatus) (string, error) {
	return "", nil
}
