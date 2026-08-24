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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

func deletingNominatim(name string) *nominatimv1alpha1.NominatimInstance {
	nom := baseNominatim(name)
	controllerutil.AddFinalizer(nom, nominatimv1alpha1.NominatimInstanceFinalizer)
	now := metav1.Now()
	nom.DeletionTimestamp = &now
	return nom
}

func TestReconcileDelete_DrainsChildOperationsBeforeFinalizer(t *testing.T) {
	scheme := testScheme(t)
	nom := deletingNominatim("drain-ops")
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "drain-ops-bootstrap",
			Namespace:  "default",
			Finalizers: []string{nominatimv1alpha1.NominatimOperationFinalizer},
		},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom, op).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	res, err := r.reconcileDelete(context.Background(), nom)
	if err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected requeue while child Operation remains, got %#v", res)
	}

	gotOp := &nominatimv1alpha1.NominatimOperation{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: op.Name, Namespace: op.Namespace}, gotOp); err != nil {
		t.Fatalf("get operation: %v", err)
	}
	if gotOp.DeletionTimestamp.IsZero() {
		t.Fatal("expected child Operation deletion to be requested")
	}

	gotNom := &nominatimv1alpha1.NominatimInstance{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}, gotNom); err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if !controllerutil.ContainsFinalizer(gotNom, nominatimv1alpha1.NominatimInstanceFinalizer) {
		t.Fatal("finalizer must remain while Operations are draining")
	}
}

func TestReconcileDelete_WaitsForOwnedCNPGBeforeFinalizer(t *testing.T) {
	scheme := testScheme(t)
	nom := deletingNominatim("drain-cnpg")
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": CNPGClusterGVK.GroupVersion().String(),
		"kind":       CNPGClusterGVK.Kind,
		"metadata": map[string]interface{}{
			"name":       OwnedCNPGClusterName(nom),
			"namespace":  nom.Namespace,
			"finalizers": []interface{}{"postgresql.cnpg.io/deleteCluster"},
		},
		"spec": map[string]interface{}{},
	}}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	if err := controllerutil.SetControllerReference(nom, cluster, scheme); err != nil {
		t.Fatalf("ownerRef: %v", err)
	}
	db := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": CNPGDatabaseGVK.GroupVersion().String(),
		"kind":       CNPGDatabaseGVK.Kind,
		"metadata": map[string]interface{}{
			"name":       OwnedCNPGDatabaseName(nom),
			"namespace":  nom.Namespace,
			"finalizers": []interface{}{"postgresql.cnpg.io/deleteDatabase"},
		},
		"spec": map[string]interface{}{},
	}}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	if err := controllerutil.SetControllerReference(nom, db, scheme); err != nil {
		t.Fatalf("ownerRef: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom, cluster, db).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	res, err := r.reconcileDelete(context.Background(), nom)
	if err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected requeue while owned CNPG remains, got %#v", res)
	}

	gotDB := &unstructured.Unstructured{}
	gotDB.SetGroupVersionKind(CNPGDatabaseGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: db.GetName(), Namespace: nom.Namespace}, gotDB); err != nil {
		t.Fatalf("get database: %v", err)
	}
	if gotDB.GetDeletionTimestamp().IsZero() {
		t.Fatal("expected owned CNPG Database deletion to be requested first")
	}

	gotNom := &nominatimv1alpha1.NominatimInstance{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}, gotNom); err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if !controllerutil.ContainsFinalizer(gotNom, nominatimv1alpha1.NominatimInstanceFinalizer) {
		t.Fatal("finalizer must remain while owned CNPG resources terminate")
	}
}

func TestReconcileDelete_LeavesClaimNamePVCAndAttachedCluster(t *testing.T) {
	scheme := testScheme(t)
	nom := deletingNominatim("leave-claim")
	nom.Spec.Project.Volume.ClaimName = "monaco-project"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "monaco-project", Namespace: "default"},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}
	attached := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": CNPGClusterGVK.GroupVersion().String(),
		"kind":       CNPGClusterGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      "external-pg",
			"namespace": nom.Namespace,
		},
		"spec": map[string]interface{}{},
	}}
	attached.SetGroupVersionKind(CNPGClusterGVK)

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom, pvc, attached).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	res, err := r.reconcileDelete(context.Background(), nom)
	if err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}
	if !res.IsZero() {
		t.Fatalf("expected finalizer removal with no owned dependents, got %#v", res)
	}

	if err := c.Get(context.Background(), types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}, &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("claimName PVC must remain: %v", err)
	}
	gotCluster := &unstructured.Unstructured{}
	gotCluster.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: attached.GetName(), Namespace: nom.Namespace}, gotCluster); err != nil {
		t.Fatalf("attached cluster must remain: %v", err)
	}
	if !gotCluster.GetDeletionTimestamp().IsZero() {
		t.Fatal("must not delete clusterRef / non-owned Cluster")
	}

	gotNom := &nominatimv1alpha1.NominatimInstance{}
	err = c.Get(context.Background(), types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}, gotNom)
	if err == nil && controllerutil.ContainsFinalizer(gotNom, nominatimv1alpha1.NominatimInstanceFinalizer) {
		t.Fatal("expected Instance finalizer removed")
	}
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get instance: %v", err)
	}
}

func TestReconcileDelete_SetsDeletingCondition(t *testing.T) {
	scheme := testScheme(t)
	nom := deletingNominatim("mark-del")
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	if _, err := r.reconcileDelete(context.Background(), nom); err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}

	got := &nominatimv1alpha1.NominatimInstance{}
	err := c.Get(context.Background(), types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}, got)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get: %v", err)
	}
	// Finalizer removed may delete the object in envtest; status was written first on the live object.
	// Re-read via a second client path: markInstanceDeleting updates status before finalizer removal,
	// so assert on the in-memory nom copy updated by reconcileDelete.
	if !meta.IsStatusConditionTrue(nom.Status.Conditions, nominatimv1alpha1.ConditionDeleting) {
		t.Fatalf("expected Deleting=True on reconciled object, got %#v", nom.Status.Conditions)
	}
}

func TestOperationReconcile_RefusesWhenParentDeleting(t *testing.T) {
	scheme := testScheme(t)
	parent := deletingNominatim("parent-del")
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "late-op", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationUpdate,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent, op).WithObjects(parent, op).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: op.Name, Namespace: op.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &nominatimv1alpha1.NominatimOperation{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: op.Name, Namespace: op.Namespace}, got); err != nil {
		t.Fatalf("get op: %v", err)
	}
	if got.Status.Phase != nominatimv1alpha1.NominatimOperationPhaseFailed {
		t.Fatalf("phase=%q want Failed", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, reasonParentDeleting) {
		t.Fatalf("message=%q want substring %q", got.Status.Message, reasonParentDeleting)
	}
}

func TestReconcileDelete_WaitsForOwnedClusterAfterDatabaseGone(t *testing.T) {
	scheme := testScheme(t)
	nom := deletingNominatim("db-then-cluster")
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": CNPGClusterGVK.GroupVersion().String(),
		"kind":       CNPGClusterGVK.Kind,
		"metadata": map[string]interface{}{
			"name":       OwnedCNPGClusterName(nom),
			"namespace":  nom.Namespace,
			"finalizers": []interface{}{"postgresql.cnpg.io/deleteCluster"},
		},
		"spec": map[string]interface{}{},
	}}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	if err := controllerutil.SetControllerReference(nom, cluster, scheme); err != nil {
		t.Fatalf("ownerRef: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom, cluster).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	res, err := r.reconcileDelete(context.Background(), nom)
	if err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected requeue while owned Cluster remains, got %#v", res)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: cluster.GetName(), Namespace: nom.Namespace}, got); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	if got.GetDeletionTimestamp().IsZero() {
		t.Fatal("expected owned Cluster deletion requested after Database absent")
	}
}

func TestReconcileDelete_IgnoresAlreadyTerminatingOperations(t *testing.T) {
	scheme := testScheme(t)
	nom := deletingNominatim("op-terminating")
	now := metav1.Now()
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "op-terminating-boot",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{nominatimv1alpha1.NominatimOperationFinalizer},
		},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom, op).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	res, err := r.reconcileDelete(context.Background(), nom)
	if err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected requeue while terminating Operation remains, got %#v", res)
	}
	gotNom := &nominatimv1alpha1.NominatimInstance{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}, gotNom); err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if !controllerutil.ContainsFinalizer(gotNom, nominatimv1alpha1.NominatimInstanceFinalizer) {
		t.Fatal("finalizer must remain while Operations terminate")
	}
}

func TestReconcileDelete_DrainAndCNPGErrors(t *testing.T) {
	scheme := testScheme(t)
	nom := deletingNominatim("del-errs")
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom).Build()
	r := &NominatimInstanceReconciler{Client: stubClient{Client: base, failList: true}, Scheme: scheme}
	if _, err := r.reconcileDelete(context.Background(), nom); err == nil {
		t.Fatal("expected list error while draining Operations")
	}

	nom2 := deletingNominatim("del-status")
	base2 := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom2).WithObjects(nom2).Build()
	r.Client = stubClient{Client: base2, failStatus: true}
	if _, err := r.reconcileDelete(context.Background(), nom2); err == nil {
		t.Fatal("expected status update error when marking Deleting")
	}
}

func TestDeleteOwnedUnstructuredIfController_NotOwnedOrMissing(t *testing.T) {
	scheme := testScheme(t)
	nom := deletingNominatim("own-check")
	other := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": CNPGClusterGVK.GroupVersion().String(),
		"kind":       CNPGClusterGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      OwnedCNPGClusterName(nom),
			"namespace": nom.Namespace,
		},
		"spec": map[string]interface{}{},
	}}
	other.SetGroupVersionKind(CNPGClusterGVK)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, other).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	waiting, err := r.deleteOwnedUnstructuredIfController(context.Background(), nom, CNPGClusterGVK, OwnedCNPGClusterName(nom))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if waiting {
		t.Fatal("non-owned Cluster must not be deleted or waited on")
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: other.GetName(), Namespace: nom.Namespace}, got); err != nil {
		t.Fatalf("non-owned Cluster must remain: %v", err)
	}

	waiting, err = r.deleteOwnedUnstructuredIfController(context.Background(), nom, CNPGDatabaseGVK, OwnedCNPGDatabaseName(nom))
	if err != nil || waiting {
		t.Fatalf("missing Database should be no-op, waiting=%v err=%v", waiting, err)
	}
}
