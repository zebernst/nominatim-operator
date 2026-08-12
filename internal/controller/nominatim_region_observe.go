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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// observeRegionsFromSucceededOps is the single status.regions writer from Succeeded
// Operations: Bootstrap fills an empty set, AddRegions merges, Rebuild replaces.
// Bootstrap/drift reconcile only ensure Operation creates.
func observeRegionsFromSucceededOps(nom *nominatimv1alpha1.Nominatim, peers []nominatimv1alpha1.NominatimOperation) {
	syncRegionsFromBootstrap(nom, peers)
	syncRegionsFromDriftOps(nom, peers)
}

// syncRegionsFromBootstrap populates nom.Status.Regions (in-memory; persisted by the
// caller's later Status().Update, e.g. syncStatus) once a Bootstrap Operation targeting
// this parent has Succeeded. It is a no-op when status.regions is already populated.
//
// A Succeeded Bootstrap Job is required to have imported every region listed on the
// Operation via nominatim import with multiple --osm-file flags. Marking the full
// region list here is therefore accurate — not a blind copy of an unfinished import.
func syncRegionsFromBootstrap(nom *nominatimv1alpha1.Nominatim, peers []nominatimv1alpha1.NominatimOperation) {
	if len(nom.Status.Regions) > 0 {
		return
	}

	for i := range peers {
		op := &peers[i]
		if op.Spec.Type != nominatimv1alpha1.NominatimOperationBootstrap {
			continue
		}
		if op.Status.Phase != nominatimv1alpha1.NominatimOperationPhaseSucceeded {
			continue
		}

		regions := op.Spec.Regions
		if len(regions) == 0 {
			regions = nom.Spec.Regions
		}
		if len(regions) == 0 {
			continue
		}

		now := metav1.Now()
		statuses := make([]nominatimv1alpha1.RegionStatus, 0, len(regions))
		for _, region := range regions {
			statuses = append(statuses, nominatimv1alpha1.RegionStatus{
				Name:            region,
				LastUpdatedTime: &now,
			})
		}
		nom.Status.Regions = statuses
		sealObservedNominatimConfig(nom)
		return
	}
}

// syncRegionsFromDriftOps updates status.regions when an AddRegions or Rebuild
// Operation targeting this parent has Succeeded. Removals are never applied here —
// shrinking the observed set requires a Succeeded Rebuild whose Spec.Regions is
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
		case nominatimv1alpha1.NominatimOperationRebuild:
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
			LastUpdatedTime: &now,
		})
	}
	nom.Status.Regions = statuses
	// Rebuild rebuilds the DB — reseal import-time Nominatim settings from current spec.
	sealObservedNominatimConfigForce(nom, true)
}
