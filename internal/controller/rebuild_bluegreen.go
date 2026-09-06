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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

const defaultBlueGreenRollbackWindow = time.Hour

// usesBlueGreenRebuild reports whether Rebuild Operations for this instance should use
// the BlueGreen path (parallel cluster) instead of InPlace database reset.
func usesBlueGreenRebuild(nom *nominatimv1alpha1.NominatimInstance) bool {
	if nom == nil {
		return false
	}
	return nom.Spec.Database.EffectiveRebuildStrategy() == nominatimv1alpha1.RebuildStrategyBlueGreen
}

// blueGreenRebuildAllowed is true only for ClusterManaged / owned-cluster mode.
func blueGreenRebuildAllowed(nom *nominatimv1alpha1.NominatimInstance) bool {
	if nom == nil {
		return false
	}
	return nom.Spec.Database.Cluster != nil ||
		nom.Status.Database.Mode == nominatimv1alpha1.DatabaseModeClusterManaged
}

// BlueGreenClusterName is the green CNPG Cluster name for a BlueGreen Rebuild.
func BlueGreenClusterName(nom *nominatimv1alpha1.NominatimInstance) string {
	return OwnedCNPGClusterName(nom) + "-bg"
}

// BlueGreenProjectPVCName is the green project PVC for a BlueGreen Rebuild.
func BlueGreenProjectPVCName(nom *nominatimv1alpha1.NominatimInstance) string {
	return ProjectPVCName(nom) + "-bg"
}

// BlueGreenFlatnodePVCName is the green flatnode PVC for a BlueGreen Rebuild.
func BlueGreenFlatnodePVCName(nom *nominatimv1alpha1.NominatimInstance) string {
	return FlatnodePVCName(nom) + "-bg"
}

// blueGreenInput is the pure observation the phase machine advances from.
type blueGreenInput struct {
	Phase           nominatimv1alpha1.RebuildPhase
	ActiveCluster   string
	PendingCluster  string
	PendingReady    bool
	ImportSucceeded bool
	APICutoverReady bool
	BlueGone        bool
	Failed          bool
	Message         string
	RollbackUntil   *metav1.Time
	Now             time.Time
	RollbackWindow  time.Duration
}

// blueGreenActions are side effects the reconciler should attempt for the next phase.
type blueGreenActions struct {
	EnsurePendingCluster bool
	EnsureImportJob      bool
	CutoverAPI           bool
	GarbageCollectBlue   bool
	GarbageCollectGreen  bool
}

// blueGreenOutput is the next status snapshot plus requested actions.
type blueGreenOutput struct {
	Phase         nominatimv1alpha1.RebuildPhase
	RollbackUntil *metav1.Time
	Message       string
}

func advanceBlueGreen(in blueGreenInput) (blueGreenOutput, blueGreenActions) {
	window := in.RollbackWindow
	if window <= 0 {
		window = defaultBlueGreenRollbackWindow
	}
	out := blueGreenOutput{
		Phase:         in.Phase,
		RollbackUntil: in.RollbackUntil,
		Message:       in.Message,
	}
	var act blueGreenActions

	if in.Failed && in.Phase != nominatimv1alpha1.RebuildPhaseCutover &&
		in.Phase != nominatimv1alpha1.RebuildPhaseRollbackWindow &&
		in.Phase != nominatimv1alpha1.RebuildPhaseGarbageCollect &&
		in.Phase != nominatimv1alpha1.RebuildPhaseSucceeded {
		out.Phase = nominatimv1alpha1.RebuildPhaseFailed
		act.GarbageCollectGreen = true
		return out, act
	}

	switch in.Phase {
	case "", nominatimv1alpha1.RebuildPhaseProvisioning:
		out.Phase = nominatimv1alpha1.RebuildPhaseProvisioning
		act.EnsurePendingCluster = true
		if in.PendingReady {
			out.Phase = nominatimv1alpha1.RebuildPhaseImporting
			act.EnsurePendingCluster = false
			act.EnsureImportJob = true
		}

	case nominatimv1alpha1.RebuildPhaseImporting:
		act.EnsureImportJob = true
		if in.ImportSucceeded {
			out.Phase = nominatimv1alpha1.RebuildPhaseReadyToCutover
			act.EnsureImportJob = false
		}

	case nominatimv1alpha1.RebuildPhaseReadyToCutover:
		out.Phase = nominatimv1alpha1.RebuildPhaseCutover
		act.CutoverAPI = true

	case nominatimv1alpha1.RebuildPhaseCutover:
		act.CutoverAPI = true
		if in.APICutoverReady {
			out.Phase = nominatimv1alpha1.RebuildPhaseRollbackWindow
			act.CutoverAPI = false
			if out.RollbackUntil == nil {
				until := metav1.NewTime(in.Now.Add(window))
				out.RollbackUntil = &until
			}
		}

	case nominatimv1alpha1.RebuildPhaseRollbackWindow:
		if in.RollbackUntil != nil && !in.Now.Before(in.RollbackUntil.Time) {
			out.Phase = nominatimv1alpha1.RebuildPhaseGarbageCollect
			act.GarbageCollectBlue = true
		}

	case nominatimv1alpha1.RebuildPhaseGarbageCollect:
		act.GarbageCollectBlue = true
		if in.BlueGone {
			out.Phase = nominatimv1alpha1.RebuildPhaseSucceeded
			act.GarbageCollectBlue = false
		}

	case nominatimv1alpha1.RebuildPhaseSucceeded, nominatimv1alpha1.RebuildPhaseFailed:
		// Terminal — reconciler clears status.rebuild when appropriate.
	}

	return out, act
}
