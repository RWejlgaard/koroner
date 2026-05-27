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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	koronerv1alpha1 "github.com/RWejlgaard/koroner/api/v1alpha1"
)

// maxOwnerDepth caps the ownership walk so a pathological owner cycle can't
// loop forever (Pod -> ReplicaSet -> Deployment is depth 2).
const maxOwnerDepth = 6

// OwnerChain walks the deceased's controller ownerReferences upward
// (e.g. Pod -> ReplicaSet -> Deployment) and returns the chain. Best-effort:
// a hop that can't be fetched ends the walk rather than failing the whole
// investigation.
func (c *Collector) OwnerChain(ctx context.Context, namespace string, owners []metav1.OwnerReference) []koronerv1alpha1.OwnerRecord {
	var chain []koronerv1alpha1.OwnerRecord
	for depth := 0; depth < maxOwnerDepth; depth++ {
		ref := controllerRef(owners)
		if ref == nil {
			break
		}
		chain = append(chain, koronerv1alpha1.OwnerRecord{
			APIVersion: ref.APIVersion,
			Kind:       ref.Kind,
			Name:       ref.Name,
		})

		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			break
		}
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gv.WithKind(ref.Kind))
		if err := c.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, obj); err != nil {
			break
		}
		owners = obj.GetOwnerReferences()
	}
	return chain
}

// TopOwner returns the root of an ownership chain (e.g. the Deployment behind a
// Pod, or the CronJob behind a Job). Returns the zero value when the chain is
// empty, meaning the deceased had no controller.
func TopOwner(chain []koronerv1alpha1.OwnerRecord) koronerv1alpha1.OwnerRecord {
	if len(chain) == 0 {
		return koronerv1alpha1.OwnerRecord{}
	}
	return chain[len(chain)-1]
}

// controllerRef returns the controlling owner reference, falling back to the
// first reference when none is explicitly marked as the controller.
func controllerRef(owners []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range owners {
		if owners[i].Controller != nil && *owners[i].Controller {
			return &owners[i]
		}
	}
	if len(owners) > 0 {
		return &owners[0]
	}
	return nil
}
