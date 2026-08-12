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

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPeerHoldsWritePlane(t *testing.T) {
	running := &nominatimv1alpha1.NominatimOperation{
		Status: nominatimv1alpha1.NominatimOperationStatus{
			Phase: nominatimv1alpha1.NominatimOperationPhaseRunning,
		},
	}
	if !peerHoldsWritePlane(running) {
		t.Fatal("Running must hold write plane")
	}

	withJob := &nominatimv1alpha1.NominatimOperation{
		Status: nominatimv1alpha1.NominatimOperationStatus{
			Phase:  nominatimv1alpha1.NominatimOperationPhasePending,
			JobRef: &nominatimv1alpha1.LocalObjectReference{Name: "job-1"},
		},
	}
	if !peerHoldsWritePlane(withJob) {
		t.Fatal("Pending with JobRef must hold write plane")
	}

	racing := &nominatimv1alpha1.NominatimOperation{
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: ""},
	}
	if peerHoldsWritePlane(racing) {
		t.Fatal("empty phase without JobRef must not hold write plane (creation race)")
	}
}

func TestShouldRequeueWritePlaneRace(t *testing.T) {
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-bbb"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
		},
	}
	peerWinner := nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-aaa"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: ""},
	}
	if !shouldRequeueWritePlaneRace(op, []nominatimv1alpha1.NominatimOperation{peerWinner}) {
		t.Fatal("lexicographically larger name must requeue during creation race")
	}

	opWinner := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-aaa"},
		Spec:       op.Spec,
	}
	peerLoser := peerWinner
	peerLoser.Name = "bootstrap-bbb"
	if shouldRequeueWritePlaneRace(opWinner, []nominatimv1alpha1.NominatimOperation{peerLoser}) {
		t.Fatal("race winner must not requeue")
	}
}

func TestFindTerminalWritePlaneBlocker(t *testing.T) {
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "update-1"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
		},
	}
	racing := nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-1"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: ""},
	}
	if findTerminalWritePlaneBlocker(op, []nominatimv1alpha1.NominatimOperation{racing}) != nil {
		t.Fatal("creation-race peer must not be a terminal blocker")
	}

	held := racing
	held.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseRunning
	blocker := findTerminalWritePlaneBlocker(op, []nominatimv1alpha1.NominatimOperation{held})
	if blocker == nil || blocker.Name != "bootstrap-1" {
		t.Fatalf("Running write-heavy peer must terminal-block, got %#v", blocker)
	}
}
