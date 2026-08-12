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
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	// ReconcileAtAnnotation is the Flux/ExternalSecrets-style nudge annotation.
	// Set to an RFC3339 timestamp (or any changing value) to request reconciliation.
	// There is NO request=<type> annotation control surface — use NominatimOperation for consequential ops.
	ReconcileAtAnnotation = "nominatim.zebernst.dev/reconcile-at"
)

// RegionChangePolicy controls how newly listed regions are imported.
// +kubebuilder:validation:Enum=AddData;Rebuild
type RegionChangePolicy string

const (
	// RegionChangeAddData imports new regions without wiping existing data (default).
	RegionChangeAddData RegionChangePolicy = "AddData"
	// RegionChangeRebuild rebuilds the database for region set changes.
	RegionChangeRebuild RegionChangePolicy = "Rebuild"
)

// OperationImpact selects which operation classes trigger a side effect (API suspend, backup pause).
// +kubebuilder:validation:Enum=Never;BootstrapRebuild;WriteHeavy;All
type OperationImpact string

const (
	OperationImpactNever OperationImpact = "Never"
	// OperationImpactBootstrapRebuild covers Bootstrap and Rebuild only (not AddRegions/Update).
	OperationImpactBootstrapRebuild OperationImpact = "BootstrapRebuild"
	// OperationImpactWriteHeavy covers Bootstrap, AddRegions, Rebuild, Migrate, and Freeze.
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

// AuxDataSpec toggles optional Nominatim auxiliary datasets downloaded to the
// operation staging PVC and symlinked into the project directory before import.
type AuxDataSpec struct {
	// WikimediaImportance downloads wikimedia-importance.csv.gz from nominatim.org/data.
	// +optional
	WikimediaImportance *bool `json:"wikimediaImportance,omitempty"`

	// SecondaryImportance downloads wikimedia-secondary-importance.sql.gz as
	// secondary_importance.sql.gz in the project directory.
	// +optional
	SecondaryImportance *bool `json:"secondaryImportance,omitempty"`

	// USPostcodes downloads us_postcodes.csv.gz (TIGER-derived US postcodes).
	// +optional
	USPostcodes *bool `json:"usPostcodes,omitempty"`
}

// AuxDataStatus reports which auxiliary datasets are present on the project volume.
type AuxDataStatus struct {
	// WikimediaImportance is true when wikimedia-importance.csv.gz is present and non-empty.
	// +optional
	WikimediaImportance bool `json:"wikimediaImportance,omitempty"`

	// SecondaryImportance is true when secondary_importance.sql.gz is present and non-empty.
	// +optional
	SecondaryImportance bool `json:"secondaryImportance,omitempty"`

	// USPostcodes is true when us_postcodes.csv.gz is present and non-empty.
	// +optional
	USPostcodes bool `json:"usPostcodes,omitempty"`
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

// DatabaseClusterRef attaches an existing CNPG Cluster in the same namespace.
type DatabaseClusterRef struct {
	// Name of the CNPG Cluster.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// ConnectionSecretRef optionally names the Secret with Postgres credentials for
	// API/worker (CNPG-shaped keys: uri, host, port, dbname, user, password).
	// When unset, defaults to the Cluster's application Secret "{name}-app".
	// +optional
	ConnectionSecretRef *LocalObjectReference `json:"connectionSecretRef,omitempty"`
}

// DatabaseClusterCreate creates a postgresql.cnpg.io/v1 Cluster owned by this NominatimInstance.
//
// This is an instance-tune surface (instances, storage, resources, affinity,
// topologySpreadConstraints) — not a full CNPG ClusterSpec. Operator-owned fields
// (PostGIS imageName, create-only bootstrap.initdb, managed www-data role) are not
// exposed here. For full CNPG control (backup, certificates, custom bootstrap), use
// database.clusterRef to attach a hand-authored Cluster.
type DatabaseClusterCreate struct {
	// Storage for the CNPG Cluster (passthrough size/storageClass; no node-specific assumptions).
	// +optional
	Storage *VolumeClaimTemplate `json:"storage,omitempty"`

	// Instances is the desired CNPG instance count.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Instances *int32 `json:"instances,omitempty"`

	// Resources for every generated Postgres instance Pod (CNPG spec.resources).
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Affinity is a CNPG AffinityConfiguration overlay (nodeSelector, tolerations,
	// enablePodAntiAffinity, topologyKey, …) written to spec.affinity.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Affinity *runtime.RawExtension `json:"affinity,omitempty"`

	// TopologySpreadConstraints maps to CNPG Cluster spec.topologySpreadConstraints.
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
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

// DatabaseSpec attaches or creates Postgres for this NominatimInstance.
// Exactly one of Cluster, ClusterRef, or ConnectionSecretRef must be set.
// +kubebuilder:validation:XValidation:rule="(has(self.cluster) ? 1 : 0) + (has(self.clusterRef) ? 1 : 0) + (has(self.connectionSecretRef) ? 1 : 0) == 1",message="database requires exactly one of cluster, clusterRef, or connectionSecretRef"
type DatabaseSpec struct {
	// Cluster creates a CNPG Cluster owned by this NominatimInstance.
	// +optional
	Cluster *DatabaseClusterCreate `json:"cluster,omitempty"`

	// ClusterRef attaches an existing CNPG Cluster (preferred when not creating).
	// Optional clusterRef.connectionSecretRef overrides the default "{cluster}-app" Secret.
	// +optional
	ClusterRef *DatabaseClusterRef `json:"clusterRef,omitempty"`

	// ConnectionSecretRef is a thin escape hatch to any Postgres (degraded: no Cluster manage,
	// no param profiles, no backup pause). Mutually exclusive with cluster / clusterRef.
	// +optional
	ConnectionSecretRef *LocalObjectReference `json:"connectionSecretRef,omitempty"`

	// PostgresProfiles for CNPG-attached modes only.
	// +optional
	PostgresProfiles *PostgresProfiles `json:"postgresProfiles,omitempty"`

	// PauseBackupsDuringOperations controls continuous backup pausing around operations.
	// Default WriteHeavy pauses Bootstrap/AddRegions/Rebuild/Migrate/Freeze but not routine Update.
	// +optional
	// +kubebuilder:default="WriteHeavy"
	PauseBackupsDuringOperations OperationImpact `json:"pauseBackupsDuringOperations,omitempty"`
}

// UpdatesSpec schedules automatic Update NominatimOperations (no CronJob in v1).
type UpdatesSpec struct {
	// Enabled turns on controller-driven Update operations when due.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Schedule is a cron expression evaluated by the NominatimInstance controller.
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

	// Image for the API container (sole supported image hatch; podSpec.containers[].image is ignored).
	// +optional
	Image *ImageSpec `json:"image,omitempty"`

	// GunicornWorkers sets GUNICORN_WORKERS for the API container.
	// When unset, the entrypoint derives the worker count from the container's
	// cgroup CPU limit (ceil(quota/period)), falling back to nproc.
	// +optional
	// +kubebuilder:validation:Minimum=1
	GunicornWorkers *int32 `json:"gunicornWorkers,omitempty"`

	// PodSpec is a user overlay merged into the operator-built API PodSpec (affinity,
	// tolerations, resources, probes, sidecars, …). Not a DeploymentSpec passthrough.
	// The operator sets default HTTP probes on /status (startup/readiness/liveness);
	// podSpec can override them when needed.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	PodSpec *runtime.RawExtension `json:"podSpec,omitempty"`

	// Route configures Gateway API HTTPRoute when set.
	// +optional
	Route *RouteSpec `json:"route,omitempty"`

	// SuspendDuringOperations controls scaling the API during day-2 operations
	// (AddRegions, Rebuild, Update, …). Default Never keeps the API up.
	// Day-0 Bootstrap is not covered here: API/UI are not created until Bootstrap
	// has populated status.regions (see servingWorkloadsAllowed).
	// +optional
	// +kubebuilder:default="Never"
	SuspendDuringOperations OperationImpact `json:"suspendDuringOperations,omitempty"`
}

// NominatimAPIConfigSpec holds Python frontend runtime knobs (Nominatim dotenv /
// NOMINATIM_* env). Applies on every API/worker reconcile; not sealed after Bootstrap.
// See https://nominatim.org/release-docs/latest/customize/Settings/
type NominatimAPIConfigSpec struct {
	// PoolSize is NOMINATIM_API_POOL_SIZE (DB connections per gunicorn worker).
	// +optional
	// +kubebuilder:validation:Minimum=1
	PoolSize *int32 `json:"poolSize,omitempty"`

	// QueryTimeoutSeconds is NOMINATIM_QUERY_TIMEOUT (SQL query cancel timeout).
	// +optional
	// +kubebuilder:validation:Minimum=0
	QueryTimeoutSeconds *int32 `json:"queryTimeoutSeconds,omitempty"`

	// RequestTimeoutSeconds is NOMINATIM_REQUEST_TIMEOUT (max request duration).
	// +optional
	// +kubebuilder:validation:Minimum=0
	RequestTimeoutSeconds *int32 `json:"requestTimeoutSeconds,omitempty"`

	// DefaultLanguage is NOMINATIM_DEFAULT_LANGUAGE.
	// +optional
	DefaultLanguage string `json:"defaultLanguage,omitempty"`

	// CorsNoAccessControl maps to NOMINATIM_CORS_NOACCESSCONTROL (yes/no).
	// When true, API responses send permissive CORS headers.
	// +optional
	CorsNoAccessControl *bool `json:"corsNoAccessControl,omitempty"`
}

// UISpec configures an optional Nominatim UI Deployment and route.
// Image defaults to ghcr.io/zebernst/nominatim-ui (upstream osm-search/nominatim-ui release package).
// Omit spec.ui, or set enabled=false, for API- and database-only serving (no UI workloads).
type UISpec struct {
	// Enabled turns on the UI Deployment, Service, and optional HTTPRoute.
	// Defaults to true when spec.ui is set. Set to false to keep API/DB-only while
	// retaining UI image/route config in the CR (operator deletes any owned UI objects).
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// Replicas for the UI Deployment.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`

	// Image for the UI container. Defaults to ghcr.io/zebernst/nominatim-ui:latest.
	// Sole supported image hatch; podSpec.containers[].image is ignored.
	// +optional
	Image *ImageSpec `json:"image,omitempty"`

	// PodSpec is a user overlay merged into the operator-built UI PodSpec.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	PodSpec *runtime.RawExtension `json:"podSpec,omitempty"`

	// Route configures Gateway API HTTPRoute when set.
	// +optional
	Route *RouteSpec `json:"route,omitempty"`
}

// WorkerSpec configures the NominatimOperation Job worker image used for
// Bootstrap / AddRegions / Rebuild / Update Jobs.
type WorkerSpec struct {
	// Image for Operation Job containers. Defaults to ghcr.io/zebernst/nominatim-worker:latest.
	// Sole supported image hatch; podSpec.containers[].image is ignored.
	// +optional
	Image *ImageSpec `json:"image,omitempty"`

	// PodSpec is a user overlay merged into Operation Job PodSpecs (including Update Jobs).
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	PodSpec *runtime.RawExtension `json:"podSpec,omitempty"`
}

// NominatimReplicationSpec maps to Nominatim replication-related dotenv settings.
type NominatimReplicationSpec struct {
	// URL is NOMINATIM_REPLICATION_URL (upstream extract update base).
	// +optional
	URL string `json:"url,omitempty"`

	// MaxDiff is NOMINATIM_REPLICATION_MAX_DIFF.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxDiff *int32 `json:"maxDiff,omitempty"`

	// UpdateIntervalSeconds is NOMINATIM_REPLICATION_UPDATE_INTERVAL.
	// +optional
	// +kubebuilder:validation:Minimum=0
	UpdateIntervalSeconds *int32 `json:"updateIntervalSeconds,omitempty"`

	// RecheckIntervalSeconds is NOMINATIM_REPLICATION_RECHECK_INTERVAL.
	// +optional
	// +kubebuilder:validation:Minimum=0
	RecheckIntervalSeconds *int32 `json:"recheckIntervalSeconds,omitempty"`
}

// NominatimConfigSpec is the typed Nominatim settings surface (dotenv / NOMINATIM_* env).
//
// Import-time fields (ImportStyle, Tokenizer) cannot be changed after Bootstrap without
// a Rebuild — the controller seals observed values into status and surfaces
// ImportConfigDrift when spec diverges. Runtime fields (Languages, Replication, API)
// apply on every reconcile.
type NominatimConfigSpec struct {
	// ImportStyle is NOMINATIM_IMPORT_STYLE (admin, street, address, full, extratags, or a path).
	// Immutable after Bootstrap. Image default is extratags when unset.
	// +optional
	ImportStyle string `json:"importStyle,omitempty"`

	// Tokenizer is NOMINATIM_TOKENIZER (e.g. icu). Immutable after Bootstrap.
	// +optional
	Tokenizer string `json:"tokenizer,omitempty"`

	// Languages is NOMINATIM_LANGUAGES (joined with commas).
	// +optional
	Languages []string `json:"languages,omitempty"`

	// Replication holds optional replication URL / interval / maxDiff settings.
	// +optional
	Replication *NominatimReplicationSpec `json:"replication,omitempty"`

	// API holds Python frontend runtime settings (pool, timeouts, CORS, default language).
	// +optional
	API *NominatimAPIConfigSpec `json:"api,omitempty"`
}

// ObservedNominatimConfig records import-time settings sealed when Bootstrap first succeeded.
type ObservedNominatimConfig struct {
	// ImportStyle sealed at Bootstrap (empty means image default was used).
	// +optional
	ImportStyle string `json:"importStyle,omitempty"`

	// Tokenizer sealed at Bootstrap (empty means image default was used).
	// +optional
	Tokenizer string `json:"tokenizer,omitempty"`
}

// NominatimInstanceSpec defines the desired state of a NominatimInstance (GitOps surface).
type NominatimInstanceSpec struct {
	// Project is the Nominatim project directory settings (required volume).
	// +kubebuilder:validation:Required
	Project ProjectSpec `json:"project"`

	// Flatnode optionally places flatnode on a dedicated PVC.
	// +optional
	Flatnode *FlatnodeSpec `json:"flatnode,omitempty"`

	// Staging defaults for operation-scoped download PVCs.
	// +optional
	Staging *StagingSpec `json:"staging,omitempty"`

	// AuxData controls optional Wikipedia importance and external postcode downloads
	// during Bootstrap (and Refresh backfill when enabled).
	// +optional
	AuxData *AuxDataSpec `json:"auxData,omitempty"`

	// Regions is the desired set of Geofabrik-style region paths (e.g. north-america/us).
	// Removal from this list does not delete DB data — a Rebuild (or putting the region back) is required to shrink.
	// +optional
	Regions []string `json:"regions,omitempty"`

	// RegionChangePolicy controls how new regions are applied. Defaults to AddData.
	// +optional
	// +kubebuilder:default="AddData"
	RegionChangePolicy RegionChangePolicy `json:"regionChangePolicy,omitempty"`

	// Nominatim is typed Nominatim dotenv/settings (import style, tokenizer, languages, replication).
	// Prefer this over setting NOMINATIM_* via podSpec; reserved operator env keys remain sealed.
	// +optional
	Nominatim *NominatimConfigSpec `json:"nominatim,omitempty"`

	// Database attaches or creates Postgres for this instance.
	// +kubebuilder:validation:Required
	Database DatabaseSpec `json:"database"`

	// Updates schedules automatic Update operations.
	// +optional
	Updates *UpdatesSpec `json:"updates,omitempty"`

	// API configures the geocoding API workload.
	// +optional
	API *APISpec `json:"api,omitempty"`

	// UI configures an optional UI workload. Omit or set ui.enabled=false for API/DB-only.
	// +optional
	UI *UISpec `json:"ui,omitempty"`

	// Worker configures the image used by NominatimOperation Jobs.
	// +optional
	Worker *WorkerSpec `json:"worker,omitempty"`
}

// RegionStatus is the observed state of an imported region.
// Presence in status.regions is the cluster source of truth that the region is imported;
// there is no per-region lifecycle phase (Operations carry Pending/Running/Succeeded/Failed).
type RegionStatus struct {
	// Name is the Geofabrik-style region path.
	Name string `json:"name"`

	// SequenceState is the last known Geofabrik update identity for this region
	// (typically "sequenceNumber@timestamp" from update/<region>/sequence.state),
	// filled by an operator-owned probe Job after Succeeded write Operations.
	// +optional
	SequenceState string `json:"sequenceState,omitempty"`

	// LastUpdatedTime is when this region entry was last written (observe or sequence probe).
	// +optional
	LastUpdatedTime *metav1.Time `json:"lastUpdatedTime,omitempty"`
}

// DatabaseMode identifies how a NominatimInstance is attached to Postgres.
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

// NominatimInstanceStatus defines the observed state of a NominatimInstance.
type NominatimInstanceStatus struct {
	// Conditions represent the latest available observations of the instance.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Regions is the cluster source of truth for which Geofabrik regions are imported
	// (synced from Succeeded Bootstrap/AddRegions/Rebuild). Project PVC files such as
	// imported-regions.txt / import-finished are worker-local resume bookmarks only.
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

	// ObservedNominatim records import-time Nominatim settings sealed at Bootstrap.
	// +optional
	ObservedNominatim *ObservedNominatimConfig `json:"observedNominatim,omitempty"`

	// AuxData reports observed auxiliary dataset files on the project volume.
	// +optional
	AuxData *AuxDataStatus `json:"auxData,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=nom
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NominatimInstance is the Schema for the nominatiminstances API
// (GitOps desired state for one managed install of Nominatim).
type NominatimInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NominatimInstanceSpec   `json:"spec,omitempty"`
	Status NominatimInstanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NominatimInstanceList contains a list of NominatimInstance.
type NominatimInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NominatimInstance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NominatimInstance{}, &NominatimInstanceList{})
}
