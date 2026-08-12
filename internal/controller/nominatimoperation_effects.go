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

	"sigs.k8s.io/controller-runtime/pkg/client"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

const (
	postgresProfileImport  = "import"
	postgresProfileRuntime = "runtime"
)

// applyPreJobCNPGEffects pauses backups and switches to the import postgres profile before
// this Operation's Job is created, when the parent's pauseBackupsDuringOperations policy
// matches this Operation's type (see operationImpactMatches: Never/BootstrapRebuild/
// WriteHeavy/All). It is a no-op when the policy doesn't cover this Operation type — e.g.
// Update under the default WriteHeavy policy only pauses when the policy is All.
func (r *NominatimOperationReconciler) applyPreJobCNPGEffects(ctx context.Context, op *nominatimv1alpha1.NominatimOperation, parent *nominatimv1alpha1.NominatimInstance) error {
	if !operationImpactMatches(parent.Spec.Database.PauseBackupsDuringOperations, op.Spec.Type) {
		return nil
	}
	if err := setBackupPaused(ctx, r.Client, r.cnpgEffects(), parent, true); err != nil {
		return err
	}
	return applyPostgresProfile(ctx, r.Client, r.cnpgEffects(), parent, postgresProfileImport)
}

// applyTerminalCNPGEffects resumes backups and switches back to the runtime postgres profile
// once this Operation reaches Succeeded or Failed — but only when no other still-active
// Operation on the same parent matches the pause policy. Without that sibling check, a
// Conflict-failed peer would resume backups / revert the postgres profile while a Running
// Bootstrap is still importing.
func (r *NominatimOperationReconciler) applyTerminalCNPGEffects(ctx context.Context, op *nominatimv1alpha1.NominatimOperation, parent *nominatimv1alpha1.NominatimInstance) error {
	impact := parent.Spec.Database.PauseBackupsDuringOperations
	if !operationImpactMatches(impact, op.Spec.Type) {
		return nil
	}
	stillPaused, err := r.hasOtherActiveMatchingImpactOps(ctx, parent, impact, op.Name)
	if err != nil {
		return err
	}
	if stillPaused {
		return nil
	}
	if err := setBackupPaused(ctx, r.Client, r.cnpgEffects(), parent, false); err != nil {
		return err
	}
	return applyPostgresProfile(ctx, r.Client, r.cnpgEffects(), parent, postgresProfileRuntime)
}

// hasOtherActiveMatchingImpactOps reports whether another Pending/Running Operation against
// the same parent still matches impact (and therefore should keep backups paused / import
// profile applied). excludeName is this Operation (the one that just went terminal or is
// being deleted).
func (r *NominatimOperationReconciler) hasOtherActiveMatchingImpactOps(
	ctx context.Context,
	parent *nominatimv1alpha1.NominatimInstance,
	impact nominatimv1alpha1.OperationImpact,
	excludeName string,
) (bool, error) {
	peers := &nominatimv1alpha1.NominatimOperationList{}
	if err := r.List(ctx, peers, client.InNamespace(parent.Namespace)); err != nil {
		return false, err
	}
	for i := range peers.Items {
		peer := &peers.Items[i]
		if peer.Name == excludeName {
			continue
		}
		if peer.Spec.NominatimInstanceRef.Name != parent.Name {
			continue
		}
		if !isActiveOperationPhase(peer.Status.Phase) {
			continue
		}
		if operationImpactMatches(impact, peer.Spec.Type) {
			return true, nil
		}
	}
	return false, nil
}

// setBackupPaused pauses or resumes continuous (Barman) backup on the attached CNPG Cluster.
// No-op when the database is not yet attached, degraded, or connectionSecretRef-only.
func setBackupPaused(ctx context.Context, c client.Client, effects CNPGEffects, nom *nominatimv1alpha1.NominatimInstance, paused bool) error {
	if skipCNPGClusterEffects(nom) {
		return nil
	}
	clusterName := nom.Status.Database.ClusterName
	if clusterName == "" {
		return fmt.Errorf("cannot pause backups: no CNPG cluster in status")
	}
	cluster, err := getCNPGCluster(ctx, c, nom.Namespace, clusterName)
	if err != nil {
		return err
	}
	if paused {
		return effects.PauseBackups(ctx, cluster)
	}
	return effects.ResumeBackups(ctx, cluster)
}

// applyPostgresProfile applies import or runtime postgresProfiles to the CNPG Cluster.
//
// Profile-managed keys are the union of import ∪ runtime. When switching profiles, keys that
// belong to that union but are absent from the target profile are removed so import-only knobs
// (e.g. work_mem) do not leak into "runtime" forever. Keys outside the union are left alone.
func applyPostgresProfile(ctx context.Context, c client.Client, effects CNPGEffects, nom *nominatimv1alpha1.NominatimInstance, which string) error {
	if skipCNPGClusterEffects(nom) {
		return nil
	}
	var params map[string]string
	profiles := nom.Spec.Database.PostgresProfiles
	switch which {
	case postgresProfileImport:
		if profiles != nil {
			params = profiles.Import
		}
	case postgresProfileRuntime:
		if profiles != nil {
			params = profiles.Runtime
		}
	default:
		return fmt.Errorf("unknown postgres profile %q (want import or runtime)", which)
	}
	removeKeys := profileKeysToRemove(profiles, params)
	if len(params) == 0 && len(removeKeys) == 0 {
		return nil
	}
	clusterName := nom.Status.Database.ClusterName
	if clusterName == "" {
		return fmt.Errorf("cannot apply postgres profile: no CNPG cluster in status")
	}
	cluster, err := getCNPGCluster(ctx, c, nom.Namespace, clusterName)
	if err != nil {
		return err
	}
	return effects.ApplyParameters(ctx, cluster, params, removeKeys)
}

// skipCNPGClusterEffects is true when there is no operator-managed Cluster to patch:
// not yet attached, degraded, or connectionSecretRef-only.
func skipCNPGClusterEffects(nom *nominatimv1alpha1.NominatimInstance) bool {
	mode := nom.Status.Database.Mode
	return mode == "" || nom.Status.Database.Degraded || mode == nominatimv1alpha1.DatabaseModeConnectionSecret
}

// profileKeysToRemove returns profile-managed keys (import ∪ runtime) that are not present in
// the target profile map, so ApplyParameters can delete them when switching profiles.
func profileKeysToRemove(profiles *nominatimv1alpha1.PostgresProfiles, target map[string]string) []string {
	if profiles == nil {
		return nil
	}
	managed := map[string]struct{}{}
	for k := range profiles.Import {
		managed[k] = struct{}{}
	}
	for k := range profiles.Runtime {
		managed[k] = struct{}{}
	}
	var remove []string
	for k := range managed {
		if _, ok := target[k]; !ok {
			remove = append(remove, k)
		}
	}
	return remove
}
