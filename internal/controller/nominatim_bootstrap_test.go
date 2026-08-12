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

// Shared Geofabrik region paths used across bootstrap tests (goconst).
const (
	testRegionMonaco  = "europe/monaco"
	testRegionMorocco = "africa/morocco"
)

func nominatimWithRegions(name string, regions ...string) *nominatimv1alpha1.NominatimInstance {
	nom := nominatimWithConnectionSecret(name)
	nom.Spec.Regions = regions
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	return nom
}

func TestEnsureBootstrapOperation_NoopWhenNoRegionsDesired(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("bootstrap-noregions")
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	if err := r.ensureBootstrapOperation(context.Background(), nom, parentOps(t, r, context.Background(), nom)); err != nil {
		t.Fatalf("ensureBootstrapOperation: %v", err)
	}

	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(context.Background(), ops); err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(ops.Items) != 0 {
		t.Fatalf("expected no operations created, got %d", len(ops.Items))
	}
}

func TestEnsureBootstrapOperation_NoopWhenStatusRegionsAlreadyPopulated(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-already-imported", testRegionMonaco)
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: testRegionMonaco}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	if err := r.ensureBootstrapOperation(context.Background(), nom, parentOps(t, r, context.Background(), nom)); err != nil {
		t.Fatalf("ensureBootstrapOperation: %v", err)
	}

	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(context.Background(), ops); err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(ops.Items) != 0 {
		t.Fatalf("expected no operations created when status.regions already populated, got %d", len(ops.Items))
	}
}

func TestEnsureBootstrapOperation_CreatesWhenEmpty(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-empty", testRegionMonaco, testRegionMorocco)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	if err := r.ensureBootstrapOperation(context.Background(), nom, parentOps(t, r, context.Background(), nom)); err != nil {
		t.Fatalf("ensureBootstrapOperation: %v", err)
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
	if len(op.Spec.Regions) != 2 || op.Spec.Regions[0] != testRegionMonaco || op.Spec.Regions[1] != testRegionMorocco {
		t.Fatalf("regions=%v want copy of spec.regions", op.Spec.Regions)
	}
	if !metav1.IsControlledBy(op, nom) {
		t.Fatalf("expected Bootstrap operation to be controlled by parent NominatimInstance, ownerRefs=%v", op.OwnerReferences)
	}
}

func TestEnsureBootstrapOperation_NoopWhenBootstrapAlreadyActive(t *testing.T) {
	for _, phase := range []nominatimv1alpha1.NominatimOperationPhase{
		nominatimv1alpha1.NominatimOperationPhasePending,
		nominatimv1alpha1.NominatimOperationPhaseRunning,
		"",
	} {
		t.Run(string(phase), func(t *testing.T) {
			scheme := testScheme(t)
			nom := nominatimWithRegions("bootstrap-active-"+sanitizePhase(phase), testRegionMonaco)
			existing := &nominatimv1alpha1.NominatimOperation{
				ObjectMeta: metav1.ObjectMeta{Name: BootstrapOperationName(nom), Namespace: "default"},
				Spec: nominatimv1alpha1.NominatimOperationSpec{
					Type:         nominatimv1alpha1.NominatimOperationBootstrap,
					NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
				},
				Status: nominatimv1alpha1.NominatimOperationStatus{Phase: phase},
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, existing).Build()
			r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

			if err := r.ensureBootstrapOperation(context.Background(), nom, parentOps(t, r, context.Background(), nom)); err != nil {
				t.Fatalf("ensureBootstrapOperation: %v", err)
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

func TestEnsureBootstrapOperation_NoopWhenBootstrapAlreadyTerminal(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-terminal", testRegionMonaco)
	existing := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: BootstrapOperationName(nom), Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseFailed},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, existing).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	if err := r.ensureBootstrapOperation(context.Background(), nom, parentOps(t, r, context.Background(), nom)); err != nil {
		t.Fatalf("ensureBootstrapOperation: %v", err)
	}

	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(context.Background(), ops); err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(ops.Items) != 1 {
		t.Fatalf("expected no new operation created alongside a terminal Bootstrap, got %d", len(ops.Items))
	}
}

func TestEnsureBootstrapOperation_ErrorsWhenConnectionSecretNameEmpty(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("bootstrap-nosecret")
	nom.Spec.Regions = []string{testRegionMonaco}
	// Deliberately leave nom.Status.Database.ConnectionSecretName empty.
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	if err := r.ensureBootstrapOperation(context.Background(), nom, parentOps(t, r, context.Background(), nom)); err == nil {
		t.Fatal("expected error when connectionSecretName is empty")
	}
}

func TestEnsureBootstrapOperation_IgnoresOperationsForOtherParents(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-other-parent", testRegionMonaco)
	other := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "other-nom-bootstrap", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "some-other-nominatim"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, other).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	if err := r.ensureBootstrapOperation(context.Background(), nom, parentOps(t, r, context.Background(), nom)); err != nil {
		t.Fatalf("ensureBootstrapOperation: %v", err)
	}

	op := &nominatimv1alpha1.NominatimOperation{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: BootstrapOperationName(nom), Namespace: "default"}, op); err != nil {
		t.Fatalf("expected Bootstrap operation to be created despite unrelated peer: %v", err)
	}
}

func TestEnsureBootstrapOperation_NoCreateAfterObservePopulatedRegions(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-succeeded", testRegionMonaco, testRegionMorocco)
	succeeded := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: BootstrapOperationName(nom), Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			Regions:      []string{testRegionMonaco, testRegionMorocco},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, succeeded).Build()
	observeRegionsFromSucceededOps(nom, []nominatimv1alpha1.NominatimOperation{*succeeded})

	if len(nom.Status.Regions) != 2 {
		t.Fatalf("status.regions=%v want 2 entries", nom.Status.Regions)
	}
	for i, want := range []string{testRegionMonaco, testRegionMorocco} {
		if nom.Status.Regions[i].Name != want {
			t.Fatalf("status.regions[%d].Name=%q want %q", i, nom.Status.Regions[i].Name, want)
		}
		if nom.Status.Regions[i].LastUpdatedTime == nil {
			t.Fatalf("status.regions[%d].LastUpdatedTime is nil", i)
		}
	}

	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}
	if err := r.ensureBootstrapOperation(context.Background(), nom, parentOps(t, r, context.Background(), nom)); err != nil {
		t.Fatalf("ensureBootstrapOperation: %v", err)
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

func TestEnsureBootstrapOperation_DoesNotSyncRegions(t *testing.T) {
	// Observe is a separate module (observeRegionsFromSucceededOps). Bootstrap
	// reconcile only ensures the Operation exists.
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-ensure-only", testRegionMonaco)
	succeeded := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: BootstrapOperationName(nom), Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			Regions:      []string{testRegionMonaco},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, succeeded).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	if err := r.ensureBootstrapOperation(context.Background(), nom, parentOps(t, r, context.Background(), nom)); err != nil {
		t.Fatalf("ensureBootstrapOperation: %v", err)
	}
	if len(nom.Status.Regions) != 0 {
		t.Fatalf("ensure-only bootstrap must not write status.regions, got %#v", nom.Status.Regions)
	}
}

// schemeWithOperationOnly registers NominatimOperation but omits NominatimInstance, so
// SetControllerReference cannot resolve the owner's GVK.
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

func TestEnsureBootstrapOperation_SetControllerReferenceError(t *testing.T) {
	scheme := schemeWithOperationOnly(t)
	nom := nominatimWithRegions("bootstrap-owner-error", testRegionMonaco)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	if err := r.ensureBootstrapOperation(context.Background(), nom, parentOps(t, r, context.Background(), nom)); err == nil {
		t.Fatal("expected SetControllerReference error to propagate")
	}
}

func TestEnsureBootstrapOperation_CreateErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithRegions("bootstrap-create-error", testRegionMonaco)
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	fc := &failingClient{Client: base, failCreate: []failSpec{{kind: "NominatimOperation"}}}
	r := &NominatimInstanceReconciler{Client: fc, Scheme: scheme}

	if err := r.ensureBootstrapOperation(context.Background(), nom, parentOps(t, r, context.Background(), nom)); err == nil {
		t.Fatal("expected Create error to propagate")
	}
}

func TestReconcile_PropagatesBootstrapError(t *testing.T) {
	scheme := testScheme(t)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"}}
	nom := baseNominatim("reconcile-bootstrap-error")
	nom.Spec.Regions = []string{testRegionMonaco}
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{Name: "s"},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom, secret).Build()
	fc := &failingListClient{Client: base}
	r := &NominatimInstanceReconciler{Client: fc, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}}

	// First reconcile only adds the finalizer.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	_, err := r.Reconcile(context.Background(), req)
	if err == nil {
		t.Fatal("expected Reconcile to propagate list/ensure bootstrap error")
	}
}

func TestBootstrapOperationName(t *testing.T) {
	nom := &nominatimv1alpha1.NominatimInstance{ObjectMeta: metav1.ObjectMeta{Name: "mynom"}}
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
