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

	corev1 "k8s.io/api/core/v1"
)

// PreviousLogs returns the tail of the dead container's logs. It prefers the
// previous (pre-restart) instance - that's where the death is recorded - and
// falls back to the current instance when no previous one exists.
func (c *Collector) PreviousLogs(ctx context.Context, namespace, pod, container string, tailLines int64) (string, error) {
	opts := &corev1.PodLogOptions{Container: container, Previous: true, TailLines: &tailLines}
	req := c.Clientset.CoreV1().Pods(namespace).GetLogs(pod, opts)
	b, err := req.DoRaw(ctx)
	if err == nil {
		return string(b), nil
	}

	// No previous instance (e.g. first crash that hasn't restarted yet, or a
	// pod that never started). Try the current logs before giving up.
	opts.Previous = false
	req = c.Clientset.CoreV1().Pods(namespace).GetLogs(pod, opts)
	b, ferr := req.DoRaw(ctx)
	if ferr != nil {
		// Return the original (previous) error - it's the more informative one.
		return "", err
	}
	return string(b), nil
}
