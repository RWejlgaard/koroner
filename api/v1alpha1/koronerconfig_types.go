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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// WatchPolicy toggles which classes of death Koroner investigates.
type WatchPolicy struct {
	// podCrashes covers CrashLoopBackOff, OOMKilled, non-zero exits, and
	// restart-threshold breaches. Defaults to true.
	// +optional
	PodCrashes *bool `json:"podCrashes,omitempty"`
	// jobFailures covers batch Jobs that exhaust their backoffLimit.
	// Defaults to true.
	// +optional
	JobFailures *bool `json:"jobFailures,omitempty"`
	// rolloutFailures covers stuck/failed Deployment & StatefulSet rollouts.
	// Defaults to false (phase 2).
	// +optional
	RolloutFailures *bool `json:"rolloutFailures,omitempty"`
	// evictions covers pods killed by node pressure or preemption.
	// Defaults to false (phase 2).
	// +optional
	Evictions *bool `json:"evictions,omitempty"`
}

// PrometheusPolicy configures optional metric collection.
type PrometheusPolicy struct {
	// enabled turns Prometheus evidence collection on. Defaults to false.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// url is the base address of the Prometheus HTTP API.
	// +optional
	URL string `json:"url,omitempty"`
	// queries optionally overrides the default PromQL templates, keyed by
	// metric label. {{.Pod}}, {{.Namespace}} are substituted.
	// +optional
	Queries map[string]string `json:"queries,omitempty"`
}

// KoronerConfigSpec is the runtime policy for Koroner. A config named
// "default" in the operator's namespace acts as the cluster-wide fallback;
// per-namespace configs override it for their namespace.
type KoronerConfigSpec struct {
	// watch toggles which death classes are investigated.
	// +optional
	Watch WatchPolicy `json:"watch,omitempty"`

	// restartThreshold is how many restarts before a crashing container is
	// declared dead. Defaults to 3.
	// +optional
	// +kubebuilder:validation:Minimum=1
	RestartThreshold *int32 `json:"restartThreshold,omitempty"`

	// logTailLines is how many lines of previous-container logs to capture.
	// Defaults to 200.
	// +optional
	// +kubebuilder:validation:Minimum=0
	LogTailLines *int32 `json:"logTailLines,omitempty"`

	// namespaceSelector limits which namespaces are watched. Empty selects all.
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// obituaryRetention is how long obituaries are kept before reaping.
	// Defaults to "168h" (7d). Reaper is phase 2.
	// +optional
	ObituaryRetention *metav1.Duration `json:"obituaryRetention,omitempty"`

	// prometheus configures optional metric evidence.
	// +optional
	Prometheus PrometheusPolicy `json:"prometheus,omitempty"`
}

// KoronerConfigStatus defines the observed state of KoronerConfig.
type KoronerConfigStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the KoronerConfig resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// KoronerConfig is the Schema for the koronerconfigs API
type KoronerConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of KoronerConfig
	// +required
	Spec KoronerConfigSpec `json:"spec"`

	// status defines the observed state of KoronerConfig
	// +optional
	Status KoronerConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// KoronerConfigList contains a list of KoronerConfig
type KoronerConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []KoronerConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KoronerConfig{}, &KoronerConfigList{})
}
