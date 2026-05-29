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
	"testing"
	"time"
)

func TestRateLimiterAllowsUpToCap(t *testing.T) {
	r := newRateLimiter()
	for i := 0; i < 3; i++ {
		if !r.reserve("ns-a", 3) {
			t.Fatalf("call %d should be allowed", i)
		}
	}
	if r.reserve("ns-a", 3) {
		t.Fatal("4th call should be denied")
	}
}

func TestRateLimiterIsPerNamespace(t *testing.T) {
	r := newRateLimiter()
	for i := 0; i < 2; i++ {
		if !r.reserve("ns-a", 2) {
			t.Fatalf("ns-a call %d should be allowed", i)
		}
	}
	if !r.reserve("ns-b", 2) {
		t.Fatal("ns-b should have its own budget")
	}
}

func TestRateLimiterRollsOff(t *testing.T) {
	r := newRateLimiter()
	frozen := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return frozen }

	for i := 0; i < 2; i++ {
		r.reserve("ns-a", 2)
	}
	if r.reserve("ns-a", 2) {
		t.Fatal("third call should be denied at t0")
	}

	// Advance past the window.
	r.now = func() time.Time { return frozen.Add(time.Hour + time.Minute) }
	if !r.reserve("ns-a", 2) {
		t.Fatal("call after window roll-off should be allowed")
	}
}
