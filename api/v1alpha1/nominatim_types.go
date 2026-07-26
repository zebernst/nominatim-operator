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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ReconcileAtAnnotation is the Flux/ExternalSecrets-style nudge annotation.
	// Set to an RFC3339 timestamp (or any changing value) to request reconciliation.
	// There is NO request=<type> annotation control surface — use NominatimOperation for consequential ops.
	ReconcileAtAnnotation = "nominatim.zebernst.dev/reconcile-at"
)

// RegionChangePolicy controls how newly listed regions are imported.
// +kubebuilder:validation:Enum=AddData;Reimport
type RegionChangePolicy string

const (
	// RegionChangeAddData imports new regions without wiping existing data (default).
	RegionChangeAddData RegionChangePolicy = "AddData"
	// RegionChangeReimport rebuilds the database for region set changes.
	RegionChangeReimport RegionChangePolicy = "Reimport"
)

// OperationImpact selects which operation classes trigger a side effect (API suspend, backup pause).
// +kubebuilder:validation:Enum=Never;BootstrapReimport;WriteHeavy;All
type OperationImpact string

const (
	OperationImpactNever OperationImpact = "Never"
	// OperationImpactBootstrapReimport covers Bootstrap and Reimport only (not AddRegions/Update).
	OperationImpactBootstrapReimport OperationImpact = "BootstrapReimport"
	// OperationImpactWriteHeavy covers Bootstrap, AddRegions, and Reimport.
	OperationImpactWriteHeavy OperationImpact = "WriteHeavy"
	OperationImpactAll        OperationImpact = "All"
)

// VolumeSource references an existing PVC and/or describes a PVC to create.
// At least one of ClaimName or VolumeClaimTemplate must be set.
// +kubebuilder:validation:XValidation:rule="has(self.claimName) || has(self.volumeClaimTemplate)",message="volume requires claimName and/or volumeClaimTemplate"
type VolumeSource struct {
	// ClaimName references an existing PersistentVolumeClaim in the same namespace.
	// +optional
	ClaimName string `json:"claimName,omitempty"`

	// VolumeClaimTemplate describes a PVC for the operator to create/own when ClaimName is empty
	// (or to use as a template when creating a dedicated claim).
	// +optional
	VolumeClaimTemplate *VolumeClaimTemplate `json:"volumeClaimTemplate,omitempty"`
}

// VolumeClaimTemplate is a minimal PVC template (storage size + optional storage class + access modes).
type VolumeClaimTemplate struct {
	// Metadata for the generated PVC (name/labels/annotations). Name may be operator-derived when empty.
	// +optional
	Metadata metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec of the PersistentVolumeClaim.
	Spec corev1.PersistentVolumeClaimSpec `json:"spec"`
}

// ProjectSpec is the Nominatim project directory (PROJECT_DIR) volume and settings.
type ProjectSpec struct {
	// Volume is the required project directory PVC (Nominatim docs: project).
	// +kubebuilder:validation:Required
	Volume VolumeSource `json:"volume"`
}

// FlatnodeSpec optionally places the flatnode file on a dedicated PVC (perf-sensitive).
// When unset, flatnode may live on the project volume.
type FlatnodeSpec struct {
	// Volume for the flatnode file PVC.
	// +kubebuilder:validation:Required
	Volume VolumeSource `json:"volume"`
}

// StagingSpec defaults for operation-scoped staging PVCs (PBF/aux downloads; not emptyDir).
type StagingSpec struct {
	// StorageClassName for staging PVCs.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// Size is the default staging PVC size (e.g. 50Gi).
	// +optional
	Size string `json:"size,omitempty"`
}

// LocalObjectReference is a same-namespace object name.
type LocalObjectReference struct {
	// Name of the referent.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// DatabaseClusterCreate creates a postgresql.cnpg.io/v1 Cluster.
type DatabaseClusterCreate struct {
	// Storage for the CNPG Cluster (passthrough size/storageClass; no node-specific assumptions).
	// +optional
	Storage *VolumeClaimTemplate `json:"storage,omitempty"`

	// Instances is the desired CNPG instance count.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Instances *int32 `json:"instances,omitempty"`
}

// PostgresProfiles holds CNPG-oriented postgres parameter profiles (import vs runtime).
type PostgresProfiles struct {
	// Import profile name or inline knobs for write-heavy import phases.
	// +optional
	Import map[string]string `json:"import,omitempty"`

	// Runtime profile for steady-state API serving.
	// +optional
	Runtime map[string]string `json:"runtime,omitempty"`
}

// DatabaseSpec attaches or creates Postgres for this Nominatim instance.
// Exactly one of Cluster, ClusterRef, or ConnectionSecretRef must be set.
// +kubebuilder:validation:XValidation:rule="(has(self.cluster) ? 1 : 0) + (has(self.clusterRef) ? 1 : 0) + (has(self.connectionSecretRef) ? 1 : 0) == 1",message="database requires exactly one of cluster, clusterRef, or connectionSecretRef"
type DatabaseSpec struct {
	// Cluster creates a CNPG Cluster owned by this Nominatim.
	// +optional
	Cluster *DatabaseClusterCreate `json:"cluster,omitempty"`

	// ClusterRef attaches an existing CNPG Cluster (preferred when not creating).
	// +optional
	ClusterRef *LocalObjectReference `json:"clusterRef,omitempty"`

	// ConnectionSecretRef is a thin escape hatch to any Postgres (degraded: no Cluster manage,
	// no param profiles, no backup pause).
	// +optional
	ConnectionSecretRef *LocalObjectReference `json:"connectionSecretRef,omitempty"`

	// PostgresProfiles for CNPG-attached modes only.
	// +optional
	PostgresProfiles *PostgresProfiles `json:"postgresProfiles,omitempty"`

	// PauseBackupsDuringOperations controls continuous backup pausing around operations.
	// Default WriteHeavy pauses Bootstrap/AddRegions/Reimport but not routine Update.
	// +optional
	// +kubebuilder:default="WriteHeavy"
	PauseBackupsDuringOperations OperationImpact `json:"pauseBackupsDuringOperations,omitempty"`
}

// UpdatesSpec schedules automatic Update NominatimOperations (no CronJob in v1).
type UpdatesSpec struct {
	// Enabled turns on controller-driven Update operations when due.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Schedule is a cron expression evaluated by the Nominatim controller.
	// +optional
	Schedule string `json:"schedule,omitempty"`
}

// ImageSpec identifies a container image.
type ImageSpec struct {
	// Repository is the image repository (e.g. ghcr.io/zebernst/nominatim-api).
	// +optional
	Repository string `json:"repository,omitempty"`

	// Tag is the image tag.
	// +optional
	Tag string `json:"tag,omitempty"`

	// PullPolicy for the container.
	// +optional
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// ParentReference mirrors Gateway API parentRefs for HTTPRoute attachment (local struct; no gateway-api dep).
type ParentReference struct {
	// Group of the referent. Defaults to gateway.networking.k8s.io when empty.
	// +optional
	Group *string `json:"group,omitempty"`

	// Kind of the referent. Defaults to Gateway when empty.
	// +optional
	Kind *string `json:"kind,omitempty"`

	// Namespace of the referent. Defaults to local namespace when empty.
	// +optional
	Namespace *string `json:"namespace,omitempty"`

	// Name of the referent.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// SectionName is an optional section on the parent (e.g. listener name).
	// +optional
	SectionName *string `json:"sectionName,omitempty"`
}

// RouteSpec configures an HTTPRoute when the operator should own Gateway API routing.
type RouteSpec struct {
	// ParentRefs attach the HTTPRoute to Gateways.
	// +optional
	ParentRefs []ParentReference `json:"parentRefs,omitempty"`

	// Hostnames for the HTTPRoute.
	// +optional
	Hostnames []string `json:"hostnames,omitempty"`
}

// APISpec configures the Nominatim API Deployment and optional route.
type APISpec struct {
	// Replicas for the API Deployment.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`

	// Image for the API container.
	// +optional
	Image *ImageSpec `json:"image,omitempty"`

	// Route configures Gateway API HTTPRoute when set.
	// +optional
	Route *RouteSpec `json:"route,omitempty"`

	// SuspendDuringOperations controls scaling/suspending the API during operations.
	// Default Never keeps the API up (homelab default).
	// +optional
	// +kubebuilder:default="Never"
	SuspendDuringOperations OperationImpact `json:"suspendDuringOperations,omitempty"`
}

// UISpec configures an optional Nominatim UI Deployment and route.
type UISpec struct {
	// Replicas for the UI Deployment.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`

	// Image for the UI container.
	// +optional
	Image *ImageSpec `json:"image,omitempty"`

	// Route configures Gateway API HTTPRoute when set.
	// +optional
	Route *RouteSpec `json:"route,omitempty"`
}

// NominatimSpec defines the desired state of Nominatim (GitOps surface).
type NominatimSpec struct {
	// Project is the Nominatim project directory settings (required volume).
	// +kubebuilder:validation:Required
	Project ProjectSpec `json:"project"`

	// Flatnode optionally places flatnode on a dedicated PVC.
	// +optional
	Flatnode *FlatnodeSpec `json:"flatnode,omitempty"`

	// Staging defaults for operation-scoped download PVCs.
	// +optional
	Staging *StagingSpec `json:"staging,omitempty"`

	// Regions is the desired set of Geofabrik-style region paths (e.g. north-america/us).
	// Removal from this list does not delete DB data — a Reimport (or putting the region back) is required to shrink.
	// +optional
	Regions []string `json:"regions,omitempty"`

	// RegionChangePolicy controls how new regions are applied. Defaults to AddData.
	// +optional
	// +kubebuilder:default="AddData"
	RegionChangePolicy RegionChangePolicy `json:"regionChangePolicy,omitempty"`

	// Database attaches or creates Postgres for this instance.
	// +kubebuilder:validation:Required
	Database DatabaseSpec `json:"database"`

	// Updates schedules automatic Update operations.
	// +optional
	Updates *UpdatesSpec `json:"updates,omitempty"`

	// API configures the geocoding API workload.
	// +optional
	API *APISpec `json:"api,omitempty"`

	// UI configures an optional UI workload.
	// +optional
	UI *UISpec `json:"ui,omitempty"`
}

// RegionStatus is the observed state of an imported region.
type RegionStatus struct {
	// Name is the Geofabrik-style region path.
	Name string `json:"name"`

	// Phase is a short status string (e.g. Imported, Updating, Error).
	// +optional
	Phase string `json:"phase,omitempty"`

	// SequenceState reflects the last known update sequence identity when available.
	// +optional
	SequenceState string `json:"sequenceState,omitempty"`

	// LastUpdatedTime is when this region status was last refreshed.
	// +optional
	LastUpdatedTime *metav1.Time `json:"lastUpdatedTime,omitempty"`

	// Message is human-readable detail.
	// +optional
	Message string `json:"message,omitempty"`
}

// DatabaseMode identifies how Nominatim is attached to Postgres.
const (
	// DatabaseModeClusterManaged means the operator owns a CNPG Cluster (spec.database.cluster).
	DatabaseModeClusterManaged = "ClusterManaged"
	// DatabaseModeClusterAttached means the operator watches an existing CNPG Cluster (spec.database.clusterRef).
	DatabaseModeClusterAttached = "ClusterAttached"
	// DatabaseModeConnectionSecret means degraded mode via an arbitrary connection Secret.
	DatabaseModeConnectionSecret = "ConnectionSecret"
)

// DatabaseStatus reports observed database attachment for API/worker wiring.
type DatabaseStatus struct {
	// Mode is ClusterManaged, ClusterAttached, or ConnectionSecret.
	// +optional
	Mode string `json:"mode,omitempty"`

	// ConnectionSecretName is the Secret name API/worker should use for Postgres credentials.
	// +optional
	ConnectionSecretName string `json:"connectionSecretName,omitempty"`

	// ClusterName is the CNPG Cluster name when managing or attached (empty in degraded mode).
	// +optional
	ClusterName string `json:"clusterName,omitempty"`

	// Degraded is true when using connectionSecretRef (no Cluster manage, no profiles, no backup pause).
	// +optional
	Degraded bool `json:"degraded,omitempty"`
}

// NominatimStatus defines the observed state of Nominatim.
type NominatimStatus struct {
	// Conditions represent the latest available observations of the instance.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Regions is the source of truth for imported regions (replaces imported-regions.txt).
	// +optional
	Regions []RegionStatus `json:"regions,omitempty"`

	// Database reports connection secret and CNPG attachment mode.
	// +optional
	Database DatabaseStatus `json:"database,omitempty"`

	// ObservedGeneration is the last reconciled generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ActiveOperationRefs references NominatimOperation(s) currently in progress.
	// +optional
	ActiveOperationRefs []corev1.ObjectReference `json:"activeOperationRefs,omitempty"`

	// LastUpdateScheduleTime is the last cron fire time for which the controller created
	// (or intentionally skipped) a scheduled Update NominatimOperation. Used as the schedule
	// cursor so fires are not double-created across reconciles.
	// +optional
	LastUpdateScheduleTime *metav1.Time `json:"lastUpdateScheduleTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=nom
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Nominatim is the Schema for the nominatims API (GitOps desired state for one install).
type Nominatim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NominatimSpec   `json:"spec,omitempty"`
	Status NominatimStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NominatimList contains a list of Nominatim.
type NominatimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Nominatim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Nominatim{}, &NominatimList{})
}
