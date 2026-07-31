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
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CNPGBackupPausedAnnotation marks a CNPG Cluster as having continuous backup paused by this
// operator around write-heavy Operations.
//
// CNPG does not (as of v1) expose a first-class "pause continuous backup" field — the closest
// primitive, `cnpg.io/hibernation`, hibernates the whole cluster (stops Postgres itself), which
// is not what we want while an Operation's Job is actively writing to the database. For v1alpha1
// operator scope we instead record pause intent as an annotation: it's a clear, inspectable
// marker of "the operator currently considers backups paused for this Cluster" that dashboards
// or future Barman/CNPG-I plugin wiring can key off, without touching cluster health.
const CNPGBackupPausedAnnotation = "nominatim.zebernst.dev/backup-paused"

// CNPGEffects abstracts Barman backup pause and postgres parameter patches for CNPG Clusters.
// Operations (nominatim-vzw) call these hooks; degraded connectionSecretRef mode must never
// invoke them (guarded in setBackupPaused/applyPostgresProfile, not here).
type CNPGEffects interface {
	PauseBackups(ctx context.Context, cluster *unstructured.Unstructured) error
	ResumeBackups(ctx context.Context, cluster *unstructured.Unstructured) error
	// ApplyParameters merges params into spec.postgresql.parameters, then deletes removeKeys.
	// removeKeys clears stale profile-managed knobs (e.g. import-only work_mem) when switching
	// to a profile that does not redefine them. Non-managed / unrelated keys are preserved.
	ApplyParameters(ctx context.Context, cluster *unstructured.Unstructured, params map[string]string, removeKeys []string) error
}

// defaultCNPGEffects is the real CNPGEffects implementation: PauseBackups/ResumeBackups
// set/clear CNPGBackupPausedAnnotation, and ApplyParameters merges params into
// spec.postgresql.parameters. Both patch the live Cluster via Client.Update.
type defaultCNPGEffects struct {
	Client client.Client
}

func (e defaultCNPGEffects) PauseBackups(ctx context.Context, cluster *unstructured.Unstructured) error {
	return e.setBackupPausedAnnotation(ctx, cluster, true)
}

func (e defaultCNPGEffects) ResumeBackups(ctx context.Context, cluster *unstructured.Unstructured) error {
	return e.setBackupPausedAnnotation(ctx, cluster, false)
}

func (e defaultCNPGEffects) setBackupPausedAnnotation(ctx context.Context, cluster *unstructured.Unstructured, paused bool) error {
	annotations := cluster.GetAnnotations()
	if paused {
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[CNPGBackupPausedAnnotation] = annotationValueTrue
	} else if annotations != nil {
		delete(annotations, CNPGBackupPausedAnnotation)
	}
	cluster.SetAnnotations(annotations)
	return e.Client.Update(ctx, cluster)
}

// ApplyParameters merges params into spec.postgresql.parameters (creating the map if absent),
// deletes removeKeys, and patches the Cluster. A no-op when both params and removeKeys are empty.
func (e defaultCNPGEffects) ApplyParameters(ctx context.Context, cluster *unstructured.Unstructured, params map[string]string, removeKeys []string) error {
	if len(params) == 0 && len(removeKeys) == 0 {
		return nil
	}
	merged, _, err := unstructured.NestedStringMap(cluster.Object, "spec", "postgresql", "parameters")
	if err != nil {
		return fmt.Errorf("read spec.postgresql.parameters: %w", err)
	}
	if merged == nil {
		merged = map[string]string{}
	}
	for _, k := range removeKeys {
		delete(merged, k)
	}
	for k, v := range params {
		merged[k] = v
	}
	if err := unstructured.SetNestedStringMap(cluster.Object, merged, "spec", "postgresql", "parameters"); err != nil {
		return fmt.Errorf("set spec.postgresql.parameters: %w", err)
	}
	return e.Client.Update(ctx, cluster)
}

// cnpgEffects returns r.CNPGEffects when set (test override), else defaultCNPGEffects backed
// by r's own client.
func (r *NominatimReconciler) cnpgEffects() CNPGEffects {
	if r.CNPGEffects != nil {
		return r.CNPGEffects
	}
	return defaultCNPGEffects{Client: r.Client}
}

// cnpgEffects returns r.CNPGEffects when set (test override), else defaultCNPGEffects backed
// by r's own client.
func (r *NominatimOperationReconciler) cnpgEffects() CNPGEffects {
	if r.CNPGEffects != nil {
		return r.CNPGEffects
	}
	return defaultCNPGEffects{Client: r.Client}
}
