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
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// errInjected stands in for an API-server failure that is not Conflict/NotFound, so the
// RetryOnConflict wrappers surface it instead of retrying.
var errInjected = errors.New("injected failure")

const (
	rebuildTestUIDOld   = "uid-old"
	rebuildTestUIDNew   = "uid-new"
	rebuildTestUIDFresh = "uid-fresh"
)

func newOwnedCNPGDatabase(nom *nominatimv1alpha1.NominatimInstance, uid string, applied bool) *unstructured.Unstructured {
	db := &unstructured.Unstructured{}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	db.SetName(OwnedCNPGDatabaseName(nom))
	db.SetNamespace(nom.Namespace)
	db.SetUID(types.UID(uid))
	db.SetAnnotations(map[string]string{})
	_ = unstructured.SetNestedField(db.Object, cnpgAppDatabaseName, "spec", "name")
	_ = unstructured.SetNestedField(db.Object, cnpgAppOwnerName, "spec", "owner")
	_ = unstructured.SetNestedField(db.Object, OwnedCNPGClusterName(nom), "spec", "cluster", "name")
	_ = unstructured.SetNestedField(db.Object, cnpgDatabaseReclaimDelete, "spec", "databaseReclaimPolicy")
	_ = unstructured.SetNestedField(db.Object, "present", "spec", "ensure")
	_ = unstructured.SetNestedField(db.Object, applied, "status", "applied")
	return db
}

func readyOwnedCNPGCluster(nom *nominatimv1alpha1.NominatimInstance) *unstructured.Unstructured {
	cluster := newCNPGCluster(OwnedCNPGClusterName(nom))
	_ = unstructured.SetNestedSlice(cluster.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	return cluster
}

// rebuildParent is a NominatimInstance that owns its CNPG Cluster, i.e. the only mode in which
// Rebuild drops and recreates the Database CR.
func rebuildParent(name string) *nominatimv1alpha1.NominatimInstance {
	parent := baseNominatim(name)
	instances := int32(1)
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		Cluster: &nominatimv1alpha1.DatabaseClusterCreate{Instances: &instances},
	}
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode:                 nominatimv1alpha1.DatabaseModeClusterManaged,
		ClusterName:          OwnedCNPGClusterName(parent),
		ConnectionSecretName: CNPGAppSecretName(OwnedCNPGClusterName(parent)),
	}
	return parent
}

func rebuildOp(name string, parent *nominatimv1alpha1.NominatimInstance, annotations map[string]string) *nominatimv1alpha1.NominatimOperation {
	return &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   parent.Namespace,
			Annotations: annotations,
		},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationRebuild,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name},
			Regions:              []string{"europe/monaco"},
		},
	}
}

// pendingResetAnnotations is the handshake state an Operation carries between the drop and
// the moment the replacement Database reports applied=true.
func pendingResetAnnotations(prevUID string) map[string]string {
	return map[string]string{
		annotationRebuildDBReset:   rebuildDBResetPending,
		annotationRebuildDBPrevUID: prevUID,
	}
}

func cnpgAppSecret(parent *nominatimv1alpha1.NominatimInstance) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      parent.Status.Database.ConnectionSecretName,
			Namespace: parent.Namespace,
		},
		Data: map[string][]byte{"uri": []byte("postgresql://n:p@h/db")},
	}
}

func getOwnedCNPGDatabase(t *testing.T, c client.Client, parent *nominatimv1alpha1.NominatimInstance) *unstructured.Unstructured {
	t.Helper()
	db := &unstructured.Unstructured{}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	key := types.NamespacedName{Name: OwnedCNPGDatabaseName(parent), Namespace: parent.Namespace}
	if err := c.Get(context.Background(), key, db); err != nil {
		t.Fatalf("get owned CNPG Database: %v", err)
	}
	return db
}

func getOperation(t *testing.T, c client.Client, op *nominatimv1alpha1.NominatimOperation) *nominatimv1alpha1.NominatimOperation {
	t.Helper()
	got := &nominatimv1alpha1.NominatimOperation{}
	key := types.NamespacedName{Name: op.Name, Namespace: op.Namespace}
	if err := c.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get NominatimOperation: %v", err)
	}
	return got
}

func isCNPGDatabase(obj client.Object) bool {
	u, ok := obj.(*unstructured.Unstructured)
	return ok && u.GroupVersionKind() == CNPGDatabaseGVK
}

func cnpgDatabaseNotFound(name string) error {
	return apierrors.NewNotFound(schema.GroupResource{
		Group:    CNPGDatabaseGVK.Group,
		Resource: "databases",
	}, name)
}

// failDatabaseGetAfter makes the (n+1)-th and later Get of a CNPG Database return err,
// which is how the drop/recreate handshake observes the object disappearing mid-pass.
func failDatabaseGetAfter(n int, err error) interceptor.Funcs {
	seen := 0
	return interceptor.Funcs{
		Get: func(
			ctx context.Context,
			c client.WithWatch,
			key client.ObjectKey,
			obj client.Object,
			opts ...client.GetOption,
		) error {
			if isCNPGDatabase(obj) {
				seen++
				if seen > n {
					return err
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}
}

// failOperationUpdate breaks only the annotation handshake write, leaving CNPG Database
// writes intact.
func failOperationUpdate(err error) interceptor.Funcs {
	return interceptor.Funcs{
		Update: func(
			ctx context.Context,
			c client.WithWatch,
			obj client.Object,
			opts ...client.UpdateOption,
		) error {
			if _, ok := obj.(*nominatimv1alpha1.NominatimOperation); ok {
				return err
			}
			return c.Update(ctx, obj, opts...)
		},
	}
}

func TestEnsureRebuildDatabaseReset_SkipsNonRebuild(t *testing.T) {
	r := &NominatimOperationReconciler{}
	op := &nominatimv1alpha1.NominatimOperation{
		Spec: nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationBootstrap},
	}
	ready, err := r.ensureRebuildDatabaseReset(context.Background(), op, baseNominatim("skip"))
	if err != nil || !ready {
		t.Fatalf("ready=%v err=%v want ready=true", ready, err)
	}
}

func TestEnsureRebuildDatabaseReset_SkipsNonOwnedCluster(t *testing.T) {
	r := &NominatimOperationReconciler{}
	op := &nominatimv1alpha1.NominatimOperation{
		Spec: nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationRebuild},
	}
	parent := baseNominatim("secret-mode")
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{Name: "ext"},
	}
	parent.Status.Database.Mode = nominatimv1alpha1.DatabaseModeConnectionSecret
	ready, err := r.ensureRebuildDatabaseReset(context.Background(), op, parent)
	if err != nil || !ready {
		t.Fatalf("ready=%v err=%v want ready=true for connection-secret mode", ready, err)
	}
}

func TestEnsureRebuildDatabaseReset_DeletesThenRecreates(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("ri-reset")
	oldDB := newOwnedCNPGDatabase(parent, rebuildTestUIDOld, true)
	op := rebuildOp("ri-op", parent, nil)

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(parent, op, oldDB, cnpgAppSecret(parent), readyOwnedCNPGCluster(parent)).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	// Pass 1: delete existing Database, not ready yet.
	ready, err := r.ensureRebuildDatabaseReset(context.Background(), op, parent)
	if err != nil {
		t.Fatalf("pass1: %v", err)
	}
	if ready {
		t.Fatal("pass1: expected ready=false after requesting delete")
	}
	gotOp := getOperation(t, c, op)
	if gotOp.Annotations[annotationRebuildDBPrevUID] != rebuildTestUIDOld {
		t.Fatalf("prev uid annotation=%q", gotOp.Annotations[annotationRebuildDBPrevUID])
	}
	db := &unstructured.Unstructured{}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	dbKey := types.NamespacedName{Name: OwnedCNPGDatabaseName(parent), Namespace: parent.Namespace}
	err = c.Get(context.Background(), dbKey, db)
	if err == nil {
		t.Fatal("expected Database deleted after pass1")
	}
	if client.IgnoreNotFound(err) != nil {
		t.Fatalf("get database: %v", err)
	}

	// Pass 2: recreate Database CR (status.applied still false).
	op = gotOp
	ready, err = r.ensureRebuildDatabaseReset(context.Background(), op, parent)
	if err != nil {
		t.Fatalf("pass2: %v", err)
	}
	if ready {
		t.Fatal("pass2: expected ready=false until applied=true")
	}
	db = getOwnedCNPGDatabase(t, c, parent)
	reclaim, _, _ := unstructured.NestedString(db.Object, "spec", "databaseReclaimPolicy")
	if reclaim != cnpgDatabaseReclaimDelete {
		t.Fatalf("reclaim=%q want %q", reclaim, cnpgDatabaseReclaimDelete)
	}

	// Simulate CNPG applying extensions on the new Database.
	_ = unstructured.SetNestedField(db.Object, true, "status", "applied")
	db.SetUID(rebuildTestUIDNew)
	if err := c.Update(context.Background(), db); err != nil {
		t.Fatalf("mark applied: %v", err)
	}

	// Pass 3: applied with new UID → done.
	op = getOperation(t, c, op)
	ready, err = r.ensureRebuildDatabaseReset(context.Background(), op, parent)
	if err != nil || !ready {
		t.Fatalf("pass3: ready=%v err=%v", ready, err)
	}
	if got := getOperation(t, c, op); got.Annotations[annotationRebuildDBReset] != rebuildDBResetDone {
		t.Fatalf("reset annotation=%q want %q", got.Annotations[annotationRebuildDBReset], rebuildDBResetDone)
	}

	// Pass 4: idempotent once done.
	ready, err = r.ensureRebuildDatabaseReset(context.Background(), op, parent)
	if err != nil || !ready {
		t.Fatalf("pass4: ready=%v err=%v", ready, err)
	}
}

// A Rebuild on a parent whose Database CR was never created still has to run the
// handshake, recording "none" so the recreate branch is not mistaken for the drop branch.
func TestEnsureRebuildDatabaseReset_FirstPassRecordsNoneWhenDatabaseMissing(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("ri-absent")
	op := rebuildOp("ri-absent-op", parent, nil)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(parent, op).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	ready, err := r.ensureRebuildDatabaseReset(context.Background(), op, parent)
	if err != nil {
		t.Fatalf("ensureRebuildDatabaseReset: %v", err)
	}
	if ready {
		t.Fatal("expected ready=false on the first pass")
	}
	got := getOperation(t, c, op)
	if got.Annotations[annotationRebuildDBPrevUID] != rebuildDBPrevUIDNone {
		t.Fatalf("prev uid annotation=%q want %q",
			got.Annotations[annotationRebuildDBPrevUID], rebuildDBPrevUIDNone)
	}
	if got.Annotations[annotationRebuildDBReset] != rebuildDBResetPending {
		t.Fatalf("reset annotation=%q want %q",
			got.Annotations[annotationRebuildDBReset], rebuildDBResetPending)
	}
}

// prevUID="none" means there was nothing to drop, so the same-UID guard must not apply
// and the freshly created Database can arm the Job as soon as it reports applied=true.
func TestEnsureRebuildDatabaseReset_ReadyWhenPrevUIDNoneAndApplied(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("ri-none-applied")
	op := rebuildOp("ri-none-applied-op", parent, pendingResetAnnotations(rebuildDBPrevUIDNone))
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(parent, op, newOwnedCNPGDatabase(parent, rebuildTestUIDFresh, true)).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	ready, err := r.ensureRebuildDatabaseReset(context.Background(), op, parent)
	if err != nil || !ready {
		t.Fatalf("ready=%v err=%v want ready=true", ready, err)
	}
	if got := getOperation(t, c, op); got.Annotations[annotationRebuildDBReset] != rebuildDBResetDone {
		t.Fatalf("reset annotation=%q want %q", got.Annotations[annotationRebuildDBReset], rebuildDBResetDone)
	}
}

// The stale-object guard: CNPG has not finished the drop yet, so the Database still carries
// the recorded UID and the Job must stay unarmed even though status.applied is true.
func TestEnsureRebuildDatabaseReset_WaitsWhileUIDUnchanged(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("ri-same-uid")
	op := rebuildOp("ri-same-uid-op", parent, pendingResetAnnotations(rebuildTestUIDOld))
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(parent, op, newOwnedCNPGDatabase(parent, rebuildTestUIDOld, true)).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	ready, err := r.ensureRebuildDatabaseReset(context.Background(), op, parent)
	if err != nil {
		t.Fatalf("ensureRebuildDatabaseReset: %v", err)
	}
	if ready {
		t.Fatal("expected ready=false while the Database still has the recorded UID")
	}
	if got := getOperation(t, c, op); got.Annotations[annotationRebuildDBReset] != rebuildDBResetPending {
		t.Fatalf("reset annotation=%q want %q",
			got.Annotations[annotationRebuildDBReset], rebuildDBResetPending)
	}
}

func TestEnsureRebuildDatabaseReset_WaitsUntilReplacementApplied(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(db *unstructured.Unstructured)
		wantErr bool
	}{
		{
			name:   "applied false",
			mutate: func(db *unstructured.Unstructured) {},
		},
		{
			name: "applied absent",
			mutate: func(db *unstructured.Unstructured) {
				unstructured.RemoveNestedField(db.Object, "status", "applied")
			},
		},
		{
			name: "applied not a bool",
			mutate: func(db *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(db.Object, "yes", "status", "applied")
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := testScheme(t)
			parent := rebuildParent("ri-pending")
			op := rebuildOp("ri-pending-op", parent, pendingResetAnnotations(rebuildTestUIDOld))
			db := newOwnedCNPGDatabase(parent, rebuildTestUIDNew, false)
			tc.mutate(db)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(parent, op, db).Build()
			r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

			ready, err := r.ensureRebuildDatabaseReset(context.Background(), op, parent)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error for a non-boolean status.applied")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ensureRebuildDatabaseReset: %v", err)
			}
			if ready {
				t.Fatal("expected ready=false until the replacement reports applied=true")
			}
		})
	}
}

// Falls back to the conventional owned Cluster name when status has not caught up, so a
// Rebuild that races cluster-create still recreates the Database against the right Cluster.
func TestEnsureRebuildDatabaseReset_RecreateFallsBackToOwnedClusterName(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("ri-fallback")
	parent.Status.Database.ClusterName = ""
	op := rebuildOp("ri-fallback-op", parent, pendingResetAnnotations(rebuildDBPrevUIDNone))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(parent, op).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	ready, err := r.ensureRebuildDatabaseReset(context.Background(), op, parent)
	if err != nil {
		t.Fatalf("ensureRebuildDatabaseReset: %v", err)
	}
	if ready {
		t.Fatal("expected ready=false right after recreating the Database")
	}
	db := getOwnedCNPGDatabase(t, c, parent)
	cluster, _, _ := unstructured.NestedString(db.Object, "spec", "cluster", "name")
	if cluster != OwnedCNPGClusterName(parent) {
		t.Fatalf("spec.cluster.name=%q want %q", cluster, OwnedCNPGClusterName(parent))
	}
}

// Records the UID and stays not-ready when the Database vanishes between the reclaim patch
// and the delete, so a concurrent delete cannot be mistaken for a finished handshake.
func TestEnsureRebuildDatabaseReset_FirstPassDatabaseVanishesAfterReclaim(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("ri-vanish")
	op := rebuildOp("ri-vanish-op", parent, nil)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(parent, op, newOwnedCNPGDatabase(parent, rebuildTestUIDOld, true)).
		WithInterceptorFuncs(failDatabaseGetAfter(1, cnpgDatabaseNotFound(OwnedCNPGDatabaseName(parent)))).
		Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	ready, err := r.ensureRebuildDatabaseReset(context.Background(), op, parent)
	if err != nil {
		t.Fatalf("ensureRebuildDatabaseReset: %v", err)
	}
	if ready {
		t.Fatal("expected ready=false after the Database vanished")
	}
	if got := getOperation(t, c, op); got.Annotations[annotationRebuildDBPrevUID] != rebuildTestUIDOld {
		t.Fatalf("prev uid annotation=%q want %q", got.Annotations[annotationRebuildDBPrevUID], rebuildTestUIDOld)
	}
}

func TestEnsureRebuildDatabaseReset_SurfacesAPIErrors(t *testing.T) {
	parent := rebuildParent("ri-errors")
	dbName := OwnedCNPGDatabaseName(parent)

	cases := []struct {
		name        string
		annotations map[string]string
		withDB      bool
		retainDB    bool
		funcs       interceptor.Funcs
		wantMessage string
	}{
		{
			name:        "first pass get",
			withDB:      true,
			funcs:       failDatabaseGetAfter(0, errInjected),
			wantMessage: errInjected.Error(),
		},
		{
			name:     "first pass reclaim patch",
			withDB:   true,
			retainDB: true,
			funcs: interceptor.Funcs{
				Update: func(
					ctx context.Context,
					c client.WithWatch,
					obj client.Object,
					opts ...client.UpdateOption,
				) error {
					if isCNPGDatabase(obj) {
						return errInjected
					}
					return c.Update(ctx, obj, opts...)
				},
			},
			wantMessage: errInjected.Error(),
		},
		{
			name:        "first pass re-get",
			withDB:      true,
			funcs:       failDatabaseGetAfter(1, errInjected),
			wantMessage: errInjected.Error(),
		},
		{
			name:   "first pass delete",
			withDB: true,
			funcs: interceptor.Funcs{
				Delete: func(
					ctx context.Context,
					c client.WithWatch,
					obj client.Object,
					opts ...client.DeleteOption,
				) error {
					return errInjected
				},
			},
			wantMessage: "delete owned CNPG Database",
		},
		{
			name:        "first pass patch with no database to drop",
			funcs:       failOperationUpdate(errInjected),
			wantMessage: errInjected.Error(),
		},
		{
			name:        "first pass patch after delete",
			withDB:      true,
			funcs:       failOperationUpdate(errInjected),
			wantMessage: errInjected.Error(),
		},
		{
			name:   "first pass patch after the database vanished",
			withDB: true,
			funcs: interceptor.Funcs{
				Get:    failDatabaseGetAfter(1, cnpgDatabaseNotFound(dbName)).Get,
				Update: failOperationUpdate(errInjected).Update,
			},
			wantMessage: errInjected.Error(),
		},
		{
			name:        "pending pass get",
			annotations: pendingResetAnnotations(rebuildTestUIDOld),
			withDB:      true,
			funcs:       failDatabaseGetAfter(0, errInjected),
			wantMessage: errInjected.Error(),
		},
		{
			name:        "recreate",
			annotations: pendingResetAnnotations(rebuildTestUIDOld),
			funcs: interceptor.Funcs{
				Create: func(
					ctx context.Context,
					c client.WithWatch,
					obj client.Object,
					opts ...client.CreateOption,
				) error {
					return errInjected
				},
			},
			wantMessage: "reconcile owned CNPG Database",
		},
		{
			name:        "done annotation patch",
			annotations: pendingResetAnnotations(rebuildTestUIDOld),
			withDB:      true,
			funcs:       failOperationUpdate(errInjected),
			wantMessage: errInjected.Error(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := testScheme(t)
			op := rebuildOp("ri-errors-op", parent, tc.annotations)
			objs := []client.Object{parent, op}
			if tc.withDB {
				// A new UID means the "done annotation patch" case reaches the final patch.
				db := newOwnedCNPGDatabase(parent, rebuildTestUIDNew, true)
				if tc.retainDB {
					_ = unstructured.SetNestedField(db.Object, "retain", "spec", "databaseReclaimPolicy")
				}
				objs = append(objs, db)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(objs...).WithInterceptorFuncs(tc.funcs).Build()
			r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

			ready, err := r.ensureRebuildDatabaseReset(context.Background(), op, parent)
			if err == nil {
				t.Fatalf("expected an error for %s (database %q)", tc.name, dbName)
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("err=%v want it to mention %q", err, tc.wantMessage)
			}
			if ready {
				t.Fatal("expected ready=false when the handshake errors")
			}
		})
	}
}

func TestEnsureDatabaseReclaimDelete_NoopWhenPolicyAlreadyDelete(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("rc-noop")
	db := newOwnedCNPGDatabase(parent, "uid-1", true)
	updates := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(db).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(
				ctx context.Context,
				cl client.WithWatch,
				obj client.Object,
				opts ...client.UpdateOption,
			) error {
				updates++
				return cl.Update(ctx, obj, opts...)
			},
		}).Build()

	if err := ensureDatabaseReclaimDelete(context.Background(), c, db); err != nil {
		t.Fatalf("ensureDatabaseReclaimDelete: %v", err)
	}
	if updates != 0 {
		t.Fatalf("updates=%d want 0 when databaseReclaimPolicy is already %q", updates, cnpgDatabaseReclaimDelete)
	}
}

// A Database left on "retain" (hand-edited, or created by an older operator) must be
// switched to "delete" or CNPG would keep the Postgres database across the Rebuild.
func TestEnsureDatabaseReclaimDelete_PatchesRetainPolicy(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("rc-retain")
	db := newOwnedCNPGDatabase(parent, "uid-1", true)
	_ = unstructured.SetNestedField(db.Object, "retain", "spec", "databaseReclaimPolicy")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(db).Build()

	if err := ensureDatabaseReclaimDelete(context.Background(), c, db); err != nil {
		t.Fatalf("ensureDatabaseReclaimDelete: %v", err)
	}
	got := getOwnedCNPGDatabase(t, c, parent)
	reclaim, _, _ := unstructured.NestedString(got.Object, "spec", "databaseReclaimPolicy")
	if reclaim != cnpgDatabaseReclaimDelete {
		t.Fatalf("databaseReclaimPolicy=%q want %q", reclaim, cnpgDatabaseReclaimDelete)
	}
}

func TestEnsureDatabaseReclaimDelete_SurfacesAPIErrors(t *testing.T) {
	cases := map[string]interceptor.Funcs{
		"get": failDatabaseGetAfter(0, errInjected),
		"update": {
			Update: func(
				ctx context.Context,
				c client.WithWatch,
				obj client.Object,
				opts ...client.UpdateOption,
			) error {
				return errInjected
			},
		},
	}
	for name, funcs := range cases {
		t.Run(name, func(t *testing.T) {
			scheme := testScheme(t)
			parent := rebuildParent("rc-" + name)
			db := newOwnedCNPGDatabase(parent, "uid-1", true)
			_ = unstructured.SetNestedField(db.Object, "retain", "spec", "databaseReclaimPolicy")
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(db).
				WithInterceptorFuncs(funcs).Build()

			if err := ensureDatabaseReclaimDelete(context.Background(), c, db); !errors.Is(err, errInjected) {
				t.Fatalf("err=%v want %v", err, errInjected)
			}
		})
	}
}

// A Database whose spec is not an object cannot be patched, and that must surface as an
// error rather than silently leaving the reclaim policy alone before the delete.
func TestEnsureDatabaseReclaimDelete_RejectsUnpatchableSpec(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("rc-malformed")
	db := newOwnedCNPGDatabase(parent, "uid-1", true)
	db.Object["spec"] = "not-an-object"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(db).Build()

	if err := ensureDatabaseReclaimDelete(context.Background(), c, db); err == nil {
		t.Fatal("expected an error when spec is not a map")
	}
}

func TestPatchRebuildResetAnnotations_SurfacesGetError(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("patch-missing")
	op := rebuildOp("patch-missing-op", parent, nil)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(parent).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	err := r.patchRebuildResetAnnotations(context.Background(), op, rebuildDBResetPending, rebuildTestUIDOld)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("err=%v want NotFound for an Operation that no longer exists", err)
	}
}

func TestCNPGOwnedDatabaseApplied(t *testing.T) {
	parent := rebuildParent("applied")
	dbName := OwnedCNPGDatabaseName(parent)

	cases := []struct {
		name    string
		db      *unstructured.Unstructured
		funcs   interceptor.Funcs
		want    bool
		wantErr bool
	}{
		{name: "absent"},
		{name: "applied", db: newOwnedCNPGDatabase(parent, "uid-1", true), want: true},
		{name: "not applied", db: newOwnedCNPGDatabase(parent, "uid-1", false)},
		{
			name: "status absent",
			db: func() *unstructured.Unstructured {
				db := newOwnedCNPGDatabase(parent, "uid-1", true)
				unstructured.RemoveNestedField(db.Object, "status", "applied")
				return db
			}(),
		},
		{
			name: "applied not a bool",
			db: func() *unstructured.Unstructured {
				db := newOwnedCNPGDatabase(parent, "uid-1", true)
				_ = unstructured.SetNestedField(db.Object, "yes", "status", "applied")
				return db
			}(),
			wantErr: true,
		},
		{
			name:    "get error",
			db:      newOwnedCNPGDatabase(parent, "uid-1", true),
			funcs:   failDatabaseGetAfter(0, errInjected),
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := testScheme(t)
			builder := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(tc.funcs)
			if tc.db != nil {
				builder = builder.WithObjects(tc.db)
			}
			c := builder.Build()

			got, err := cnpgOwnedDatabaseApplied(context.Background(), c, parent.Namespace, dbName)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("applied=%v want %v", got, tc.want)
			}
		})
	}
}

func TestCNPGClusterReadyForJobs(t *testing.T) {
	appliedDB := func(parent *nominatimv1alpha1.NominatimInstance) *unstructured.Unstructured {
		return newOwnedCNPGDatabase(parent, "uid-1", true)
	}

	cases := []struct {
		name    string
		parent  func() *nominatimv1alpha1.NominatimInstance
		objects func(parent *nominatimv1alpha1.NominatimInstance) []client.Object
		want    bool
		wantErr bool
	}{
		{
			name: "degraded skips the wait",
			parent: func() *nominatimv1alpha1.NominatimInstance {
				parent := rebuildParent("ready-degraded")
				parent.Status.Database.Degraded = true
				return parent
			},
			want: true,
		},
		{
			name:   "owned cluster ready and database applied",
			parent: func() *nominatimv1alpha1.NominatimInstance { return rebuildParent("ready-owned") },
			objects: func(parent *nominatimv1alpha1.NominatimInstance) []client.Object {
				return []client.Object{readyOwnedCNPGCluster(parent), appliedDB(parent)}
			},
			want: true,
		},
		{
			name: "owned cluster name falls back to the conventional name",
			parent: func() *nominatimv1alpha1.NominatimInstance {
				parent := rebuildParent("ready-fallback")
				parent.Status.Database.ClusterName = ""
				return parent
			},
			objects: func(parent *nominatimv1alpha1.NominatimInstance) []client.Object {
				return []client.Object{readyOwnedCNPGCluster(parent), appliedDB(parent)}
			},
			want: true,
		},
		{
			name: "attached cluster ref without a name waits",
			parent: func() *nominatimv1alpha1.NominatimInstance {
				parent := baseNominatim("ready-ref")
				parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
					ClusterRef: &nominatimv1alpha1.DatabaseClusterRef{Name: "external"},
				}
				return parent
			},
		},
		{
			name:   "no cluster to wait on",
			parent: func() *nominatimv1alpha1.NominatimInstance { return baseNominatim("ready-none") },
			want:   true,
		},
		{
			name:   "missing cluster waits",
			parent: func() *nominatimv1alpha1.NominatimInstance { return rebuildParent("ready-missing") },
		},
		{
			name:   "cluster without conditions waits",
			parent: func() *nominatimv1alpha1.NominatimInstance { return rebuildParent("ready-nocond") },
			objects: func(parent *nominatimv1alpha1.NominatimInstance) []client.Object {
				cluster := readyOwnedCNPGCluster(parent)
				unstructured.RemoveNestedField(cluster.Object, "status", "conditions")
				return []client.Object{cluster, appliedDB(parent)}
			},
		},
		{
			name:   "cluster not ready waits",
			parent: func() *nominatimv1alpha1.NominatimInstance { return rebuildParent("ready-notready") },
			objects: func(parent *nominatimv1alpha1.NominatimInstance) []client.Object {
				cluster := readyOwnedCNPGCluster(parent)
				_ = unstructured.SetNestedSlice(cluster.Object, []interface{}{
					"not-a-condition",
					map[string]interface{}{"type": "Ready", "status": "False"},
				}, "status", "conditions")
				return []client.Object{cluster, appliedDB(parent)}
			},
		},
		{
			name: "attached cluster ready needs no Database CR",
			parent: func() *nominatimv1alpha1.NominatimInstance {
				parent := baseNominatim("ready-attached")
				parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
					ClusterRef: &nominatimv1alpha1.DatabaseClusterRef{Name: "external"},
				}
				parent.Status.Database.ClusterName = "external"
				return parent
			},
			objects: func(parent *nominatimv1alpha1.NominatimInstance) []client.Object {
				return []client.Object{newCNPGCluster("external")}
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := testScheme(t)
			parent := tc.parent()
			objs := []client.Object{parent}
			if tc.objects != nil {
				objs = append(objs, tc.objects(parent)...)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
			r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

			got, err := r.cnpgClusterReadyForJobs(context.Background(), parent)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("ready=%v want %v", got, tc.want)
			}
		})
	}
}

func TestCNPGClusterReadyForJobs_SurfacesClusterGetError(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("ready-geterr")
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(parent, readyOwnedCNPGCluster(parent)).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(
				ctx context.Context,
				cl client.WithWatch,
				key client.ObjectKey,
				obj client.Object,
				opts ...client.GetOption,
			) error {
				if u, ok := obj.(*unstructured.Unstructured); ok && u.GroupVersionKind() == CNPGClusterGVK {
					return errInjected
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	ready, err := r.cnpgClusterReadyForJobs(context.Background(), parent)
	if !errors.Is(err, errInjected) {
		t.Fatalf("err=%v want %v", err, errInjected)
	}
	if ready {
		t.Fatal("expected ready=false when the Cluster read fails")
	}
}

func TestReconcileRebuild_WaitsForDatabaseResetBeforeJob(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("ri-job")
	oldDB := newOwnedCNPGDatabase(parent, rebuildTestUIDOld, true)
	op := rebuildOp("ri-job-op", parent, nil)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(parent, &nominatimv1alpha1.NominatimOperation{}).
		WithObjects(parent, op, oldDB, cnpgAppSecret(parent), readyOwnedCNPGCluster(parent)).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: op.Name, Namespace: parent.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile1: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected requeue while resetting database, got %#v", res)
	}
	job := &batchv1.Job{}
	err = c.Get(context.Background(), types.NamespacedName{Name: op.Name, Namespace: parent.Namespace}, job)
	if err == nil {
		t.Fatal("Job must not exist before Database reset completes")
	}
}

// activeOperationRefs must be registered before the Job exists so the parent can scale the
// API to zero and CNPG can DROP DATABASE under reclaim=delete.
func TestReconcileRebuild_RegistersActiveRefBeforeJob(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("ri-ref")
	op := rebuildOp("ri-ref-op", parent, nil)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(parent, &nominatimv1alpha1.NominatimOperation{}).
		WithObjects(parent, op, newOwnedCNPGDatabase(parent, rebuildTestUIDOld, true),
			cnpgAppSecret(parent), readyOwnedCNPGCluster(parent)).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: op.Name, Namespace: parent.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &nominatimv1alpha1.NominatimInstance{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, got); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if len(got.Status.ActiveOperationRefs) != 1 || got.Status.ActiveOperationRefs[0].Name != op.Name {
		t.Fatalf("activeOperationRefs=%v want [%s] before the Job is armed", got.Status.ActiveOperationRefs, op.Name)
	}
}

// DROP DATABASE cannot proceed while the API Deployment still has pods.
func TestEnsureRebuildDatabaseReset_WaitsForAPIQuiesced(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("ri-api")
	op := rebuildOp("ri-api-op", parent, nil)
	replicas := int32(1)
	api := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: APIName(parent), Namespace: parent.Namespace},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&nominatimv1alpha1.NominatimOperation{}).
		WithObjects(parent, op, api, newOwnedCNPGDatabase(parent, rebuildTestUIDOld, true)).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	ready, err := r.ensureRebuildDatabaseReset(context.Background(), op, parent)
	if err != nil {
		t.Fatalf("ensureRebuildDatabaseReset: %v", err)
	}
	if ready {
		t.Fatal("expected ready=false while the API Deployment still has ReadyReplicas")
	}
	gotAPI := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: APIName(parent), Namespace: parent.Namespace}, gotAPI); err != nil {
		t.Fatalf("get API: %v", err)
	}
	if gotAPI.Spec.Replicas == nil || *gotAPI.Spec.Replicas != 0 {
		t.Fatalf("API replicas=%v want 0 (Operation must scale down before DROP DATABASE)", gotAPI.Spec.Replicas)
	}
	db := &unstructured.Unstructured{}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: OwnedCNPGDatabaseName(parent), Namespace: parent.Namespace,
	}, db); err != nil {
		t.Fatalf("Database should still exist until the API is quiesced: %v", err)
	}
	if string(db.GetUID()) != rebuildTestUIDOld {
		t.Fatalf("uid=%q want %q (delete must wait for API drain)", db.GetUID(), rebuildTestUIDOld)
	}
}

// The NominatimInstance reconciler must not CreateOrUpdate the owned Database while a Rebuild is
// mid drop/recreate, or it will fight the Operation's Delete under the pre-Rebuild UID.
func TestReconcileOwnedCNPGDatabase_SkipsWhileRebuildResetPending(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("ri-skip")
	op := rebuildOp("ri-skip-op", parent, pendingResetAnnotations(rebuildTestUIDOld))
	op.Status.Phase = nominatimv1alpha1.NominatimOperationPhasePending
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(parent, op).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileOwnedCNPGDatabase(context.Background(), parent, OwnedCNPGClusterName(parent)); err != nil {
		t.Fatalf("reconcileOwnedCNPGDatabase: %v", err)
	}
	db := &unstructured.Unstructured{}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	err := c.Get(context.Background(), types.NamespacedName{
		Name: OwnedCNPGDatabaseName(parent), Namespace: parent.Namespace,
	}, db)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected no Database recreate while reset=pending, got err=%v", err)
	}
}

// The Job may only be created once the Operation records reset=done, i.e. after the
// replacement Database exists under a new UID with status.applied=true.
func TestReconcileRebuild_CreatesJobAfterDatabaseResetCompletes(t *testing.T) {
	scheme := testScheme(t)
	parent := rebuildParent("ri-armed")
	op := rebuildOp("ri-armed-op", parent, pendingResetAnnotations(rebuildTestUIDOld))
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(parent, op).
		WithObjects(parent, op, newOwnedCNPGDatabase(parent, rebuildTestUIDNew, true),
			cnpgAppSecret(parent), readyOwnedCNPGCluster(parent)).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: op.Name, Namespace: parent.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if got := getOperation(t, c, op); got.Annotations[annotationRebuildDBReset] != rebuildDBResetDone {
		t.Fatalf("reset annotation=%q want %q", got.Annotations[annotationRebuildDBReset], rebuildDBResetDone)
	}
	job := &batchv1.Job{}
	key := types.NamespacedName{Name: op.Name, Namespace: parent.Namespace}
	if err := c.Get(context.Background(), key, job); err != nil {
		t.Fatalf("expected the Rebuild Job once the Database reset is done: %v", err)
	}
	if !jobHasDatabaseDSN(job) {
		t.Fatal("Rebuild Job must carry NOMINATIM_DATABASE_DSN")
	}
}
