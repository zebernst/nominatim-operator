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
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

func nominatimWithRegions(name string, regions ...string) *nominatimv1alpha1.Nominatim {
	nom := nominatimWithConnectionSecret(name)
	nom.Spec.Regions = regions
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	return nom
}

func TestReconcileBootstrap_NoopWhenNoRegionsDesired(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("bootstrap-noregions")
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileBootstrap(context.Background(), nom); err != nil {
		t.Fatalf("reconcileBootstrap: %v", err)
	}

	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(context.Background(), ops); err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(ops.Items) != 0 {
		t.Fatalf("expected no operations created, got %d", len(ops.Items))
	}
}

func TestReconcileBootstrap_NoopWhenStatusRegionsAlreadyPopulated(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-already-imported", "europe/monaco")
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco", Phase: "Imported"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileBootstrap(context.Background(), nom); err != nil {
		t.Fatalf("reconcileBootstrap: %v", err)
	}

	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(context.Background(), ops); err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(ops.Items) != 0 {
		t.Fatalf("expected no operations created when status.regions already populated, got %d", len(ops.Items))
	}
}

func TestReconcileBootstrap_CreatesOperationWhenEmpty(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-empty", "europe/monaco", "africa/morocco")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileBootstrap(context.Background(), nom); err != nil {
		t.Fatalf("reconcileBootstrap: %v", err)
	}

	op := &nominatimv1alpha1.NominatimOperation{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: BootstrapOperationName(nom), Namespace: "default"}, op); err != nil {
		t.Fatalf("expected Bootstrap operation %q to be created: %v", BootstrapOperationName(nom), err)
	}
	if op.Spec.Type != nominatimv1alpha1.NominatimOperationBootstrap {
		t.Fatalf("type=%q want Bootstrap", op.Spec.Type)
	}
	if op.Spec.NominatimRef.Name != nom.Name {
		t.Fatalf("nominatimRef=%q want %q", op.Spec.NominatimRef.Name, nom.Name)
	}
	if len(op.Spec.Regions) != 2 || op.Spec.Regions[0] != "europe/monaco" || op.Spec.Regions[1] != "africa/morocco" {
		t.Fatalf("regions=%v want copy of spec.regions", op.Spec.Regions)
	}
	if !metav1.IsControlledBy(op, nom) {
		t.Fatalf("expected Bootstrap operation to be controlled by parent Nominatim, ownerRefs=%v", op.OwnerReferences)
	}
}

func TestReconcileBootstrap_NoopWhenBootstrapAlreadyPendingOrRunning(t *testing.T) {
	for _, phase := range []nominatimv1alpha1.NominatimOperationPhase{
		nominatimv1alpha1.NominatimOperationPhasePending,
		nominatimv1alpha1.NominatimOperationPhaseRunning,
		"",
	} {
		t.Run(string(phase), func(t *testing.T) {
			scheme := testScheme(t)
			nom := nominatimWithRegions("bootstrap-active-"+sanitizePhase(phase), "europe/monaco")
			existing := &nominatimv1alpha1.NominatimOperation{
				ObjectMeta: metav1.ObjectMeta{Name: BootstrapOperationName(nom), Namespace: "default"},
				Spec: nominatimv1alpha1.NominatimOperationSpec{
					Type:         nominatimv1alpha1.NominatimOperationBootstrap,
					NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
				},
				Status: nominatimv1alpha1.NominatimOperationStatus{Phase: phase},
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, existing).Build()
			r := &NominatimReconciler{Client: c, Scheme: scheme}

			if err := r.reconcileBootstrap(context.Background(), nom); err != nil {
				t.Fatalf("reconcileBootstrap: %v", err)
			}

			ops := &nominatimv1alpha1.NominatimOperationList{}
			if err := c.List(context.Background(), ops); err != nil {
				t.Fatalf("list operations: %v", err)
			}
			if len(ops.Items) != 1 {
				t.Fatalf("expected exactly the pre-existing operation, got %d", len(ops.Items))
			}
		})
	}
}

func TestReconcileBootstrap_NoopWhenBootstrapAlreadyTerminal(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-terminal", "europe/monaco")
	existing := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: BootstrapOperationName(nom), Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseFailed},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, existing).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileBootstrap(context.Background(), nom); err != nil {
		t.Fatalf("reconcileBootstrap: %v", err)
	}

	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(context.Background(), ops); err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(ops.Items) != 1 {
		t.Fatalf("expected no new operation created alongside a terminal Bootstrap, got %d", len(ops.Items))
	}
}

func TestReconcileBootstrap_ErrorsWhenConnectionSecretNameEmpty(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("bootstrap-nosecret")
	nom.Spec.Regions = []string{"europe/monaco"}
	// Deliberately leave nom.Status.Database.ConnectionSecretName empty.
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileBootstrap(context.Background(), nom); err == nil {
		t.Fatal("expected error when connectionSecretName is empty")
	}
}

func TestReconcileBootstrap_IgnoresOperationsForOtherParents(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-other-parent", "europe/monaco")
	other := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "other-nom-bootstrap", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "some-other-nominatim"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, other).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileBootstrap(context.Background(), nom); err != nil {
		t.Fatalf("reconcileBootstrap: %v", err)
	}

	op := &nominatimv1alpha1.NominatimOperation{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: BootstrapOperationName(nom), Namespace: "default"}, op); err != nil {
		t.Fatalf("expected Bootstrap operation to be created despite unrelated peer: %v", err)
	}
}

func TestReconcileBootstrap_SyncsStatusRegionsAfterSucceeded(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-succeeded", "europe/monaco", "africa/morocco")
	succeeded := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: BootstrapOperationName(nom), Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			Regions:      []string{"europe/monaco", "africa/morocco"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, succeeded).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileBootstrap(context.Background(), nom); err != nil {
		t.Fatalf("reconcileBootstrap: %v", err)
	}

	if len(nom.Status.Regions) != 2 {
		t.Fatalf("status.regions=%v want 2 entries", nom.Status.Regions)
	}
	for i, want := range []string{"europe/monaco", "africa/morocco"} {
		if nom.Status.Regions[i].Name != want {
			t.Fatalf("status.regions[%d].Name=%q want %q", i, nom.Status.Regions[i].Name, want)
		}
		if nom.Status.Regions[i].Phase != "Imported" {
			t.Fatalf("status.regions[%d].Phase=%q want Imported", i, nom.Status.Regions[i].Phase)
		}
		if nom.Status.Regions[i].LastUpdatedTime == nil {
			t.Fatalf("status.regions[%d].LastUpdatedTime is nil", i)
		}
	}

	// No new Bootstrap operation should be created once status.regions is now populated.
	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(context.Background(), ops); err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(ops.Items) != 1 {
		t.Fatalf("expected only the pre-existing succeeded operation, got %d", len(ops.Items))
	}
}

func TestReconcileBootstrap_SyncFallsBackToParentSpecRegions(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-fallback-regions", "europe/monaco")
	succeeded := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: BootstrapOperationName(nom), Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			// Regions left empty on the Operation; sync should fall back to parent spec.
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, succeeded).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileBootstrap(context.Background(), nom); err != nil {
		t.Fatalf("reconcileBootstrap: %v", err)
	}
	if len(nom.Status.Regions) != 1 || nom.Status.Regions[0].Name != "europe/monaco" {
		t.Fatalf("status.regions=%v want [europe/monaco]", nom.Status.Regions)
	}
}

func TestReconcileBootstrap_SyncSkipsSucceededOperationWithNoRegionsAnywhere(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("bootstrap-no-regions-anywhere")
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	// nom.Spec.Regions intentionally left empty.
	succeeded := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: BootstrapOperationName(nom), Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			// Regions left empty on both the Operation and the parent spec.
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, succeeded).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileBootstrap(context.Background(), nom); err != nil {
		t.Fatalf("reconcileBootstrap: %v", err)
	}
	if len(nom.Status.Regions) != 0 {
		t.Fatalf("status.regions=%v want empty when no regions are known anywhere", nom.Status.Regions)
	}
}

func TestReconcileBootstrap_SyncIgnoresNonBootstrapOrNonSucceeded(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-ignore-others", "europe/monaco")
	running := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: BootstrapOperationName(nom), Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			Regions:      []string{"europe/monaco"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseRunning},
	}
	update := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: nom.Name + "-update", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, running, update).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileBootstrap(context.Background(), nom); err != nil {
		t.Fatalf("reconcileBootstrap: %v", err)
	}
	if len(nom.Status.Regions) != 0 {
		t.Fatalf("status.regions=%v want empty (no succeeded Bootstrap present)", nom.Status.Regions)
	}
}

func TestReconcileBootstrap_ListErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-list-error", "europe/monaco")
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	fc := &failingListClient{Client: base}
	r := &NominatimReconciler{Client: fc, Scheme: scheme}

	if err := r.reconcileBootstrap(context.Background(), nom); err == nil {
		t.Fatal("expected List error to propagate")
	}
}

// schemeWithOperationOnly registers NominatimOperation(List) but deliberately omits
// Nominatim(List), so List() calls for Operations succeed while
// controllerutil.SetControllerReference(nom, ...) fails to resolve the owner's GVK —
// isolating that specific error branch from the List error path exercised by
// TestReconcileBootstrap_ListErrorPropagates.
func schemeWithOperationOnly(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	metav1.AddToGroupVersion(s, nominatimv1alpha1.GroupVersion)
	s.AddKnownTypes(nominatimv1alpha1.GroupVersion, &nominatimv1alpha1.NominatimOperation{}, &nominatimv1alpha1.NominatimOperationList{})
	return s
}

func TestReconcileBootstrap_SetControllerReferenceError(t *testing.T) {
	scheme := schemeWithOperationOnly(t)
	nom := nominatimWithRegions("bootstrap-owner-error", "europe/monaco")
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileBootstrap(context.Background(), nom); err == nil {
		t.Fatal("expected SetControllerReference error to propagate")
	}
}

func TestReconcileBootstrap_CreateErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-create-error", "europe/monaco")
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	fc := &failingClient{Client: base, failCreate: []failSpec{{kind: "NominatimOperation"}}}
	r := &NominatimReconciler{Client: fc, Scheme: scheme}

	if err := r.reconcileBootstrap(context.Background(), nom); err == nil {
		t.Fatal("expected Create error to propagate")
	}
}

func TestReconcile_PropagatesBootstrapError(t *testing.T) {
	scheme := testScheme(t)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"}}
	nom := baseNominatim("reconcile-bootstrap-error")
	nom.Spec.Regions = []string{"europe/monaco"}
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{Name: "s"},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom, secret).Build()
	fc := &failingListClient{Client: base}
	r := &NominatimReconciler{Client: fc, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}}

	// First reconcile only adds the finalizer.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	_, err := r.Reconcile(context.Background(), req)
	if err == nil {
		t.Fatal("expected Reconcile to propagate reconcileBootstrap error")
	}
}

func TestBootstrapOperationName(t *testing.T) {
	nom := &nominatimv1alpha1.Nominatim{ObjectMeta: metav1.ObjectMeta{Name: "mynom"}}
	if got := BootstrapOperationName(nom); got != "mynom-bootstrap" {
		t.Fatalf("BootstrapOperationName=%q want mynom-bootstrap", got)
	}
}

func sanitizePhase(phase nominatimv1alpha1.NominatimOperationPhase) string {
	if phase == "" {
		return "empty"
	}
	return string(phase)
}

// failingListClient wraps a real client.Client to inject a simulated List failure,
// exercising the error-propagation branch in listOperationsForParent.
type failingListClient struct {
	client.Client
}

func (f *failingListClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*nominatimv1alpha1.NominatimOperationList); ok {
		return fmt.Errorf("simulated list failure")
	}
	return f.Client.List(ctx, list, opts...)
}
