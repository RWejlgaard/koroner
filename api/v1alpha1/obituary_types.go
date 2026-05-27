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

// Subject identifies the deceased workload an Obituary is written for.
type Subject struct {
	// apiVersion of the deceased object, e.g. "v1" or "batch/v1".
	// +required
	APIVersion string `json:"apiVersion"`
	// kind of the deceased object, e.g. "Pod" or "Job".
	// +required
	Kind string `json:"kind"`
	// name of the deceased object.
	// +required
	Name string `json:"name"`
	// namespace of the deceased object.
	// +required
	Namespace string `json:"namespace"`
	// uid of the deceased object at time of death.
	// +optional
	UID string `json:"uid,omitempty"`
}

// ObituarySpec is the immutable record of who died and when. It is populated
// once by Koroner at creation and never reconciled toward a desired state.
type ObituarySpec struct {
	// subject is the deceased workload this obituary documents.
	// +required
	Subject Subject `json:"subject"`

	// timeOfDeath is when the death was detected.
	// +optional
	TimeOfDeath *metav1.Time `json:"timeOfDeath,omitempty"`

	// deathEpisodeKey deduplicates obituaries: one death episode -> one obituary.
	// Typically "<uid>/<container>/<restartCount>".
	// +required
	DeathEpisodeKey string `json:"deathEpisodeKey"`
}

// EventRecord is a single entry in the death timeline, distilled from a
// Kubernetes Event involving the deceased.
type EventRecord struct {
	// +optional
	Time *metav1.Time `json:"time,omitempty"`
	// type is the Event type, e.g. "Normal" or "Warning".
	// +optional
	Type string `json:"type,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// OwnerRecord is one hop in the deceased's ownership chain
// (e.g. Pod -> ReplicaSet -> Deployment).
type OwnerRecord struct {
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
	// +optional
	Kind string `json:"kind,omitempty"`
	// +optional
	Name string `json:"name,omitempty"`
}

// MetricsSnapshot holds optional Prometheus-sourced numbers around time of
// death. Only populated when a KoronerConfig enables Prometheus.
type MetricsSnapshot struct {
	// source describes where these numbers came from, e.g. the Prometheus URL.
	// +optional
	Source string `json:"source,omitempty"`
	// samples maps a metric label (e.g. "memory_bytes") to a human-readable value.
	// +optional
	Samples map[string]string `json:"samples,omitempty"`
}

// Confidence expresses how sure the heuristic diagnosis is.
// +kubebuilder:validation:Enum=Low;Medium;High
type Confidence string

const (
	ConfidenceLow    Confidence = "Low"
	ConfidenceMedium Confidence = "Medium"
	ConfidenceHigh   Confidence = "High"
)

// ObituaryPhase tracks the investigation lifecycle.
// +kubebuilder:validation:Enum=Investigating;Complete;Failed
type ObituaryPhase string

const (
	PhaseInvestigating ObituaryPhase = "Investigating"
	PhaseComplete      ObituaryPhase = "Complete"
	PhaseFailed        ObituaryPhase = "Failed"
)

// ObituaryStatus is the forensic finding assembled by Koroner.
type ObituaryStatus struct {
	// phase tracks the investigation lifecycle.
	// +optional
	Phase ObituaryPhase `json:"phase,omitempty"`

	// occurrences is how many distinct death episodes have rolled up into this
	// obituary. An obituary represents a known issue - one (workload, container,
	// cause) - so a crash-looping or repeatedly-recreated workload increments
	// this rather than spawning new obituaries.
	// +optional
	Occurrences int32 `json:"occurrences,omitempty"`

	// firstSeen is when this issue was first observed.
	// +optional
	FirstSeen *metav1.Time `json:"firstSeen,omitempty"`

	// lastSeen is when this issue was most recently observed.
	// +optional
	LastSeen *metav1.Time `json:"lastSeen,omitempty"`

	// lastEpisode is an internal discriminator ("<podUID>/<restartCount>") for
	// the most recently counted death, so the same crash isn't counted twice
	// across reconciles.
	// +optional
	LastEpisode string `json:"lastEpisode,omitempty"`

	// lastPod is the name of the most recent dead pod whose evidence (logs,
	// exit code) is reflected below. Useful when the subject is a workload that
	// recreates pods.
	// +optional
	LastPod string `json:"lastPod,omitempty"`

	// causeOfDeath is the heuristic verdict, e.g. "OutOfMemory", "RepeatedCrash".
	// +optional
	CauseOfDeath string `json:"causeOfDeath,omitempty"`

	// confidence is how sure the verdict is.
	// +optional
	Confidence Confidence `json:"confidence,omitempty"`

	// summary is a one-line human-readable cause.
	// +optional
	Summary string `json:"summary,omitempty"`

	// exitCode of the dead container, when applicable.
	// +optional
	ExitCode *int32 `json:"exitCode,omitempty"`

	// signal that terminated the container, when applicable.
	// +optional
	Signal *int32 `json:"signal,omitempty"`

	// oomKilled is true when the kernel OOM killer was involved.
	// +optional
	OOMKilled bool `json:"oomKilled,omitempty"`

	// restartCount observed at time of death.
	// +optional
	RestartCount int32 `json:"restartCount,omitempty"`

	// logTail is the captured tail of the previous container's logs.
	// +optional
	LogTail string `json:"logTail,omitempty"`

	// eventTimeline is the ordered list of relevant Kubernetes events.
	// +optional
	EventTimeline []EventRecord `json:"eventTimeline,omitempty"`

	// owners is the deceased's ownership chain.
	// +optional
	Owners []OwnerRecord `json:"owners,omitempty"`

	// metrics is an optional Prometheus snapshot around time of death.
	// +optional
	Metrics *MetricsSnapshot `json:"metrics,omitempty"`

	// narrative is a human-readable post-mortem. Empty in phase 1; reserved for
	// the LLM-backed Narrator.
	// +optional
	Narrative string `json:"narrative,omitempty"`

	// conditions represent the current state of the Obituary resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=obit
// +kubebuilder:printcolumn:name="Cause",type=string,JSONPath=`.status.causeOfDeath`
// +kubebuilder:printcolumn:name="Subject",type=string,JSONPath=`.spec.subject.name`
// +kubebuilder:printcolumn:name="Occurrences",type=integer,JSONPath=`.status.occurrences`
// +kubebuilder:printcolumn:name="Confidence",type=string,JSONPath=`.status.confidence`
// +kubebuilder:printcolumn:name="Last-Seen",type=date,JSONPath=`.status.lastSeen`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Obituary is the Schema for the obituaries API
type Obituary struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Obituary
	// +required
	Spec ObituarySpec `json:"spec"`

	// status defines the observed state of Obituary
	// +optional
	Status ObituaryStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ObituaryList contains a list of Obituary
type ObituaryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Obituary `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Obituary{}, &ObituaryList{})
}
