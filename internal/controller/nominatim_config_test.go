/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

    10|Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

const (
	testImportStyleAddress   = "address"
	testImportStyleExtratags = "extratags"
	testImportStyleFull      = "full"
)

func TestEffectiveNominatimConfigEnv_BeforeBootstrapUsesSpec(t *testing.T) {
	maxDiff := int32(50)
	nom := &nominatimv1alpha1.Nominatim{
		Spec: nominatimv1alpha1.NominatimSpec{
			Nominatim: &nominatimv1alpha1.NominatimConfigSpec{
				ImportStyle: testImportStyleAddress,
				Tokenizer:   "icu",
				Languages:   []string{"en", "de"},
				Replication: &nominatimv1alpha1.NominatimReplicationSpec{
					URL:     "https://download.geofabrik.de/europe/monaco-updates",
					MaxDiff: &maxDiff,
				},
			},
		},
	}
	env := envMap(effectiveNominatimConfigEnv(nom))
	if env["NOMINATIM_IMPORT_STYLE"] != testImportStyleAddress {
		t.Fatalf("IMPORT_STYLE=%q", env["NOMINATIM_IMPORT_STYLE"])
	}
	if env["NOMINATIM_TOKENIZER"] != "icu" {
		t.Fatalf("TOKENIZER=%q", env["NOMINATIM_TOKENIZER"])
	}
	if env["NOMINATIM_LANGUAGES"] != "en,de" {
		t.Fatalf("LANGUAGES=%q", env["NOMINATIM_LANGUAGES"])
	}
	if env["NOMINATIM_REPLICATION_URL"] == "" || env["NOMINATIM_REPLICATION_MAX_DIFF"] != "50" {
		t.Fatalf("replication env=%v", env)
	}
}

func TestEffectiveNominatimConfigEnv_AfterBootstrapUsesSealedImportTime(t *testing.T) {
	nom := &nominatimv1alpha1.Nominatim{
		Spec: nominatimv1alpha1.NominatimSpec{
			Nominatim: &nominatimv1alpha1.NominatimConfigSpec{
				ImportStyle: testImportStyleFull, // drifted
				Tokenizer:   "icu",
				Languages:   []string{"fr"},
			},
		},
		Status: nominatimv1alpha1.NominatimStatus{
			Regions: []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}},
			ObservedNominatim: &nominatimv1alpha1.ObservedNominatimConfig{
				ImportStyle: testImportStyleExtratags,
				Tokenizer:   "icu",
			},
		},
	}
	env := envMap(effectiveNominatimConfigEnv(nom))
	if env["NOMINATIM_IMPORT_STYLE"] != testImportStyleExtratags {
		t.Fatalf("sealed IMPORT_STYLE=%q want extratags", env["NOMINATIM_IMPORT_STYLE"])
	}
	if env["NOMINATIM_LANGUAGES"] != "fr" {
		t.Fatalf("runtime LANGUAGES should follow spec, got %q", env["NOMINATIM_LANGUAGES"])
	}
}

func TestSealAndDriftImportConfig(t *testing.T) {
	nom := &nominatimv1alpha1.Nominatim{
		ObjectMeta: metav1.ObjectMeta{Generation: 3},
		Spec: nominatimv1alpha1.NominatimSpec{
			Nominatim: &nominatimv1alpha1.NominatimConfigSpec{
				ImportStyle: testImportStyleAddress,
				Tokenizer:   "icu",
			},
		},
	}
	sealObservedNominatimConfig(nom)
	if nom.Status.ObservedNominatim == nil || nom.Status.ObservedNominatim.ImportStyle != testImportStyleAddress {
		t.Fatalf("observed=%+v", nom.Status.ObservedNominatim)
	}
	// Second seal is a no-op even if spec changes.
	nom.Spec.Nominatim.ImportStyle = testImportStyleFull
	sealObservedNominatimConfig(nom)
	if nom.Status.ObservedNominatim.ImportStyle != testImportStyleAddress {
		t.Fatal("seal should not overwrite")
	}

	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}}
	syncImportConfigDriftCondition(nom)
	found := false
	for _, c := range nom.Status.Conditions {
		if c.Type == nominatimv1alpha1.ConditionImportConfigDrift && c.Status == metav1.ConditionTrue {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected ImportConfigDrift condition, got %+v", nom.Status.Conditions)
	}

	nom.Spec.Nominatim.ImportStyle = testImportStyleAddress
	syncImportConfigDriftCondition(nom)
	for _, c := range nom.Status.Conditions {
		if c.Type == nominatimv1alpha1.ConditionImportConfigDrift {
			t.Fatalf("drift condition should clear when aligned, got %+v", c)
		}
	}
}

func TestMergePodSpecOverlay_AllowsNominatimImportStyleFromOverlay(t *testing.T) {
	base := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "api",
			Image: "sealed:v1",
			Env:   []corev1.EnvVar{{Name: "NOMINATIM_DATABASE_DSN", Value: "postgres://op"}},
		}},
	}
	overlay := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name: "api",
			Env: []corev1.EnvVar{
				{Name: "NOMINATIM_IMPORT_STYLE", Value: testImportStyleAddress},
				{Name: "NOMINATIM_DATABASE_DSN", Value: "postgres://evil"},
			},
		}},
	}
	got, err := mergePodSpecOverlay(base, mustPodSpecRaw(t, overlay), "api", "sealed:v1", corev1.PullIfNotPresent)
	if err != nil {
		t.Fatal(err)
	}
	env := envMap(got.Containers[0].Env)
	if env["NOMINATIM_IMPORT_STYLE"] != testImportStyleAddress {
		t.Fatalf("IMPORT_STYLE should pass through overlay, got %q", env["NOMINATIM_IMPORT_STYLE"])
	}
	if env["NOMINATIM_DATABASE_DSN"] != "postgres://op" {
		t.Fatalf("DSN must stay sealed, got %q", env["NOMINATIM_DATABASE_DSN"])
	}
}
