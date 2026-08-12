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
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

const (
	envAuxWikimediaImportance = "NOMINATIM_AUX_WIKIMEDIA_IMPORTANCE"
	envAuxSecondaryImportance = "NOMINATIM_AUX_SECONDARY_IMPORTANCE"
	envAuxUSPostcodes         = "NOMINATIM_AUX_US_POSTCODES"
	sequenceAuxReportCMKey    = "aux-data.json"
)

func effectiveAuxDataEnv(nom *nominatimv1alpha1.Nominatim) []corev1.EnvVar {
	if nom == nil || nom.Spec.AuxData == nil {
		return nil
	}
	ad := nom.Spec.AuxData
	return []corev1.EnvVar{
		{Name: envAuxWikimediaImportance, Value: boolEnv(auxDataEnabled(ad.WikimediaImportance))},
		{Name: envAuxSecondaryImportance, Value: boolEnv(auxDataEnabled(ad.SecondaryImportance))},
		{Name: envAuxUSPostcodes, Value: boolEnv(auxDataEnabled(ad.USPostcodes))},
	}
}

func auxDataEnabled(v *bool) bool {
	return v != nil && *v
}

func boolEnv(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// parseAuxDataReport decodes aux dataset presence JSON from the sequence probe ConfigMap.
func parseAuxDataReport(raw string) (*nominatimv1alpha1.AuxDataStatus, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	out := &nominatimv1alpha1.AuxDataStatus{}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return nil, fmt.Errorf("unmarshal aux data report: %w", err)
	}
	return out, nil
}

// applyAuxDataReport merges observed aux dataset presence into status.
func applyAuxDataReport(nom *nominatimv1alpha1.Nominatim, report *nominatimv1alpha1.AuxDataStatus) {
	if report == nil {
		return
	}
	if nom.Status.AuxData == nil {
		nom.Status.AuxData = &nominatimv1alpha1.AuxDataStatus{}
	}
	nom.Status.AuxData.WikimediaImportance = report.WikimediaImportance
	nom.Status.AuxData.SecondaryImportance = report.SecondaryImportance
	nom.Status.AuxData.USPostcodes = report.USPostcodes
}
