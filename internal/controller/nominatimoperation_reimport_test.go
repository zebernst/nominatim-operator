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
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

func newOwnedCNPGDatabase(nom *nominatimv1alpha1.Nominatim, uid string, applied bool) *unstructured.Unstructured {
	db := &unstructured.Unstructured{}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	db.SetName(OwnedCNPGDatabaseName(nom))
	db.SetNamespace(nom.Namespace)
	db.SetUID(types.UID(uid))
	db.SetAnnotations(map[string]string{})
	_ = unstructured.SetNestedField(db.Object, cnpgAppDatabaseName, "spec", "name")
	_ = unstructured.SetNestedField(db.Object, cnpgAppOwnerName, "spec", "owner")
	_ = unstructured.SetNestedField(db.Object, OwnedCNPGClusterName(nom), "spec", "cluster", "name")
	_ = unstructured.SetNestedField(db.Object, "delete", "spec", "databaseReclaimPolicy")
	_ = unstructured.SetNestedField(db.Object, "present", "spec", "ensure")
	_ = unstructured.SetNestedField(db.Object, applied, "status", "applied")
	return db
}

func readyOwnedCNPGCluster(nom *nominatimv1alpha1.Nominatim) *unstructured.Unstructured {
	cluster := newCNPGCluster(OwnedCNPGClusterName(nom))
	_ = unstructured.SetNestedSlice(cluster.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	return cluster
}

func TestEnsureReimportDatabaseReset_SkipsNonReimport(t *testing.T) {
	r := &NominatimOperationReconciler{}
	op := &nominatimv1alpha1.NominatimOperation{
		Spec: nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationBootstrap},
	}
	ready, err := r.ensureReimportDatabaseReset(context.Background(), op, baseNominatim("skip"))
	if err != nil || !ready {
		t.Fatalf("ready=%v err=%v want ready=true", ready, err)
	}
}

func TestEnsureReimportDatabaseReset_SkipsNonOwnedCluster(t *testing.T) {
	r := &NominatimOperationReconciler{}
	op := &nominatimv1alpha1.NominatimOperation{
		Spec: nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationReimport},
	}
	parent := baseNominatim("secret-mode")
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{Name: "ext"},
	}
	parent.Status.Database.Mode = nominatimv1alpha1.DatabaseModeConnectionSecret
	ready, err := r.ensureReimportDatabaseReset(context.Background(), op, parent)
	if err != nil || !ready {
		t.Fatalf("ready=%v err=%v want ready=true for connection-secret mode", ready, err)
	}
}

func TestEnsureReimportDatabaseReset_DeletesThenRecreates(t *testing.T) {
	scheme := testScheme(t)
	parent := baseNominatim("ri-reset")
	instances := int32(1)
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		Cluster: &nominatimv1alpha1.DatabaseClusterCreate{Instances: &instances},
	}
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode:                 nominatimv1alpha1.DatabaseModeClusterManaged,
		ClusterName:          OwnedCNPGClusterName(parent),
		ConnectionSecretName: CNPGAppSecretName(OwnedCNPGClusterName(parent)),
	}

	oldDB := newOwnedCNPGDatabase(parent, "uid-old", true)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: parent.Status.Database.ConnectionSecretName, Namespace: "default"},
		Data:       map[string][]byte{"uri": []byte("postgresql://n:p@h/db")},
	}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "ri-op", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationReimport,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name},
			Regions:      []string{"europe/monaco"},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(parent, op, oldDB, secret, readyOwnedCNPGCluster(parent)).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	// Pass 1: delete existing Database, not ready yet.
	ready, err := r.ensureReimportDatabaseReset(context.Background(), op, parent)
	if err != nil {
		t.Fatalf("pass1: %v", err)
	}
	if ready {
		t.Fatal("pass1: expected ready=false after requesting delete")
	}
	gotOp := &nominatimv1alpha1.NominatimOperation{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: op.Name, Namespace: "default"}, gotOp); err != nil {
		t.Fatalf("get op: %v", err)
	}
	if gotOp.Annotations[annotationReimportDBPrevUID] != "uid-old" {
		t.Fatalf("prev uid annotation=%q", gotOp.Annotations[annotationReimportDBPrevUID])
	}
	db := &unstructured.Unstructured{}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	err = c.Get(context.Background(), types.NamespacedName{Name: OwnedCNPGDatabaseName(parent), Namespace: "default"}, db)
	if err == nil {
		t.Fatal("expected Database deleted after pass1")
	}
	if client.IgnoreNotFound(err) != nil {
		t.Fatalf("get database: %v", err)
	}

	// Pass 2: recreate Database CR (status.applied still false).
	op = gotOp
	ready, err = r.ensureReimportDatabaseReset(context.Background(), op, parent)
	if err != nil {
		t.Fatalf("pass2: %v", err)
	}
	if ready {
		t.Fatal("pass2: expected ready=false until applied=true")
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: OwnedCNPGDatabaseName(parent), Namespace: "default"}, db); err != nil {
		t.Fatalf("pass2 get database: %v", err)
	}
	reclaim, _, _ := unstructured.NestedString(db.Object, "spec", "databaseReclaimPolicy")
	if reclaim != "delete" {
		t.Fatalf("reclaim=%q want delete", reclaim)
	}

	// Simulate CNPG applying extensions on the new Database.
	_ = unstructured.SetNestedField(db.Object, true, "status", "applied")
	db.SetUID("uid-new")
	if err := c.Update(context.Background(), db); err != nil {
		t.Fatalf("mark applied: %v", err)
	}

	// Pass 3: applied with new UID → done.
	if err := c.Get(context.Background(), types.NamespacedName{Name: op.Name, Namespace: "default"}, op); err != nil {
		t.Fatalf("reload op: %v", err)
	}
	ready, err = r.ensureReimportDatabaseReset(context.Background(), op, parent)
	if err != nil || !ready {
		t.Fatalf("pass3: ready=%v err=%v", ready, err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: op.Name, Namespace: "default"}, op); err != nil {
		t.Fatalf("reload op done: %v", err)
	}
	if op.Annotations[annotationReimportDBReset] != reimportDBResetDone {
		t.Fatalf("reset annotation=%q want %q", op.Annotations[annotationReimportDBReset], reimportDBResetDone)
	}

	// Pass 4: idempotent once done.
	ready, err = r.ensureReimportDatabaseReset(context.Background(), op, parent)
	if err != nil || !ready {
		t.Fatalf("pass4: ready=%v err=%v", ready, err)
	}
}

func TestReconcileReimport_WaitsForDatabaseResetBeforeJob(t *testing.T) {
	scheme := testScheme(t)
	parent := baseNominatim("ri-job")
	instances := int32(1)
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		Cluster: &nominatimv1alpha1.DatabaseClusterCreate{Instances: &instances},
	}
	parent.Spec.Project = nominatimv1alpha1.ProjectSpec{
		Volume: nominatimv1alpha1.VolumeSource{ClaimName: "project"},
	}
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode:                 nominatimv1alpha1.DatabaseModeClusterManaged,
		ClusterName:          OwnedCNPGClusterName(parent),
		ConnectionSecretName: CNPGAppSecretName(OwnedCNPGClusterName(parent)),
	}
	oldDB := newOwnedCNPGDatabase(parent, "uid-old", true)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: parent.Status.Database.ConnectionSecretName, Namespace: "default"},
		Data:       map[string][]byte{"uri": []byte("postgresql://n:p@h/db")},
	}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "ri-job-op", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationReimport,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name},
			Regions:      []string{"europe/monaco"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&nominatimv1alpha1.NominatimOperation{}).
		WithObjects(parent, op, oldDB, secret, readyOwnedCNPGCluster(parent)).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: op.Name, Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile1: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected requeue while resetting database, got %#v", res)
	}
	job := &batchv1.Job{}
	err = c.Get(context.Background(), types.NamespacedName{Name: op.Name, Namespace: "default"}, job)
	if err == nil {
		t.Fatal("Job must not exist before Database reset completes")
	}
}
