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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

func TestParseSequenceReport(t *testing.T) {
	t.Parallel()
	got, err := parseSequenceReport(`{"europe/monaco":"4862@2026-07-28T20:21:00Z","europe/andorra":"100"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got["europe/monaco"] != "4862@2026-07-28T20:21:00Z" {
		t.Fatalf("monaco=%q", got["europe/monaco"])
	}
	if got["europe/andorra"] != "100" {
		t.Fatalf("andorra=%q", got["europe/andorra"])
	}
}

func TestParseSequenceReport_Invalid(t *testing.T) {
	t.Parallel()
	if _, err := parseSequenceReport(`not-json`); err == nil {
		t.Fatal("expected error")
	}
}

func TestApplySequenceReports_MergesAndPrefersNewerCompletion(t *testing.T) {
	t.Parallel()
	early := metav1.NewTime(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	late := metav1.NewTime(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC))

	nom := &nominatimv1alpha1.Nominatim{
		Status: nominatimv1alpha1.NominatimStatus{
			Regions: []nominatimv1alpha1.RegionStatus{
				{Name: "europe/monaco", Phase: regionPhaseImported},
				{Name: "europe/andorra", Phase: regionPhaseImported},
			},
		},
	}
	peers := []nominatimv1alpha1.NominatimOperation{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "old-update",
				Annotations: map[string]string{
					annotationSequenceReport: `{"europe/monaco":"1@old","europe/andorra":"9@old"}`,
				},
			},
			Status: nominatimv1alpha1.NominatimOperationStatus{
				Phase:          nominatimv1alpha1.NominatimOperationPhaseSucceeded,
				CompletionTime: &early,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "new-update",
				Annotations: map[string]string{
					annotationSequenceReport: `{"europe/monaco":"2@new"}`,
				},
			},
			Status: nominatimv1alpha1.NominatimOperationStatus{
				Phase:          nominatimv1alpha1.NominatimOperationPhaseSucceeded,
				CompletionTime: &late,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "running",
				Annotations: map[string]string{
					annotationSequenceReport: `{"europe/monaco":"999"}`,
				},
			},
			Status: nominatimv1alpha1.NominatimOperationStatus{
				Phase: nominatimv1alpha1.NominatimOperationPhaseRunning,
			},
		},
	}

	applySequenceReports(nom, peers)

	if got := nom.Status.Regions[0].SequenceState; got != "2@new" {
		t.Fatalf("monaco SequenceState=%q want 2@new", got)
	}
	if got := nom.Status.Regions[1].SequenceState; got != "9@old" {
		t.Fatalf("andorra SequenceState=%q want 9@old", got)
	}
	if nom.Status.Regions[0].LastUpdatedTime == nil || !nom.Status.Regions[0].LastUpdatedTime.Equal(&late) {
		t.Fatalf("monaco LastUpdatedTime=%v want %v", nom.Status.Regions[0].LastUpdatedTime, late)
	}
}

func TestApplySequenceReports_IgnoresUnknownRegionsAndEmptyStatus(t *testing.T) {
	t.Parallel()
	nom := &nominatimv1alpha1.Nominatim{
		Status: nominatimv1alpha1.NominatimStatus{
			Regions: []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}},
		},
	}
	applySequenceReports(nom, []nominatimv1alpha1.NominatimOperation{{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				annotationSequenceReport: `{"europe/france":"1","europe/monaco":"42"}`,
			},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}})
	if nom.Status.Regions[0].SequenceState != "42" {
		t.Fatalf("got %q", nom.Status.Regions[0].SequenceState)
	}
	if len(nom.Status.Regions) != 1 {
		t.Fatalf("regions len=%d", len(nom.Status.Regions))
	}

	empty := &nominatimv1alpha1.Nominatim{}
	applySequenceReports(empty, []nominatimv1alpha1.NominatimOperation{{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{annotationSequenceReport: `{"a":"1"}`}},
		Status:     nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}})
	if len(empty.Status.Regions) != 0 {
		t.Fatal("expected no regions created")
	}
}
