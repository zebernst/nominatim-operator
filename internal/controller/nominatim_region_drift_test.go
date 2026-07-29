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
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

const testConnSecret = "pg-app"

func TestReconcileRegionDrift_AddDataCreatesAddRegions(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := baseNominatim("drift-add")
	nom.Spec.Regions = []string{"north-america/us-midwest", "asia/kazakhstan"}
	nom.Spec.RegionChangePolicy = nominatimv1alpha1.RegionChangeAddData
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "north-america/us-midwest", Phase: regionPhaseImported}}
	nom.Status.Database.ConnectionSecretName = testConnSecret

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileRegionDrift(ctx, nom); err != nil {
		t.Fatalf("reconcileRegionDrift: %v", err)
	}

	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(ctx, ops); err != nil {
		t.Fatal(err)
	}
	if len(ops.Items) != 1 {
		t.Fatalf("expected 1 AddRegions op, got %d", len(ops.Items))
	}
	op := ops.Items[0]
	if op.Spec.Type != nominatimv1alpha1.NominatimOperationAddRegions {
		t.Fatalf("type=%q", op.Spec.Type)
	}
	if len(op.Spec.Regions) != 1 || op.Spec.Regions[0] != "asia/kazakhstan" {
		t.Fatalf("regions=%v (want only missing)", op.Spec.Regions)
	}
}

func TestReconcileRegionDrift_AddDataMultiMissingCreatesSingleRegionOp(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := baseNominatim("drift-multi")
	nom.Spec.Regions = []string{"a", "b", "c"}
	nom.Spec.RegionChangePolicy = nominatimv1alpha1.RegionChangeAddData
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "a", Phase: regionPhaseImported}}
	nom.Status.Database.ConnectionSecretName = testConnSecret

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileRegionDrift(ctx, nom); err != nil {
		t.Fatalf("reconcileRegionDrift: %v", err)
	}

	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(ctx, ops); err != nil {
		t.Fatal(err)
	}
	if len(ops.Items) != 1 {
		t.Fatalf("expected 1 AddRegions op, got %d", len(ops.Items))
	}
	op := ops.Items[0]
	if op.Spec.Type != nominatimv1alpha1.NominatimOperationAddRegions {
		t.Fatalf("type=%q", op.Spec.Type)
	}
	if len(op.Spec.Regions) != 1 || op.Spec.Regions[0] != "b" {
		t.Fatalf("regions=%v (want only first missing region [b])", op.Spec.Regions)
	}
}

func TestReconcileRegionDrift_AddDataSerialMultiMissingCreatesNextOpAfterSucceed(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := baseNominatim("drift-serial-multi")
	nom.Spec.Regions = []string{"a", "b", "c"}
	nom.Spec.RegionChangePolicy = nominatimv1alpha1.RegionChangeAddData
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "a", Phase: regionPhaseImported}}
	nom.Status.Database.ConnectionSecretName = testConnSecret

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(nom, &nominatimv1alpha1.NominatimOperation{}).
		WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	// First reconcile creates the op for "b" only.
	if err := r.reconcileRegionDrift(ctx, nom); err != nil {
		t.Fatalf("reconcileRegionDrift (1st): %v", err)
	}
	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(ctx, ops); err != nil {
		t.Fatal(err)
	}
	if len(ops.Items) != 1 || len(ops.Items[0].Spec.Regions) != 1 || ops.Items[0].Spec.Regions[0] != "b" {
		t.Fatalf("expected single op for [b], got %#v", ops.Items)
	}

	// Mark the op Succeeded and update status.regions accordingly (simulating what
	// the operation controller + syncRegionsFromDriftOps would do).
	first := &nominatimv1alpha1.NominatimOperation{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(&ops.Items[0]), first); err != nil {
		t.Fatalf("get op: %v", err)
	}
	first.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseSucceeded
	if err := c.Status().Update(ctx, first); err != nil {
		t.Fatalf("update op status: %v", err)
	}

	// Re-reconcile: syncRegionsFromDriftOps should merge "b" into status, and drift
	// should then create a new op for the next missing region, "c".
	if err := r.reconcileRegionDrift(ctx, nom); err != nil {
		t.Fatalf("reconcileRegionDrift (2nd): %v", err)
	}
	if err := c.List(ctx, ops); err != nil {
		t.Fatal(err)
	}
	var cOps []nominatimv1alpha1.NominatimOperation
	for _, op := range ops.Items {
		if op.Spec.Type == nominatimv1alpha1.NominatimOperationAddRegions && len(op.Spec.Regions) == 1 && op.Spec.Regions[0] == "c" {
			cOps = append(cOps, op)
		}
	}
	if len(cOps) != 1 {
		t.Fatalf("expected exactly 1 AddRegions op for [c], got %d (all ops: %#v)", len(cOps), ops.Items)
	}
}

func TestReconcileRegionDrift_ReimportPolicyCreatesReimport(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := baseNominatim("drift-reimp")
	nom.Spec.Regions = []string{"south-america/brazil"}
	nom.Spec.RegionChangePolicy = nominatimv1alpha1.RegionChangeReimport
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{
		{Name: "north-america/us-midwest", Phase: regionPhaseImported},
		{Name: "asia/kazakhstan", Phase: regionPhaseImported},
	}
	nom.Status.Database.ConnectionSecretName = testConnSecret

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileRegionDrift(ctx, nom); err != nil {
		t.Fatalf("reconcileRegionDrift: %v", err)
	}

	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(ctx, ops); err != nil {
		t.Fatal(err)
	}
	if len(ops.Items) != 1 || ops.Items[0].Spec.Type != nominatimv1alpha1.NominatimOperationReimport {
		t.Fatalf("expected Reimport, got %#v", ops.Items)
	}
	if len(ops.Items[0].Spec.Regions) != 1 || ops.Items[0].Spec.Regions[0] != "south-america/brazil" {
		t.Fatalf("reimport regions=%v", ops.Items[0].Spec.Regions)
	}
}

func TestReconcileRegionDrift_RemovalDoesNotCreateOpOrShrinkStatus(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := baseNominatim("drift-rm")
	nom.Spec.Regions = []string{"north-america/us-midwest"} // removed asia/kazakhstan from spec
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{
		{Name: "north-america/us-midwest", Phase: regionPhaseImported},
		{Name: "asia/kazakhstan", Phase: regionPhaseImported},
	}
	nom.Status.Database.ConnectionSecretName = testConnSecret

	before := append([]nominatimv1alpha1.RegionStatus(nil), nom.Status.Regions...)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileRegionDrift(ctx, nom); err != nil {
		t.Fatalf("reconcileRegionDrift: %v", err)
	}
	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(ctx, ops); err != nil {
		t.Fatal(err)
	}
	if len(ops.Items) != 0 {
		t.Fatalf("removal must not auto-create operations, got %d", len(ops.Items))
	}
	if len(nom.Status.Regions) != len(before) {
		t.Fatalf("removal must not shrink status.regions (no surgical delete), got %d want %d",
			len(nom.Status.Regions), len(before))
	}
}

func TestReconcileRegionDrift_NoParallelAddRegions(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := baseNominatim("drift-serial")
	nom.Spec.Regions = []string{"a", "b", "c"}
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "a"}}
	nom.Status.Database.ConnectionSecretName = testConnSecret

	active := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "existing-add", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationAddRegions,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			Regions:      []string{"b"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom, active).WithObjects(nom, active).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileRegionDrift(ctx, nom); err != nil {
		t.Fatal(err)
	}
	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(ctx, ops); err != nil {
		t.Fatal(err)
	}
	if len(ops.Items) != 1 {
		t.Fatalf("expected no second AddRegions while one is active, got %d", len(ops.Items))
	}
}

func TestSyncRegionsFromDriftOps_AddAndReimport(t *testing.T) {
	nom := baseNominatim("sync-drift")
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "a"}}

	add := nominatimv1alpha1.NominatimOperation{
		Spec:   nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationAddRegions, Regions: []string{"b"}},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	syncRegionsFromDriftOps(nom, []nominatimv1alpha1.NominatimOperation{add})
	if len(nom.Status.Regions) != 2 {
		t.Fatalf("add merge: %#v", nom.Status.Regions)
	}

	reimp := nominatimv1alpha1.NominatimOperation{
		Spec:   nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationReimport, Regions: []string{"c"}},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	syncRegionsFromDriftOps(nom, []nominatimv1alpha1.NominatimOperation{reimp})
	if len(nom.Status.Regions) != 1 || nom.Status.Regions[0].Name != "c" {
		t.Fatalf("reimport replace: %#v", nom.Status.Regions)
	}
}

func TestReconcileRegionDrift_SkipsWhenEmptyStatus(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("drift-empty")
	nom.Spec.Regions = []string{"north-america/us-midwest"}
	// status.regions empty → bootstrap owns this; drift must no-op
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileRegionDrift(context.Background(), nom); err != nil {
		t.Fatal(err)
	}
	ops := &nominatimv1alpha1.NominatimOperationList{}
	_ = c.List(context.Background(), ops)
	if len(ops.Items) != 0 {
		t.Fatalf("expected drift to defer to bootstrap when status empty, got %d ops", len(ops.Items))
	}
}

func TestReconcileRegionDrift_SkipsWhenBootstrapActive(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := baseNominatim("drift-boot-active")
	nom.Spec.Regions = []string{"a", "b"}
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "a"}}
	nom.Status.Database.ConnectionSecretName = testConnSecret
	boot := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "boot", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom, boot).WithObjects(nom, boot).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileRegionDrift(ctx, nom); err != nil {
		t.Fatal(err)
	}
	ops := &nominatimv1alpha1.NominatimOperationList{}
	_ = c.List(ctx, ops)
	var adds int
	for _, op := range ops.Items {
		if op.Spec.Type == nominatimv1alpha1.NominatimOperationAddRegions {
			adds++
		}
	}
	if adds != 0 {
		t.Fatalf("expected no AddRegions while Bootstrap active, got %d", adds)
	}
}
