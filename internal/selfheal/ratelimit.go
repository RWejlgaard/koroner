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
	"sync"
	"time"
)

// rateLimiter enforces a per-namespace cap on heal actions inside a rolling
// one-hour window. It is in-memory by design: on operator restart the limiter
// resets to zero, which is the conservative direction (delays heals, never
// permits more than configured during steady-state operation).
type rateLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	window  time.Duration
	history map[string][]time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{
		now:     time.Now,
		window:  time.Hour,
		history: make(map[string][]time.Time),
	}
}

// reserve returns true and records a timestamp if the namespace has had fewer
// than maxPerWindow actions in the rolling window; otherwise returns false and
// does not record.
func (r *rateLimiter) reserve(namespace string, maxPerWindow int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	cutoff := now.Add(-r.window)
	kept := r.history[namespace][:0]
	for _, t := range r.history[namespace] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= maxPerWindow {
		r.history[namespace] = kept
		return false
	}
	r.history[namespace] = append(kept, now)
	return true
}
