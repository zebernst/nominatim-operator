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

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

const (
	defaultWorkerImage = "ghcr.io/zebernst/nominatim-worker"
	defaultStagingSize = "50Gi"

	// workerImageAnnotation overrides the Operation Job worker image when set on the Operation.
	workerImageAnnotation = "nominatim.zebernst.dev/worker-image"

	projectVolumeName  = "project"
	stagingVolumeName  = "staging"
	flatnodeVolumeName = "flatnode"

	projectMountPath  = "/nominatim"
	stagingMountPath  = "/import-staging"
	flatnodeMountPath = "/flatnode"
	flatnodeFilePath  = "/flatnode/flatnode.file"

	reasonConflict       = "Conflict"
	reasonParentNotFound = "ParentNotFound"
)

// Mutex policy for NominatimOperations targeting the same Nominatim:
//
// At most one write-heavy Operation (Bootstrap, AddRegions, Reimport) may be
// Pending or Running per Nominatim. If another write-heavy Operation is already
// active, a new Operation is set to Failed with reason Conflict (no Job created).
//
// Update and CatchUp use the same Conflict policy when a write-heavy Operation
// is active (and vice versa): they may not run alongside write-heavy work.
// Two Update/CatchUp Operations may run concurrently when no write-heavy peer
// is active.
func isWriteHeavyOperation(t nominatimv1alpha1.NominatimOperationType) bool {
	switch t {
	case nominatimv1alpha1.NominatimOperationBootstrap,
		nominatimv1alpha1.NominatimOperationAddRegions,
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

func findConflictingOperation(op *nominatimv1alpha1.NominatimOperation, peers []nominatimv1alpha1.NominatimOperation) *nominatimv1alpha1.NominatimOperation {
	for i := range peers {
		peer := &peers[i]
		if peer.Name == op.Name || peer.Namespace != op.Namespace {
			continue
		}
		if peer.Spec.NominatimRef.Name != op.Spec.NominatimRef.Name {
			continue
		}
		if !isActiveOperationPhase(peer.Status.Phase) {
			continue
		}
		// Conflict when either side is write-heavy (see mutex policy comment above).
		if isWriteHeavyOperation(op.Spec.Type) || isWriteHeavyOperation(peer.Spec.Type) {
			return peer
		}
	}
	return nil
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

func workerImageForOperation(op *nominatimv1alpha1.NominatimOperation) string {
	if op.Annotations != nil {
		if img := strings.TrimSpace(op.Annotations[workerImageAnnotation]); img != "" {
			return img
		}
	}
	return defaultWorkerImage
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

func buildOperationJob(op *nominatimv1alpha1.NominatimOperation, parent *nominatimv1alpha1.Nominatim, stagingClaim, image string) *batchv1.Job {
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

	regions := op.Spec.Regions
	if len(regions) == 0 {
		regions = parent.Spec.Regions
	}
	if len(regions) > 0 {
		env = append(env, corev1.EnvVar{Name: "NOMINATIM_REGIONS", Value: strings.Join(regions, ",")})
		if isWriteHeavyOperation(op.Spec.Type) {
			// First region drives the initial PBF download; multi-region imports add the
			// rest via NOMINATIM_REGIONS (already set above).
			env = append(env, corev1.EnvVar{Name: "PBF_URL", Value: pbfURLForRegion(regions[0])})
		}
	}

	if parent.Status.Database.ConnectionSecretName != "" {
		env = append(env, dbEnvVars(parent.Status.Database.ConnectionSecretName)...)
	}

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
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:         "worker",
						Image:        image,
						Env:          env,
						VolumeMounts: mounts,
					}},
					Volumes: volumes,
				},
			},
		},
	}
}

// pbfURLForRegion builds the Geofabrik download URL for a single region path. Only the
// first region drives the initial PBF download; additional regions are imported via
// NOMINATIM_REGIONS (worker-side), not additional PBF_URL values.
func pbfURLForRegion(region string) string {
	return fmt.Sprintf("https://download.geofabrik.de/%s-latest.osm.pbf", region)
}

func conflictMessage(peer *nominatimv1alpha1.NominatimOperation) string {
	return fmt.Sprintf(
		"%s: another active Operation %q (type=%s, phase=%s) targets the same Nominatim",
		reasonConflict, peer.Name, peer.Spec.Type, peer.Status.Phase,
	)
}
