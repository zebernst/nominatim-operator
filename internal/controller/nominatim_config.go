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
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// effectiveNominatimConfigEnv returns NOMINATIM_* env vars from spec.nominatim.
// Import-time keys (IMPORT_STYLE, TOKENIZER) use sealed status.observedNominatim once
// Bootstrap has populated status.regions; runtime keys always follow spec.
func effectiveNominatimConfigEnv(nom *nominatimv1alpha1.Nominatim) []corev1.EnvVar {
	if nom == nil {
		return nil
	}
	cfg := nom.Spec.Nominatim
	importStyle, tokenizer := effectiveImportTimeSettings(nom)

	var env []corev1.EnvVar
	if importStyle != "" {
		env = append(env, corev1.EnvVar{Name: "NOMINATIM_IMPORT_STYLE", Value: importStyle})
	}
	if tokenizer != "" {
		env = append(env, corev1.EnvVar{Name: "NOMINATIM_TOKENIZER", Value: tokenizer})
	}
	if cfg == nil {
		return env
	}
	if len(cfg.Languages) > 0 {
		env = append(env, corev1.EnvVar{
			Name:  "NOMINATIM_LANGUAGES",
			Value: strings.Join(cfg.Languages, ","),
		})
	}
	if cfg.Replication != nil {
		r := cfg.Replication
		if r.URL != "" {
			env = append(env, corev1.EnvVar{Name: "NOMINATIM_REPLICATION_URL", Value: r.URL})
		}
		if r.MaxDiff != nil {
			env = append(env, corev1.EnvVar{
				Name:  "NOMINATIM_REPLICATION_MAX_DIFF",
				Value: fmt.Sprintf("%d", *r.MaxDiff),
			})
		}
		if r.UpdateIntervalSeconds != nil {
			env = append(env, corev1.EnvVar{
				Name:  "NOMINATIM_REPLICATION_UPDATE_INTERVAL",
				Value: fmt.Sprintf("%d", *r.UpdateIntervalSeconds),
			})
		}
		if r.RecheckIntervalSeconds != nil {
			env = append(env, corev1.EnvVar{
				Name:  "NOMINATIM_REPLICATION_RECHECK_INTERVAL",
				Value: fmt.Sprintf("%d", *r.RecheckIntervalSeconds),
			})
		}
	}
	return env
}

func effectiveImportTimeSettings(nom *nominatimv1alpha1.Nominatim) (importStyle, tokenizer string) {
	if len(nom.Status.Regions) > 0 && nom.Status.ObservedNominatim != nil {
		return nom.Status.ObservedNominatim.ImportStyle, nom.Status.ObservedNominatim.Tokenizer
	}
	if nom.Spec.Nominatim == nil {
		return "", ""
	}
	return nom.Spec.Nominatim.ImportStyle, nom.Spec.Nominatim.Tokenizer
}

// sealObservedNominatimConfig copies current import-time spec into status when Bootstrap
// first succeeds (status.regions becoming non-empty). No-op if already sealed unless force.
func sealObservedNominatimConfig(nom *nominatimv1alpha1.Nominatim) {
	sealObservedNominatimConfigForce(nom, false)
}

func sealObservedNominatimConfigForce(nom *nominatimv1alpha1.Nominatim, force bool) {
	if !force && nom.Status.ObservedNominatim != nil {
		return
	}
	obs := &nominatimv1alpha1.ObservedNominatimConfig{}
	if nom.Spec.Nominatim != nil {
		obs.ImportStyle = nom.Spec.Nominatim.ImportStyle
		obs.Tokenizer = nom.Spec.Nominatim.Tokenizer
	}
	nom.Status.ObservedNominatim = obs
}

// syncImportConfigDriftCondition sets ImportConfigDrift when sealed import-time settings
// differ from spec after Bootstrap. Clears the condition when aligned or not yet sealed.
func syncImportConfigDriftCondition(nom *nominatimv1alpha1.Nominatim) {
	if len(nom.Status.Regions) == 0 || nom.Status.ObservedNominatim == nil {
		meta.RemoveStatusCondition(&nom.Status.Conditions, nominatimv1alpha1.ConditionImportConfigDrift)
		return
	}
	specStyle, specTok := "", ""
	if nom.Spec.Nominatim != nil {
		specStyle = nom.Spec.Nominatim.ImportStyle
		specTok = nom.Spec.Nominatim.Tokenizer
	}
	obs := nom.Status.ObservedNominatim
	if specStyle == obs.ImportStyle && specTok == obs.Tokenizer {
		meta.RemoveStatusCondition(&nom.Status.Conditions, nominatimv1alpha1.ConditionImportConfigDrift)
		return
	}
	msg := fmt.Sprintf(
		"import-time nominatim settings differ from Bootstrap seal (spec importStyle=%q tokenizer=%q; sealed importStyle=%q tokenizer=%q); Reimport required to apply",
		specStyle, specTok, obs.ImportStyle, obs.Tokenizer,
	)
	meta.SetStatusCondition(&nom.Status.Conditions, metav1.Condition{
		Type:               nominatimv1alpha1.ConditionImportConfigDrift,
		Status:             metav1.ConditionTrue,
		Reason:             "ImportConfigDrift",
		Message:            msg,
		ObservedGeneration: nom.Generation,
	})
}
