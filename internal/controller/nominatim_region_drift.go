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
	reimportNameSuffix   = "-reimport"
)

// reconcileRegionDrift creates AddRegions or Reimport NominatimOperations when
// spec.regions diverges from status.regions. Removals never delete DB data — they
// only surface ConditionRegionRemovalUnsupported (set in syncStatus).
func (r *NominatimReconciler) reconcileRegionDrift(ctx context.Context, nom *nominatimv1alpha1.Nominatim) error {
	ops, err := r.listOperationsForParent(ctx, nom)
	if err != nil {
		return fmt.Errorf("list operations for region drift: %w", err)
	}

	syncRegionsFromDriftOps(nom, ops)
	applySequenceReports(nom, ops)

	// Bootstrap owns the empty→first-import path.
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
	case nominatimv1alpha1.RegionChangeReimport:
		return r.ensureReimportOperation(ctx, nom, ops, desired)
	default:
		return r.ensureAddRegionsOperation(ctx, nom, ops, missing)
	}
}

func (r *NominatimReconciler) ensureAddRegionsOperation(
	ctx context.Context,
	nom *nominatimv1alpha1.Nominatim,
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
	// up on a later reconcile once this operation Succeeds and syncRegionsFromDriftOps
	// merges it into status.regions (see the serial-AddRegions guard above).
	next := missing[0]

	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: nom.Namespace},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationAddRegions,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			Regions:      []string{next},
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

func (r *NominatimReconciler) ensureReimportOperation(
	ctx context.Context,
	nom *nominatimv1alpha1.Nominatim,
	peers []nominatimv1alpha1.NominatimOperation,
	desired []string,
) error {
	log := logf.FromContext(ctx)

	for i := range peers {
		peer := &peers[i]
		if peer.Spec.Type == nominatimv1alpha1.NominatimOperationReimport && isActiveOperationPhase(peer.Status.Phase) {
			return nil
		}
		if isWriteHeavyOperation(peer.Spec.Type) && isActiveOperationPhase(peer.Status.Phase) {
			log.Info("skipping Reimport create; write-heavy operation active", "peer", peer.Name)
			return nil
		}
	}

	if nom.Status.Database.ConnectionSecretName == "" {
		return fmt.Errorf("cannot create Reimport operation: status.database.connectionSecretName is empty")
	}

	name := nom.Name + reimportNameSuffix
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
			Type:         nominatimv1alpha1.NominatimOperationReimport,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			Regions:      append([]string(nil), desired...),
		},
	}
	if err := controllerutil.SetControllerReference(nom, op, r.Scheme); err != nil {
		return fmt.Errorf("set controller reference on Reimport operation: %w", err)
	}
	if err := r.Create(ctx, op); err != nil {
		return fmt.Errorf("create Reimport operation: %w", err)
	}
	log.Info("created Reimport operation", "operation", op.Name, "regions", strings.Join(desired, ","))
	return nil
}

// syncRegionsFromDriftOps updates status.regions when an AddRegions or Reimport
// Operation targeting this parent has Succeeded. Removals are never applied here —
// shrinking the observed set requires a Succeeded Reimport whose Spec.Regions is
// the new desired set (full rebuild), not a surgical delete.
func syncRegionsFromDriftOps(nom *nominatimv1alpha1.Nominatim, peers []nominatimv1alpha1.NominatimOperation) {
	for i := range peers {
		op := &peers[i]
		if op.Status.Phase != nominatimv1alpha1.NominatimOperationPhaseSucceeded {
			continue
		}
		switch op.Spec.Type {
		case nominatimv1alpha1.NominatimOperationAddRegions:
			mergeRegionsIntoStatus(nom, op.Spec.Regions)
		case nominatimv1alpha1.NominatimOperationReimport:
			replaceRegionsStatus(nom, op.Spec.Regions)
		}
	}
}

func mergeRegionsIntoStatus(nom *nominatimv1alpha1.Nominatim, regions []string) {
	have := make(map[string]struct{}, len(nom.Status.Regions))
	for _, rs := range nom.Status.Regions {
		have[rs.Name] = struct{}{}
	}
	now := metav1.Now()
	for _, region := range regions {
		if _, ok := have[region]; ok {
			continue
		}
		nom.Status.Regions = append(nom.Status.Regions, nominatimv1alpha1.RegionStatus{
			Name:            region,
			Phase:           regionPhaseImported,
			LastUpdatedTime: &now,
		})
		have[region] = struct{}{}
	}
}

func replaceRegionsStatus(nom *nominatimv1alpha1.Nominatim, regions []string) {
	now := metav1.Now()
	statuses := make([]nominatimv1alpha1.RegionStatus, 0, len(regions))
	for _, region := range regions {
		statuses = append(statuses, nominatimv1alpha1.RegionStatus{
			Name:            region,
			Phase:           regionPhaseImported,
			LastUpdatedTime: &now,
		})
	}
	nom.Status.Regions = statuses
	// Reimport rebuilds the DB — reseal import-time Nominatim settings from current spec.
	sealObservedNominatimConfigForce(nom, true)
}
