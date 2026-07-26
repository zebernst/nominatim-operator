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

	"sigs.k8s.io/controller-runtime/pkg/client"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// setParentBackupPaused pauses/resumes CNPG backups on the parent's attached Cluster.
//
// It no-ops while the parent's Nominatim controller hasn't attached a database yet
// (status.database.mode unset — there is no Cluster to act on), in addition to the
// degraded/connectionSecretRef guard already inside setBackupPaused itself.
func (r *NominatimOperationReconciler) setParentBackupPaused(ctx context.Context, parent *nominatimv1alpha1.Nominatim, paused bool) error {
	if parent.Status.Database.Mode == "" {
		return nil
	}
	return setBackupPaused(ctx, r.Client, r.cnpgEffects(), parent, paused)
}

// applyParentPostgresProfile applies the import/runtime postgres profile to the parent's
// attached Cluster, subject to the same not-yet-attached guard as setParentBackupPaused.
func (r *NominatimOperationReconciler) applyParentPostgresProfile(ctx context.Context, parent *nominatimv1alpha1.Nominatim, which string) error {
	if parent.Status.Database.Mode == "" {
		return nil
	}
	return applyPostgresProfile(ctx, r.Client, r.cnpgEffects(), parent, which)
}

// applyPreJobCNPGEffects pauses backups and switches to the import postgres profile before
// this Operation's Job is created, when the parent's pauseBackupsDuringOperations policy
// matches this Operation's type (see operationImpactMatches: Never/BootstrapReimport/
// WriteHeavy/All). It is a no-op when the policy doesn't cover this Operation type — e.g.
// Update under the default WriteHeavy policy only pauses when the policy is All.
func (r *NominatimOperationReconciler) applyPreJobCNPGEffects(ctx context.Context, op *nominatimv1alpha1.NominatimOperation, parent *nominatimv1alpha1.Nominatim) error {
	if !operationImpactMatches(parent.Spec.Database.PauseBackupsDuringOperations, op.Spec.Type) {
		return nil
	}
	if err := r.setParentBackupPaused(ctx, parent, true); err != nil {
		return err
	}
	return r.applyParentPostgresProfile(ctx, parent, "import")
}

// applyTerminalCNPGEffects resumes backups and switches back to the runtime postgres profile
// once this Operation reaches Succeeded or Failed — but only when no other still-active
// Operation on the same parent matches the pause policy. Without that sibling check, a
// Conflict-failed peer would resume backups / revert the postgres profile while a Running
// Bootstrap is still importing.
func (r *NominatimOperationReconciler) applyTerminalCNPGEffects(ctx context.Context, op *nominatimv1alpha1.NominatimOperation, parent *nominatimv1alpha1.Nominatim) error {
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
	if err := r.setParentBackupPaused(ctx, parent, false); err != nil {
		return err
	}
	return r.applyParentPostgresProfile(ctx, parent, "runtime")
}

// hasOtherActiveMatchingImpactOps reports whether another Pending/Running Operation against
// the same parent still matches impact (and therefore should keep backups paused / import
// profile applied). excludeName is this Operation (the one that just went terminal or is
// being deleted).
func (r *NominatimOperationReconciler) hasOtherActiveMatchingImpactOps(
	ctx context.Context,
	parent *nominatimv1alpha1.Nominatim,
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
		if peer.Spec.NominatimRef.Name != parent.Name {
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
