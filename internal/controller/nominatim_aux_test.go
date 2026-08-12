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
)

func ptrBool(v bool) *bool { return &v }

func TestEffectiveAuxDataEnv(t *testing.T) {
	t.Parallel()

	trueVal := true
	nom := &nominatimv1alpha1.Nominatim{
		Spec: nominatimv1alpha1.NominatimSpec{
			AuxData: &nominatimv1alpha1.AuxDataSpec{
				WikimediaImportance: &trueVal,
				USPostcodes:         ptrBool(false),
			},
		},
	}
	env := envMap(effectiveAuxDataEnv(nom))
	if env[envAuxWikimediaImportance] != "true" {
		t.Fatalf("wikimedia=%q want true", env[envAuxWikimediaImportance])
	}
	if env[envAuxSecondaryImportance] != "false" {
		t.Fatalf("secondary=%q want false", env[envAuxSecondaryImportance])
	}
	if env[envAuxUSPostcodes] != "false" {
		t.Fatalf("usPostcodes=%q want false", env[envAuxUSPostcodes])
	}

	if got := effectiveAuxDataEnv(&nominatimv1alpha1.Nominatim{}); got != nil {
		t.Fatalf("nil auxData should yield nil env, got %v", got)
	}
}

func TestParseAuxDataReport(t *testing.T) {
	t.Parallel()

	report, err := parseAuxDataReport(`{"wikimediaImportance":true,"usPostcodes":true}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !report.WikimediaImportance || !report.USPostcodes || report.SecondaryImportance {
		t.Fatalf("unexpected report: %+v", report)
	}

	if report, err := parseAuxDataReport(""); err != nil || report != nil {
		t.Fatalf("empty report: report=%v err=%v", report, err)
	}
	if report, err := parseAuxDataReport("   "); err != nil || report != nil {
		t.Fatalf("whitespace report: report=%v err=%v", report, err)
	}

	if _, err := parseAuxDataReport("{not-json"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestApplyAuxDataReport(t *testing.T) {
	t.Parallel()

	nom := &nominatimv1alpha1.Nominatim{}
	applyAuxDataReport(nom, &nominatimv1alpha1.AuxDataStatus{
		WikimediaImportance: true,
		SecondaryImportance: true,
	})
	if nom.Status.AuxData == nil || !nom.Status.AuxData.WikimediaImportance || !nom.Status.AuxData.SecondaryImportance {
		t.Fatalf("status auxData not applied: %+v", nom.Status.AuxData)
	}
}
