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
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
)

// Events returns the deceased's Kubernetes events as an ordered timeline,
// oldest first. Filtered server-side by the involved object's UID.
func (c *Collector) Events(ctx context.Context, namespace, uid string) ([]koronerv1alpha1.EventRecord, error) {
	sel := fields.OneTermEqualSelector("involvedObject.uid", uid).String()
	list, err := c.Clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: sel})
	if err != nil {
		return nil, err
	}

	records := make([]koronerv1alpha1.EventRecord, 0, len(list.Items))
	for i := range list.Items {
		ev := &list.Items[i]
		t := eventTime(ev)
		records = append(records, koronerv1alpha1.EventRecord{
			Time:    t,
			Type:    ev.Type,
			Reason:  ev.Reason,
			Message: ev.Message,
		})
	}

	sort.SliceStable(records, func(a, b int) bool {
		if records[a].Time == nil || records[b].Time == nil {
			return records[a].Time != nil // entries with a time sort first
		}
		return records[a].Time.Before(records[b].Time)
	})
	return records, nil
}

// eventTime picks the most meaningful timestamp an Event carries. The core/v1
// Event API exposes several, populated inconsistently across event sources.
func eventTime(ev *corev1.Event) *metav1.Time {
	switch {
	case !ev.LastTimestamp.IsZero():
		return &ev.LastTimestamp
	case !ev.EventTime.IsZero():
		t := metav1.NewTime(ev.EventTime.Time)
		return &t
	case !ev.FirstTimestamp.IsZero():
		return &ev.FirstTimestamp
	default:
		return nil
	}
}
