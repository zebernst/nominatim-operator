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

func TestWritePlaneTerminalConflict_Error(t *testing.T) {
	t.Parallel()
	err := &writePlaneTerminalConflict{
		peer: &nominatimv1alpha1.NominatimOperation{ObjectMeta: metav1.ObjectMeta{Name: "held-op"}},
	}
	if got := err.Error(); got != `write plane held by "held-op"` {
		t.Fatalf("Error()=%q", got)
	}
}

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

func TestEvaluateWritePlane(t *testing.T) {
	update := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "update-1", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
		},
	}
	bootstrapRunning := nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-1", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseRunning},
	}
	racingWinner := nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-aaa", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: ""},
	}
	racingLoser := racingWinner
	racingLoser.Name = "bootstrap-bbb"
	done := nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "done", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	otherNom := nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "other-nom"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseRunning},
	}
	updatePeer := nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "update-2", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhasePending},
	}

	t.Run("Hold when peer holds write plane", func(t *testing.T) {
		ev := evaluateWritePlane(update, []nominatimv1alpha1.NominatimOperation{bootstrapRunning})
		if ev.Decision != writePlaneHold || ev.Peer == nil || ev.Peer.Name != "bootstrap-1" {
			t.Fatalf("want Hold on bootstrap-1, got %#v", ev)
		}
		if !ev.ScheduleBusy() {
			t.Fatal("Hold must be schedule-busy")
		}
	})

	t.Run("RaceWait when lex-larger against creation-race peer", func(t *testing.T) {
		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-bbb", Namespace: "ns"},
			Spec:       racingLoser.Spec,
		}
		ev := evaluateWritePlane(op, []nominatimv1alpha1.NominatimOperation{racingWinner})
		if ev.Decision != writePlaneRaceWait {
			t.Fatalf("want RaceWait, got %#v", ev)
		}
		if !ev.ScheduleBusy() {
			t.Fatal("RaceWait must be schedule-busy")
		}
	})

	t.Run("Ok for race winner but ScheduleBusy while peer still racing", func(t *testing.T) {
		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-aaa", Namespace: "ns"},
			Spec:       racingWinner.Spec,
		}
		ev := evaluateWritePlane(op, []nominatimv1alpha1.NominatimOperation{racingLoser})
		if ev.Decision != writePlaneOK {
			t.Fatalf("race winner claim must be Ok, got %#v", ev)
		}
		if !ev.ScheduleBusy() {
			t.Fatal("schedule must stay busy while any conflicting peer is still active/racing")
		}
		if ev.BusyPeer == nil || ev.BusyPeer.Name != "bootstrap-bbb" {
			t.Fatalf("want BusyPeer bootstrap-bbb, got %#v", ev.BusyPeer)
		}
	})

	t.Run("Ok and not schedule-busy for two Updates", func(t *testing.T) {
		ev := evaluateWritePlane(update, []nominatimv1alpha1.NominatimOperation{updatePeer, done, otherNom})
		if ev.Decision != writePlaneOK || ev.ScheduleBusy() {
			t.Fatalf("two Updates must be Ok and not schedule-busy, got %#v", ev)
		}
	})

	t.Run("schedule probe busy against racing write-heavy even if probe would win lex race", func(t *testing.T) {
		probe := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: "__schedule-probe__", Namespace: "ns"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationUpdate,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
			},
		}
		ev := evaluateWritePlane(probe, []nominatimv1alpha1.NominatimOperation{racingWinner})
		// Probe name sorts before bootstrap-aaa, so claim decision is Ok — but schedule must skip.
		if ev.Decision != writePlaneOK {
			t.Fatalf("probe claim decision should be Ok (lex winner), got %#v", ev)
		}
		if !ev.ScheduleBusy() || ev.BusyPeer == nil || ev.BusyPeer.Name != "bootstrap-aaa" {
			t.Fatalf("schedule probe must be busy against racing write-heavy, got %#v", ev)
		}
	})
}
