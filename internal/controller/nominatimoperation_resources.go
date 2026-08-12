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

package controller

import (
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

const (
	defaultWorkerRepository = "ghcr.io/zebernst/nominatim-worker"
	defaultStagingSize      = "50Gi"

	projectVolumeName  = "project"
	stagingVolumeName  = "staging"
	flatnodeVolumeName = "flatnode"

	projectMountPath  = "/nominatim"
	stagingMountPath  = "/import-staging"
	flatnodeMountPath = "/flatnode"
	flatnodeFilePath  = "/flatnode/flatnode.file"

	reasonConflict            = "Conflict"
	reasonParentNotFound      = "ParentNotFound"
	reasonNotImplemented      = "NotImplemented"
	reasonRegionsRequired     = "RegionsRequired"
	reasonBootstrapIncomplete = "BootstrapIncomplete"
)

// isOperationTypeImplemented reports whether the worker entrypoint can run this type.
// All CRD enum values except unknown/future strings are implemented.
func isOperationTypeImplemented(t nominatimv1alpha1.NominatimOperationType) bool {
	switch t {
	case nominatimv1alpha1.NominatimOperationBootstrap,
		nominatimv1alpha1.NominatimOperationAddRegions,
		nominatimv1alpha1.NominatimOperationReimport,
		nominatimv1alpha1.NominatimOperationUpdate,
		nominatimv1alpha1.NominatimOperationCatchUp,
		nominatimv1alpha1.NominatimOperationRefresh,
		nominatimv1alpha1.NominatimOperationMigrate,
		nominatimv1alpha1.NominatimOperationFreeze:
		return true
	default:
		return false
	}
}

// Mutex policy for NominatimOperations targeting the same Nominatim:
//
// At most one write-heavy Operation (Bootstrap, AddRegions, Reimport, Migrate,
// Freeze) may be Pending or Running per Nominatim. If another write-heavy
// Operation already holds the write plane (Running or Job armed), a new
// Operation is set to Failed with reason Conflict (no Job created). Two fresh
// write-heavy peers without Jobs use a lex-name creation race (requeue, not
// dual Conflict). Peer evaluation lives in evaluateWritePlane
// (nominatimoperation_mutex.go); schedule probes use ScheduleBusy().
//
// Update and CatchUp use the same Conflict policy when a write-heavy Operation
// is active (and vice versa): they may not run alongside write-heavy work.
// Two Update/CatchUp Operations may run concurrently when no write-heavy peer
// is active. Migrate/Freeze are write-heavy so Update cannot run beside them
// (upstream: stop updates before migrate; freeze removes update capability).
func isWriteHeavyOperation(t nominatimv1alpha1.NominatimOperationType) bool {
	switch t {
	case nominatimv1alpha1.NominatimOperationBootstrap,
		nominatimv1alpha1.NominatimOperationAddRegions,
		nominatimv1alpha1.NominatimOperationReimport,
		nominatimv1alpha1.NominatimOperationMigrate,
		nominatimv1alpha1.NominatimOperationFreeze:
		return true
	default:
		return false
	}
}

// requiresRegionsEnv is true for Operations whose worker scripts consume
// NOMINATIM_REGIONS (and optionally PBF_URL). Refresh/Migrate/Freeze act on
// the existing database only and must not inherit parent.spec.regions implicitly.
func requiresRegionsEnv(t nominatimv1alpha1.NominatimOperationType) bool {
	switch t {
	case nominatimv1alpha1.NominatimOperationRefresh,
		nominatimv1alpha1.NominatimOperationMigrate,
		nominatimv1alpha1.NominatimOperationFreeze:
		return false
	default:
		return true
	}
}

// requiresImportPBFURL is true for Operations that run nominatim import with
// Geofabrik extracts (Bootstrap/Reimport). Migrate/Freeze/AddRegions are
// write-heavy for the mutex but do not download PBFs via PBF_URL.
func requiresImportPBFURL(t nominatimv1alpha1.NominatimOperationType) bool {
	switch t {
	case nominatimv1alpha1.NominatimOperationBootstrap,
		nominatimv1alpha1.NominatimOperationReimport:
		return true
	default:
		return false
	}
}

func isActiveOperationPhase(phase nominatimv1alpha1.NominatimOperationPhase) bool {
	switch phase {
	case "", nominatimv1alpha1.NominatimOperationPhasePending, nominatimv1alpha1.NominatimOperationPhaseRunning:
		return true
	default:
		return false
	}
}

func isTerminalOperationPhase(phase nominatimv1alpha1.NominatimOperationPhase) bool {
	return phase == nominatimv1alpha1.NominatimOperationPhaseSucceeded ||
		phase == nominatimv1alpha1.NominatimOperationPhaseFailed
}

// effectiveRegions returns the region set an Operation acts on: Operation.spec.regions
// when set, else falling back to the parent Nominatim's spec.regions. Shared by the
// pre-Job region gate (see requiresRegionGate) and buildOperationJob so both agree on
// what "no regions configured" means.
func effectiveRegions(op *nominatimv1alpha1.NominatimOperation, parent *nominatimv1alpha1.Nominatim) []string {
	if len(op.Spec.Regions) > 0 {
		return op.Spec.Regions
	}
	return parent.Spec.Regions
}

// requiresRegionGate reports whether an Operation type is subject to the pre-Job
// region gate (R1: regions required, R2: Bootstrap-done for regions-mode parents).
// Bootstrap and Reimport intentionally opt out: Bootstrap is how a regions-mode
// parent first gets imported, and Reimport rebuilds from a caller-supplied region set.
func requiresRegionGate(t nominatimv1alpha1.NominatimOperationType) bool {
	switch t {
	case nominatimv1alpha1.NominatimOperationAddRegions,
		nominatimv1alpha1.NominatimOperationUpdate,
		nominatimv1alpha1.NominatimOperationCatchUp:
		return true
	default:
		return false
	}
}

// bootstrapComplete reports whether a regions-mode Nominatim (parent.Spec.Regions
// non-empty) has finished its initial Bootstrap. This is the cluster source of
// truth for "import complete" (not project PVC import-finished):
// either status.regions already has entries, or a peer Bootstrap Operation
// targeting this Nominatim has Succeeded (brief window before status sync).
// PBF-only parents never need this gate — callers check parent.Spec.Regions first.
func bootstrapComplete(parent *nominatimv1alpha1.Nominatim, peers []nominatimv1alpha1.NominatimOperation) bool {
	if len(parent.Status.Regions) > 0 {
		return true
	}
	for i := range peers {
		peer := &peers[i]
		if peer.Spec.Type == nominatimv1alpha1.NominatimOperationBootstrap &&
			peer.Status.Phase == nominatimv1alpha1.NominatimOperationPhaseSucceeded &&
			peer.Spec.NominatimRef.Name == parent.Name {
			return true
		}
	}
	return false
}

func resolveStagingSpec(op *nominatimv1alpha1.NominatimOperation, parent *nominatimv1alpha1.Nominatim) nominatimv1alpha1.StagingSpec {
	out := nominatimv1alpha1.StagingSpec{}
	if parent.Spec.Staging != nil {
		out = *parent.Spec.Staging.DeepCopy()
	}
	if op.Spec.Staging != nil {
		if op.Spec.Staging.Size != "" {
			out.Size = op.Spec.Staging.Size
		}
		if op.Spec.Staging.StorageClassName != nil {
			sc := *op.Spec.Staging.StorageClassName
			out.StorageClassName = &sc
		}
	}
	if out.Size == "" {
		out.Size = defaultStagingSize
	}
	return out
}

func volumeClaimName(vs nominatimv1alpha1.VolumeSource, fallback string) string {
	if vs.ClaimName != "" {
		return vs.ClaimName
	}
	if vs.VolumeClaimTemplate != nil && vs.VolumeClaimTemplate.Metadata.Name != "" {
		return vs.VolumeClaimTemplate.Metadata.Name
	}
	return fallback
}

// workerImageForOperation resolves the Job image from Operation.spec.image,
// then Nominatim.spec.worker.image, else the default worker repository:tag.
func workerImageForOperation(op *nominatimv1alpha1.NominatimOperation, parent *nominatimv1alpha1.Nominatim) string {
	if op != nil && op.Spec.Image != nil {
		return resolveImage(op.Spec.Image, defaultWorkerRepository)
	}
	if parent != nil && parent.Spec.Worker != nil && parent.Spec.Worker.Image != nil {
		return resolveImage(parent.Spec.Worker.Image, defaultWorkerRepository)
	}
	return resolveImage(nil, defaultWorkerRepository)
}

// workerPullPolicyForOperation resolves ImagePullPolicy the same way as workerImageForOperation.
func workerPullPolicyForOperation(op *nominatimv1alpha1.NominatimOperation, parent *nominatimv1alpha1.Nominatim) corev1.PullPolicy {
	if op != nil && op.Spec.Image != nil {
		return resolvePullPolicy(op.Spec.Image)
	}
	if parent != nil && parent.Spec.Worker != nil {
		return resolvePullPolicy(parent.Spec.Worker.Image)
	}
	return ""
}

func phaseFromJob(succeeded, failed, active int32) nominatimv1alpha1.NominatimOperationPhase {
	switch {
	case succeeded > 0:
		return nominatimv1alpha1.NominatimOperationPhaseSucceeded
	case failed > 0:
		return nominatimv1alpha1.NominatimOperationPhaseFailed
	case active > 0:
		return nominatimv1alpha1.NominatimOperationPhaseRunning
	default:
		return nominatimv1alpha1.NominatimOperationPhasePending
	}
}

func stagingPVCName(op *nominatimv1alpha1.NominatimOperation) string {
	return op.Name + "-staging"
}

func buildStagingPVC(op *nominatimv1alpha1.NominatimOperation, staging nominatimv1alpha1.StagingSpec) *corev1.PersistentVolumeClaim {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stagingPVCName(op),
			Namespace: op.Namespace,
			Labels:    operationLabels(op),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(staging.Size),
				},
			},
			StorageClassName: staging.StorageClassName,
		},
	}
	return pvc
}

func operationLabels(op *nominatimv1alpha1.NominatimOperation) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":                "nominatim-operation",
		"app.kubernetes.io/instance":            op.Name,
		"app.kubernetes.io/managed-by":          "nominatim-operator",
		"nominatim.zebernst.dev/nominatim":      op.Spec.NominatimRef.Name,
		"nominatim.zebernst.dev/operation":      op.Name,
		"nominatim.zebernst.dev/operation-type": string(op.Spec.Type),
	}
}

func buildOperationJob(op *nominatimv1alpha1.NominatimOperation, parent *nominatimv1alpha1.Nominatim, stagingClaim, image string, pullPolicy corev1.PullPolicy) (*batchv1.Job, error) {
	projectClaim := volumeClaimName(parent.Spec.Project.Volume, parent.Name+"-project")

	volumes := []corev1.Volume{
		{
			Name: projectVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: projectClaim},
			},
		},
		{
			Name: stagingVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: stagingClaim},
			},
		},
	}
	mounts := []corev1.VolumeMount{
		{Name: projectVolumeName, MountPath: projectMountPath},
		{Name: stagingVolumeName, MountPath: stagingMountPath},
	}

	env := []corev1.EnvVar{
		{Name: "OPERATION_TYPE", Value: string(op.Spec.Type)},
		{Name: "PROJECT_DIR", Value: projectMountPath},
		{Name: "IMPORT_STAGING", Value: stagingMountPath},
	}
	if op.Spec.Type == nominatimv1alpha1.NominatimOperationReimport {
		// Worker refuses Reimport unless the operator explicitly arms it after
		// orchestrating DB/project reset (see images/worker/scripts/reimport.sh).
		env = append(env, corev1.EnvVar{Name: "NOMINATIM_REIMPORT_CONFIRM", Value: "1"})
	}

	if requiresRegionsEnv(op.Spec.Type) {
		regions := effectiveRegions(op, parent)
		if len(regions) > 0 {
			env = append(env, corev1.EnvVar{Name: "NOMINATIM_REGIONS", Value: strings.Join(regions, ",")})
			if requiresImportPBFURL(op.Spec.Type) {
				// PBF_URL is regions[0] for back-compat / first extract URL. The worker
				// downloads every NOMINATIM_REGIONS path and passes multiple --osm-file
				// flags to nominatim import (not add-data — that path is AddRegions only).
				env = append(env, corev1.EnvVar{Name: "PBF_URL", Value: pbfURLForRegion(regions[0])})
			}
		}
	}

	if parent.Status.Database.ConnectionSecretName != "" {
		env = append(env, dbEnvVars(parent.Status.Database.ConnectionSecretName)...)
	}
	env = append(env, effectiveNominatimConfigEnv(parent)...)
	env = append(env, effectiveAuxDataEnv(parent)...)

	if parent.Spec.Flatnode != nil {
		flatClaim := volumeClaimName(parent.Spec.Flatnode.Volume, parent.Name+"-flatnode")
		volumes = append(volumes, corev1.Volume{
			Name: flatnodeVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: flatClaim},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{Name: flatnodeVolumeName, MountPath: flatnodeMountPath})
		env = append(env, corev1.EnvVar{Name: "NOMINATIM_FLATNODE_FILE", Value: flatnodeFilePath})
	}

	backoff := int32(2)
	podSpec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		Containers: []corev1.Container{{
			Name:            "worker",
			Image:           image,
			ImagePullPolicy: pullPolicy,
			Env:             env,
			VolumeMounts:    mounts,
		}},
		Volumes: volumes,
	}
	var workerPodSpec *runtime.RawExtension
	if parent.Spec.Worker != nil {
		workerPodSpec = parent.Spec.Worker.PodSpec
	}
	merged, err := mergePodSpecOverlay(podSpec, workerPodSpec, "worker", image, pullPolicy)
	if err != nil {
		return nil, err
	}
	// Jobs require RestartPolicyNever; do not allow podSpec overlays to change it.
	merged.RestartPolicy = corev1.RestartPolicyNever

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      op.Name,
			Namespace: op.Namespace,
			Labels:    operationLabels(op),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: operationLabels(op),
				},
				Spec: merged,
			},
		},
	}, nil
}

// pbfURLForRegion builds the Geofabrik download URL for a single region path.
// Bootstrap/Reimport Jobs set PBF_URL from regions[0]; the worker still downloads
// all NOMINATIM_REGIONS extracts and passes them as multiple --osm-file flags.
func pbfURLForRegion(region string) string {
	return fmt.Sprintf("https://download.geofabrik.de/%s-latest.osm.pbf", region)
}

func conflictMessage(peer *nominatimv1alpha1.NominatimOperation) string {
	return fmt.Sprintf(
		"%s: another active Operation %q (type=%s, phase=%s) targets the same Nominatim",
		reasonConflict, peer.Name, peer.Spec.Type, peer.Status.Phase,
	)
}
