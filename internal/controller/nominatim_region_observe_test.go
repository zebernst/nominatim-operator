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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

func TestObserveRegionsFromSucceededOps_BootstrapThenAddThenRebuild(t *testing.T) {
	nom := nominatimWithRegions("observe-all", "europe/monaco")

	boot := nominatimv1alpha1.NominatimOperation{
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:    nominatimv1alpha1.NominatimOperationBootstrap,
			Regions: []string{"europe/monaco"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	observeRegionsFromSucceededOps(nom, []nominatimv1alpha1.NominatimOperation{boot})
	if len(nom.Status.Regions) != 1 || nom.Status.Regions[0].Name != "europe/monaco" {
		t.Fatalf("bootstrap observe: %#v", nom.Status.Regions)
	}

	add := nominatimv1alpha1.NominatimOperation{
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:    nominatimv1alpha1.NominatimOperationAddRegions,
			Regions: []string{"europe/andorra"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	observeRegionsFromSucceededOps(nom, []nominatimv1alpha1.NominatimOperation{boot, add})
	if len(nom.Status.Regions) != 2 {
		t.Fatalf("add merge: %#v", nom.Status.Regions)
	}

	reimp := nominatimv1alpha1.NominatimOperation{
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:    nominatimv1alpha1.NominatimOperationRebuild,
			Regions: []string{"europe/liechtenstein"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	observeRegionsFromSucceededOps(nom, []nominatimv1alpha1.NominatimOperation{boot, add, reimp})
	if len(nom.Status.Regions) != 1 || nom.Status.Regions[0].Name != "europe/liechtenstein" {
		t.Fatalf("rebuild replace: %#v", nom.Status.Regions)
	}
}

func TestObserveRegions_BootstrapFallsBackToParentSpecRegions(t *testing.T) {
	nom := nominatimWithRegions("bootstrap-fallback-regions", "europe/monaco")
	succeeded := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: BootstrapOperationName(nom), Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			// Regions left empty on the Operation; sync should fall back to parent spec.
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	observeRegionsFromSucceededOps(nom, []nominatimv1alpha1.NominatimOperation{*succeeded})
	if len(nom.Status.Regions) != 1 || nom.Status.Regions[0].Name != "europe/monaco" {
		t.Fatalf("status.regions=%v want [europe/monaco]", nom.Status.Regions)
	}
}

func TestObserveRegions_BootstrapSkipsWhenNoRegionsAnywhere(t *testing.T) {
	nom := nominatimWithConnectionSecret("bootstrap-no-regions-anywhere")
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	// nom.Spec.Regions intentionally left empty.
	succeeded := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: BootstrapOperationName(nom), Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			// Regions left empty on both the Operation and the parent spec.
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	observeRegionsFromSucceededOps(nom, []nominatimv1alpha1.NominatimOperation{*succeeded})
	if len(nom.Status.Regions) != 0 {
		t.Fatalf("status.regions=%v want empty when no regions are known anywhere", nom.Status.Regions)
	}
}

func TestObserveRegions_IgnoresNonBootstrapOrNonSucceeded(t *testing.T) {
	nom := nominatimWithRegions("bootstrap-ignore-others", "europe/monaco")
	running := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: BootstrapOperationName(nom), Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			Regions:      []string{"europe/monaco"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseRunning},
	}
	update := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: nom.Name + "-update", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	observeRegionsFromSucceededOps(nom, []nominatimv1alpha1.NominatimOperation{*running, *update})
	if len(nom.Status.Regions) != 0 {
		t.Fatalf("status.regions=%v want empty (no succeeded Bootstrap present)", nom.Status.Regions)
	}
}
