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
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

const (
	addRegionsNameSuffix = "-addregions"
	rebuildNameSuffix    = "-rebuild"
)

// reconcileRegionDrift creates AddRegions or Rebuild NominatimOperations when
// spec.regions diverges from status.regions. Removals never delete DB data — they
// only surface ConditionRegionRemovalUnsupported (set in syncStatus).
// ops is the Reconcile-scoped parent Operation list (one list per pass).
func (r *NominatimInstanceReconciler) reconcileRegionDrift(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance, ops []nominatimv1alpha1.NominatimOperation) error {
	// Bootstrap owns the empty→first-import path. Observation of Succeeded ops
	// into status.regions happens in observeRegionsFromSucceededOps (Reconcile).
	if len(nom.Status.Regions) == 0 {
		return nil
	}

	desired := append([]string(nil), nom.Spec.Regions...)
	observed := make([]string, 0, len(nom.Status.Regions))
	for _, rs := range nom.Status.Regions {
		observed = append(observed, rs.Name)
	}
	missing := difference(desired, observed)
	if len(missing) == 0 {
		return nil
	}

	policy := nom.Spec.RegionChangePolicy
	if policy == "" {
		policy = nominatimv1alpha1.RegionChangeAddData
	}

	switch policy {
	case nominatimv1alpha1.RegionChangeRebuild:
		return r.ensureRebuildOperation(ctx, nom, ops, desired)
	default:
		return r.ensureAddRegionsOperation(ctx, nom, ops, missing)
	}
}

func (r *NominatimInstanceReconciler) ensureAddRegionsOperation(
	ctx context.Context,
	nom *nominatimv1alpha1.NominatimInstance,
	peers []nominatimv1alpha1.NominatimOperation,
	missing []string,
) error {
	log := logf.FromContext(ctx)

	// Serial AddRegions: never create a second while one is active or while any
	// write-heavy peer is Pending/Running (mutex will Conflict anyway — skip create).
	for i := range peers {
		peer := &peers[i]
		if peer.Spec.Type == nominatimv1alpha1.NominatimOperationAddRegions && isActiveOperationPhase(peer.Status.Phase) {
			return nil
		}
		if isWriteHeavyOperation(peer.Spec.Type) && isActiveOperationPhase(peer.Status.Phase) {
			log.Info("skipping AddRegions create; write-heavy operation active", "peer", peer.Name)
			return nil
		}
	}

	if nom.Status.Database.ConnectionSecretName == "" {
		return fmt.Errorf("cannot create AddRegions operation: status.database.connectionSecretName is empty")
	}

	name := nom.Name + addRegionsNameSuffix
	for i := range peers {
		if peers[i].Name == name && !isTerminalOperationPhase(peers[i].Status.Phase) {
			return nil
		}
		// Terminal AddRegions with the same deterministic name: allow a new create
		// only after renaming isn't possible — use generation-suffixed names for retries.
	}
	// Prefer a stable name; if a terminal op already used it, append observed-generation.
	opName := name
	for i := range peers {
		if peers[i].Name == opName {
			opName = fmt.Sprintf("%s-%d", name, nom.Generation)
			break
		}
	}

	// Create one AddRegions operation per missing region, serially: only the
	// first missing region is included here. The next missing region is picked
	// up on a later reconcile once this operation Succeeds and observeRegionsFromSucceededOps
	// merges it into status.regions (see the serial-AddRegions guard above).
	next := missing[0]

	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: nom.Namespace},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationAddRegions,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			Regions:              []string{next},
		},
	}
	if err := controllerutil.SetControllerReference(nom, op, r.Scheme); err != nil {
		return fmt.Errorf("set controller reference on AddRegions operation: %w", err)
	}
	if err := r.Create(ctx, op); err != nil {
		return fmt.Errorf("create AddRegions operation: %w", err)
	}
	log.Info("created AddRegions operation", "operation", op.Name, "region", next)
	return nil
}

func (r *NominatimInstanceReconciler) ensureRebuildOperation(
	ctx context.Context,
	nom *nominatimv1alpha1.NominatimInstance,
	peers []nominatimv1alpha1.NominatimOperation,
	desired []string,
) error {
	log := logf.FromContext(ctx)

	for i := range peers {
		peer := &peers[i]
		if peer.Spec.Type == nominatimv1alpha1.NominatimOperationRebuild && isActiveOperationPhase(peer.Status.Phase) {
			return nil
		}
		if isWriteHeavyOperation(peer.Spec.Type) && isActiveOperationPhase(peer.Status.Phase) {
			log.Info("skipping Rebuild create; write-heavy operation active", "peer", peer.Name)
			return nil
		}
	}

	if nom.Status.Database.ConnectionSecretName == "" {
		return fmt.Errorf("cannot create Rebuild operation: status.database.connectionSecretName is empty")
	}

	name := nom.Name + rebuildNameSuffix
	opName := name
	for i := range peers {
		if peers[i].Name == opName {
			opName = fmt.Sprintf("%s-%d", name, nom.Generation)
			break
		}
	}

	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: nom.Namespace},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationRebuild,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			Regions:              append([]string(nil), desired...),
		},
	}
	if err := controllerutil.SetControllerReference(nom, op, r.Scheme); err != nil {
		return fmt.Errorf("set controller reference on Rebuild operation: %w", err)
	}
	if err := r.Create(ctx, op); err != nil {
		return fmt.Errorf("create Rebuild operation: %w", err)
	}
	log.Info("created Rebuild operation", "operation", op.Name, "regions", strings.Join(desired, ","))
	return nil
}
