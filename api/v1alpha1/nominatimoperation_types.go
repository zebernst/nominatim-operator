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

// NominatimOperationType is the kind of finite workflow to run against a Nominatim instance.
// +kubebuilder:validation:Enum=Bootstrap;AddRegions;Reimport;Update;CatchUp;Refresh;Migrate;Freeze
type NominatimOperationType string

const (
	// NominatimOperationBootstrap performs initial import when the instance is empty/unready.
	NominatimOperationBootstrap NominatimOperationType = "Bootstrap"
	// NominatimOperationAddRegions imports newly listed regions without wiping existing data.
	NominatimOperationAddRegions NominatimOperationType = "AddRegions"
	// NominatimOperationReimport rebuilds the database for a (possibly reduced) region set.
	NominatimOperationReimport NominatimOperationType = "Reimport"
	// NominatimOperationUpdate applies incremental Geofabrik-style updates.
	NominatimOperationUpdate NominatimOperationType = "Update"
	// NominatimOperationCatchUp forces a catch-up / refresh-style update through the same queue.
	NominatimOperationCatchUp NominatimOperationType = "CatchUp"

	// NominatimOperationRefresh is reserved for a future refresh workflow (accepted; not implemented yet).
	NominatimOperationRefresh NominatimOperationType = "Refresh"
	// NominatimOperationMigrate is reserved for a future migrate workflow (accepted; not implemented yet).
	NominatimOperationMigrate NominatimOperationType = "Migrate"
	// NominatimOperationFreeze is reserved for a future freeze workflow (accepted; not implemented yet).
	NominatimOperationFreeze NominatimOperationType = "Freeze"
)

// NominatimOperationPhase is the high-level progress of an operation.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type NominatimOperationPhase string

const (
	// NominatimOperationPhasePending means the operation is queued or not yet started.
	NominatimOperationPhasePending NominatimOperationPhase = "Pending"
	// NominatimOperationPhaseRunning means the underlying Job (or prep work) is in progress.
	NominatimOperationPhaseRunning NominatimOperationPhase = "Running"
	// NominatimOperationPhaseSucceeded means the operation completed successfully.
	NominatimOperationPhaseSucceeded NominatimOperationPhase = "Succeeded"
	// NominatimOperationPhaseFailed means the operation failed.
	NominatimOperationPhaseFailed NominatimOperationPhase = "Failed"
)

// NominatimOperationSpec defines the desired state of NominatimOperation.
//
// NominatimOperation is an imperative, finite workflow — NOT a GitOps desired-state surface.
// Do not manage NominatimOperation objects via Flux Kustomizations or similar continuous apply;
// create them with kubectl (or let the Nominatim controller create them) and let them complete.
type NominatimOperationSpec struct {
	// Type selects the workflow to run.
	// +kubebuilder:validation:Required
	Type NominatimOperationType `json:"type"`

	// NominatimRef names the Nominatim instance in the same namespace.
	// The operation controller will set an ownerReference to that Nominatim when reconciling
	// (callers need not set metadata.ownerReferences).
	// +kubebuilder:validation:Required
	NominatimRef LocalObjectReference `json:"nominatimRef"`

	// Regions are Geofabrik-style region paths targeted by AddRegions or Reimport.
	// Ignored for other operation types unless documented otherwise by the controller.
	// +optional
	Regions []string `json:"regions,omitempty"`

	// Staging overrides Nominatim.spec.staging for this operation's download PVC
	// (size and/or storageClass). When unset, the parent Nominatim staging defaults apply.
	// +optional
	Staging *StagingSpec `json:"staging,omitempty"`
}

// NominatimOperationStatus defines the observed state of NominatimOperation.
type NominatimOperationStatus struct {
	// Phase is the high-level operation progress (Pending, Running, Succeeded, Failed).
	// +optional
	Phase NominatimOperationPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations of the operation.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// JobRef names the batch Job owned by this operation, when created.
	// +optional
	JobRef *LocalObjectReference `json:"jobRef,omitempty"`

	// ObservedGeneration is the last reconciled generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Message is a human-readable summary of the current state.
	// +optional
	Message string `json:"message,omitempty"`

	// StartTime is when the operation began running.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the operation reached a terminal phase (Succeeded or Failed).
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=nomop
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NominatimOperation is a finite, controller- or kubectl-created workflow against a Nominatim
// instance (bootstrap, region changes, updates, etc.). It wraps batch Jobs and optional
// operation-scoped staging PVCs.
//
// NominatimOperation is NOT intended for Flux/GitOps continuous reconciliation. Treat it like
// a Job: create it to kick work, observe status until Succeeded/Failed, then leave or delete.
// Desired long-lived state belongs on the Nominatim CR; consequential ops use NominatimOperation.
type NominatimOperation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NominatimOperationSpec   `json:"spec,omitempty"`
	Status NominatimOperationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NominatimOperationList contains a list of NominatimOperation.
type NominatimOperationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NominatimOperation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NominatimOperation{}, &NominatimOperationList{})
}
