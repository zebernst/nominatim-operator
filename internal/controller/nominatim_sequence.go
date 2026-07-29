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
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// annotationSequenceReport is written by the worker (best-effort) onto the
// NominatimOperation after Bootstrap/AddRegions/Update/CatchUp so the Nominatim
// reconciler can populate status.regions[].sequenceState. Controllers cannot read
// project PVC sequence.state files directly.
const annotationSequenceReport = "nominatim.zebernst.dev/sequence-report"

// applySequenceReports merges worker-reported Geofabrik sequence identities from
// Succeeded Operations into nom.Status.Regions. Later CompletionTime wins for a
// given region. Unknown region names in the report are ignored (status.regions is
// the observed import set).
func applySequenceReports(nom *nominatimv1alpha1.Nominatim, peers []nominatimv1alpha1.NominatimOperation) {
	if len(nom.Status.Regions) == 0 || len(peers) == 0 {
		return
	}

	ops := make([]nominatimv1alpha1.NominatimOperation, 0, len(peers))
	for i := range peers {
		op := peers[i]
		if op.Status.Phase != nominatimv1alpha1.NominatimOperationPhaseSucceeded {
			continue
		}
		if op.Annotations[annotationSequenceReport] == "" {
			continue
		}
		ops = append(ops, op)
	}
	if len(ops) == 0 {
		return
	}

	sort.SliceStable(ops, func(i, j int) bool {
		return completionBefore(ops[i].Status.CompletionTime, ops[j].Status.CompletionTime)
	})

	byName := make(map[string]int, len(nom.Status.Regions))
	for i, rs := range nom.Status.Regions {
		byName[rs.Name] = i
	}

	now := metav1.Now()
	for _, op := range ops {
		report, err := parseSequenceReport(op.Annotations[annotationSequenceReport])
		if err != nil || len(report) == 0 {
			continue
		}
		for region, state := range report {
			if state == "" {
				continue
			}
			idx, ok := byName[region]
			if !ok {
				continue
			}
			rs := &nom.Status.Regions[idx]
			if rs.SequenceState == state {
				continue
			}
			rs.SequenceState = state
			t := now
			if op.Status.CompletionTime != nil {
				t = *op.Status.CompletionTime
			}
			rs.LastUpdatedTime = &t
		}
	}
}

func completionBefore(a, b *metav1.Time) bool {
	switch {
	case a == nil && b == nil:
		return false
	case a == nil:
		return true
	case b == nil:
		return false
	default:
		return a.Before(b)
	}
}

// parseSequenceReport decodes the worker annotation JSON map of region path →
// sequence identity (typically "sequenceNumber@timestamp").
func parseSequenceReport(raw string) (map[string]string, error) {
	out := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}
