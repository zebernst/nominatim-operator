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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

func TestBlueGreenAdvance_HappyPath(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	in := blueGreenInput{
		Phase:           "",
		ActiveCluster:   "demo-pg",
		PendingCluster:  "",
		ImportSucceeded: false,
		PendingReady:    false,
		APICutoverReady: false,
		RollbackUntil:   nil,
		Now:             now,
		RollbackWindow:  time.Hour,
	}

	// Empty → Provisioning
	out, act := advanceBlueGreen(in)
	if out.Phase != nominatimv1alpha1.RebuildPhaseProvisioning || !act.EnsurePendingCluster {
		t.Fatalf("empty: phase=%s act=%+v", out.Phase, act)
	}

	in.Phase = out.Phase
	in.PendingCluster = "demo-pg-bg"
	out, act = advanceBlueGreen(in)
	if out.Phase != nominatimv1alpha1.RebuildPhaseProvisioning || !act.EnsurePendingCluster {
		t.Fatalf("still provisioning until ready: phase=%s act=%+v", out.Phase, act)
	}

	in.PendingReady = true
	out, act = advanceBlueGreen(in)
	if out.Phase != nominatimv1alpha1.RebuildPhaseImporting || !act.EnsureImportJob {
		t.Fatalf("ready pending → importing: phase=%s act=%+v", out.Phase, act)
	}

	in.Phase = out.Phase
	in.ImportSucceeded = true
	out, act = advanceBlueGreen(in)
	if out.Phase != nominatimv1alpha1.RebuildPhaseReadyToCutover || act.EnsureImportJob {
		t.Fatalf("import done → ready: phase=%s act=%+v", out.Phase, act)
	}

	in.Phase = out.Phase
	out, act = advanceBlueGreen(in)
	if out.Phase != nominatimv1alpha1.RebuildPhaseCutover || !act.CutoverAPI {
		t.Fatalf("ready → cutover: phase=%s act=%+v", out.Phase, act)
	}

	in.Phase = out.Phase
	in.APICutoverReady = true
	out, act = advanceBlueGreen(in)
	if out.Phase != nominatimv1alpha1.RebuildPhaseRollbackWindow || out.RollbackUntil == nil {
		t.Fatalf("cutover ready → rollback window: phase=%s until=%v", out.Phase, out.RollbackUntil)
	}
	if !out.RollbackUntil.Time.Equal(now.Add(time.Hour)) {
		t.Fatalf("rollback until=%v want %v", out.RollbackUntil.Time, now.Add(time.Hour))
	}

	in.Phase = out.Phase
	in.RollbackUntil = out.RollbackUntil
	in.Now = now.Add(30 * time.Minute)
	out, act = advanceBlueGreen(in)
	if out.Phase != nominatimv1alpha1.RebuildPhaseRollbackWindow || act.GarbageCollectBlue {
		t.Fatalf("inside window: phase=%s act=%+v", out.Phase, act)
	}

	in.Now = now.Add(2 * time.Hour)
	out, act = advanceBlueGreen(in)
	if out.Phase != nominatimv1alpha1.RebuildPhaseGarbageCollect || !act.GarbageCollectBlue {
		t.Fatalf("after window → GC: phase=%s act=%+v", out.Phase, act)
	}

	in.Phase = out.Phase
	in.BlueGone = true
	out, act = advanceBlueGreen(in)
	if out.Phase != nominatimv1alpha1.RebuildPhaseSucceeded || act.GarbageCollectBlue {
		t.Fatalf("blue gone → succeeded: phase=%s act=%+v", out.Phase, act)
	}
}

func TestBlueGreenAdvance_FailBeforeCutoverGCsGreen(t *testing.T) {
	t.Parallel()
	out, act := advanceBlueGreen(blueGreenInput{
		Phase:   nominatimv1alpha1.RebuildPhaseImporting,
		Failed:  true,
		Message: "job failed",
		Now:     time.Now().UTC(),
	})
	if out.Phase != nominatimv1alpha1.RebuildPhaseFailed || !act.GarbageCollectGreen {
		t.Fatalf("fail pre-cutover: phase=%s act=%+v", out.Phase, act)
	}
	if act.GarbageCollectBlue || act.CutoverAPI {
		t.Fatalf("must not GC blue or cut over on pre-cutover failure: %+v", act)
	}
}

func TestBlueGreenResourceNames(t *testing.T) {
	t.Parallel()
	nom := &nominatimv1alpha1.NominatimInstance{}
	nom.Name = "demo"
	if got := BlueGreenClusterName(nom); got != "demo-pg-bg" {
		t.Fatalf("cluster=%q", got)
	}
	if got := BlueGreenProjectPVCName(nom); got != "demo-project-bg" {
		t.Fatalf("project=%q", got)
	}
	if got := BlueGreenFlatnodePVCName(nom); got != "demo-flatnode-bg" {
		t.Fatalf("flatnode=%q", got)
	}
}

func TestUsesBlueGreenRebuild(t *testing.T) {
	t.Parallel()
	nom := rebuildParent("bg")
	if usesBlueGreenRebuild(nom) {
		t.Fatal("default InPlace")
	}
	nom.Spec.Database.RebuildStrategy = nominatimv1alpha1.RebuildStrategyBlueGreen
	if !usesBlueGreenRebuild(nom) {
		t.Fatal("want BlueGreen")
	}
}

func TestBlueGreenRebuildAllowed(t *testing.T) {
	t.Parallel()
	owned := rebuildParent("owned")
	owned.Spec.Database.RebuildStrategy = nominatimv1alpha1.RebuildStrategyBlueGreen
	if !blueGreenRebuildAllowed(owned) {
		t.Fatal("owned cluster must allow BlueGreen")
	}
	secret := baseNominatim("secret")
	secret.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{Name: "ext"},
		RebuildStrategy:     nominatimv1alpha1.RebuildStrategyBlueGreen,
	}
	secret.Status.Database.Mode = nominatimv1alpha1.DatabaseModeConnectionSecret
	if blueGreenRebuildAllowed(secret) {
		t.Fatal("connectionSecretRef must not allow BlueGreen")
	}
	attached := baseNominatim("attached")
	attached.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef:      &nominatimv1alpha1.DatabaseClusterRef{Name: "external-pg"},
		RebuildStrategy: nominatimv1alpha1.RebuildStrategyBlueGreen,
	}
	attached.Status.Database.Mode = nominatimv1alpha1.DatabaseModeClusterAttached
	if blueGreenRebuildAllowed(attached) {
		t.Fatal("clusterRef must not allow BlueGreen")
	}
}

func TestBlueGreenAdvance_SetsRollbackUntilOnce(t *testing.T) {
	t.Parallel()
	fixed := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	out, _ := advanceBlueGreen(blueGreenInput{
		Phase:           nominatimv1alpha1.RebuildPhaseCutover,
		APICutoverReady: true,
		RollbackUntil:   &fixed,
		Now:             time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		RollbackWindow:  time.Hour,
	})
	if out.RollbackUntil == nil || !out.RollbackUntil.Equal(&fixed) {
		t.Fatalf("must preserve existing RollbackUntil, got %v", out.RollbackUntil)
	}
}

func TestApplyBlueGreenCutover(t *testing.T) {
	t.Parallel()
	parent := rebuildParent("cut")
	parent.Spec.Database.RebuildStrategy = nominatimv1alpha1.RebuildStrategyBlueGreen
	op := rebuildOp("op-cut", parent, nil)
	parent.Status.Rebuild = seedBlueGreenRebuildStatus(parent, op)
	parent.Status.Rebuild.Phase = nominatimv1alpha1.RebuildPhaseReadyToCutover

	applyBlueGreenCutover(parent)

	if parent.Status.Database.ClusterName != BlueGreenClusterName(parent) {
		t.Fatalf("cluster=%q want green", parent.Status.Database.ClusterName)
	}
	if parent.Status.Database.ConnectionSecretName != CNPGAppSecretName(BlueGreenClusterName(parent)) {
		t.Fatalf("secret=%q", parent.Status.Database.ConnectionSecretName)
	}
	if parent.Status.Rebuild.ActiveClusterName != BlueGreenClusterName(parent) {
		t.Fatalf("active=%q", parent.Status.Rebuild.ActiveClusterName)
	}
}

func TestBlueGreenRetiredClusterName(t *testing.T) {
	t.Parallel()
	parent := rebuildParent("retire")
	if got := blueGreenRetiredClusterName(parent); got != BlueGreenClusterName(parent) {
		t.Fatalf("before cutover retired=%q want bg (live is default)", got)
	}
	parent.Status.Database.ClusterName = BlueGreenClusterName(parent)
	if got := blueGreenRetiredClusterName(parent); got != OwnedCNPGClusterName(parent) {
		t.Fatalf("after cutover to bg retired=%q want default", got)
	}
}

func TestBlueGreenJobConnectionSecret(t *testing.T) {
	t.Parallel()
	parent := rebuildParent("jobsec")
	parent.Spec.Database.RebuildStrategy = nominatimv1alpha1.RebuildStrategyBlueGreen
	op := rebuildOp("op", parent, nil)
	parent.Status.Rebuild = seedBlueGreenRebuildStatus(parent, op)
	got := blueGreenJobConnectionSecret(parent)
	want := CNPGAppSecretName(BlueGreenClusterName(parent))
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEnsureBlueGreenPending_CreatesSiblingCluster(t *testing.T) {
	parent := rebuildParent("prov")
	parent.Spec.Database.RebuildStrategy = nominatimv1alpha1.RebuildStrategyBlueGreen
	parent.Spec.Project.Volume = nominatimv1alpha1.VolumeSource{
		VolumeClaimTemplate: &nominatimv1alpha1.VolumeClaimTemplate{
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		},
	}
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&nominatimv1alpha1.NominatimInstance{}).
		WithObjects(parent).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}
	op := rebuildOp("op-prov", parent, nil)

	ready, err := r.ensureBlueGreenPending(context.Background(), op, parent)
	if err != nil {
		t.Fatalf("ensureBlueGreenPending: %v", err)
	}
	if ready {
		t.Fatal("expected not ready until Cluster Ready + Secret + Database applied")
	}

	// Sibling Cluster must exist.
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: BlueGreenClusterName(parent), Namespace: parent.Namespace,
	}, cluster); err != nil {
		t.Fatalf("pending cluster: %v", err)
	}
	db := &unstructured.Unstructured{}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: CNPGDatabaseNameForCluster(BlueGreenClusterName(parent)), Namespace: parent.Namespace,
	}, db); err != nil {
		t.Fatalf("pending database: %v", err)
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name: BlueGreenProjectPVCName(parent), Namespace: parent.Namespace,
	}, pvc); err != nil {
		t.Fatalf("pending project PVC: %v", err)
	}

	got := &nominatimv1alpha1.NominatimInstance{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, got); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if got.Status.Rebuild == nil || got.Status.Rebuild.PendingClusterName != BlueGreenClusterName(parent) {
		t.Fatalf("status.rebuild=%+v", got.Status.Rebuild)
	}

	// Live Database must still be untouched (no InPlace drop).
	if got.Status.Database.ClusterName != OwnedCNPGClusterName(parent) {
		t.Fatalf("live cluster mutated early: %q", got.Status.Database.ClusterName)
	}
}

func TestReconcileBlueGreenAfterImport_CutoverAndGC(t *testing.T) {
	parent := rebuildParent("bg-flow")
	parent.Spec.Database.RebuildStrategy = nominatimv1alpha1.RebuildStrategyBlueGreen
	op := rebuildOp("op-flow", parent, nil)
	parent.Status.Rebuild = seedBlueGreenRebuildStatus(parent, op)
	parent.Status.Rebuild.Phase = nominatimv1alpha1.RebuildPhaseImporting

	blue := readyOwnedCNPGCluster(parent)
	greenName := BlueGreenClusterName(parent)
	green := newCNPGCluster(greenName)
	_ = unstructured.SetNestedSlice(green.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	greenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: CNPGAppSecretName(greenName), Namespace: parent.Namespace},
		Data:       map[string][]byte{"uri": []byte("postgres://g")},
	}
	blueSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: CNPGAppSecretName(OwnedCNPGClusterName(parent)), Namespace: parent.Namespace},
		Data:       map[string][]byte{"uri": []byte("postgres://b")},
	}

	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&nominatimv1alpha1.NominatimInstance{}, &nominatimv1alpha1.NominatimOperation{}).
		WithObjects(parent, op, blue, green, greenSecret, blueSecret).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	advance := func() {
		t.Helper()
		curOp := &nominatimv1alpha1.NominatimOperation{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: op.Name, Namespace: op.Namespace}, curOp); err != nil {
			t.Fatalf("get op: %v", err)
		}
		curParent := &nominatimv1alpha1.NominatimInstance{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, curParent); err != nil {
			t.Fatalf("get parent: %v", err)
		}
		if _, err := r.reconcileBlueGreenAfterImport(context.Background(), curOp, curParent); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}

	// Importing + succeeded → ReadyToCutover
	advance()
	got := &nominatimv1alpha1.NominatimInstance{}
	_ = c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, got)
	if got.Status.Rebuild.Phase != nominatimv1alpha1.RebuildPhaseReadyToCutover {
		t.Fatalf("phase=%s want ReadyToCutover", got.Status.Rebuild.Phase)
	}

	// ReadyToCutover → Cutover (flips status.database)
	advance()
	_ = c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, got)
	if got.Status.Database.ConnectionSecretName != CNPGAppSecretName(greenName) {
		t.Fatalf("cutover secret=%q want green", got.Status.Database.ConnectionSecretName)
	}
	if got.Status.Rebuild.Phase != nominatimv1alpha1.RebuildPhaseCutover {
		t.Fatalf("phase=%s want Cutover", got.Status.Rebuild.Phase)
	}

	// Cutover with APICutoverReady → RollbackWindow
	advance()
	_ = c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, got)
	if got.Status.Rebuild.Phase != nominatimv1alpha1.RebuildPhaseRollbackWindow {
		t.Fatalf("phase=%s want RollbackWindow", got.Status.Rebuild.Phase)
	}
	if got.Status.Rebuild.RollbackUntil == nil {
		t.Fatal("expected RollbackUntil")
	}

	// Inside window: blue remains
	advance()
	still := &unstructured.Unstructured{}
	still.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: OwnedCNPGClusterName(got), Namespace: got.Namespace}, still); err != nil {
		t.Fatalf("blue must remain during rollback window: %v", err)
	}

	// Expire window → GC blue
	_ = c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, got)
	past := metav1.NewTime(time.Now().UTC().Add(-time.Minute))
	got.Status.Rebuild.RollbackUntil = &past
	got.Status.Rebuild.Phase = nominatimv1alpha1.RebuildPhaseRollbackWindow
	if err := c.Status().Update(context.Background(), got); err != nil {
		t.Fatalf("expire window: %v", err)
	}
	advance()
	gone := &unstructured.Unstructured{}
	gone.SetGroupVersionKind(CNPGClusterGVK)
	err := c.Get(context.Background(), types.NamespacedName{Name: OwnedCNPGClusterName(got), Namespace: got.Namespace}, gone)
	if err == nil {
		t.Fatal("expected blue Cluster deleted after window")
	}

	// Blue gone → Succeeded
	advance()
	opFinal := &nominatimv1alpha1.NominatimOperation{}
	_ = c.Get(context.Background(), types.NamespacedName{Name: op.Name, Namespace: op.Namespace}, opFinal)
	if opFinal.Status.Phase != nominatimv1alpha1.NominatimOperationPhaseSucceeded {
		t.Fatalf("op phase=%s want Succeeded", opFinal.Status.Phase)
	}
}

func TestBlueGreenPendingReady(t *testing.T) {
	parent := rebuildParent("ready-chk")
	greenName := BlueGreenClusterName(parent)
	scheme := testScheme(t)
	r := &NominatimOperationReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme}

	ready, err := r.blueGreenPendingReady(context.Background(), parent.Namespace, greenName)
	if err != nil || ready {
		t.Fatalf("missing cluster: ready=%v err=%v", ready, err)
	}

	cluster := newCNPGCluster(greenName)
	cluster.SetNamespace(parent.Namespace)
	_ = unstructured.SetNestedSlice(cluster.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	db := &unstructured.Unstructured{}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	db.SetName(CNPGDatabaseNameForCluster(greenName))
	db.SetNamespace(parent.Namespace)
	_ = unstructured.SetNestedField(db.Object, true, "status", "applied")
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: CNPGAppSecretName(greenName), Namespace: parent.Namespace}}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, db, secret).Build()
	r.Client = c
	ready, err = r.blueGreenPendingReady(context.Background(), parent.Namespace, greenName)
	if err != nil || !ready {
		t.Fatalf("want ready: ready=%v err=%v", ready, err)
	}
}

func TestSeedBlueGreen_WhenLiveIsAlreadyBG(t *testing.T) {
	parent := rebuildParent("flip")
	parent.Status.Database.ClusterName = BlueGreenClusterName(parent)
	op := rebuildOp("op", parent, nil)
	st := seedBlueGreenRebuildStatus(parent, op)
	if st.PendingClusterName != OwnedCNPGClusterName(parent) {
		t.Fatalf("pending=%q want default name when live is -bg", st.PendingClusterName)
	}
	if st.ActiveClusterName != BlueGreenClusterName(parent) {
		t.Fatalf("active=%q", st.ActiveClusterName)
	}
}

func TestBuildOperationJob_BlueGreenUsesPendingSecretAndPVC(t *testing.T) {
	parent := rebuildParent("job-bg")
	parent.Spec.Database.RebuildStrategy = nominatimv1alpha1.RebuildStrategyBlueGreen
	op := rebuildOp("op-job", parent, nil)
	parent.Status.Rebuild = seedBlueGreenRebuildStatus(parent, op)

	job, err := buildOperationJob(op, parent, "staging", "worker:test", corev1.PullIfNotPresent)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	wantSecret := CNPGAppSecretName(BlueGreenClusterName(parent))
	foundDSN := false
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "NOMINATIM_DATABASE_DSN" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			if e.ValueFrom.SecretKeyRef.Name != wantSecret {
				t.Fatalf("DSN secret=%q want %q", e.ValueFrom.SecretKeyRef.Name, wantSecret)
			}
			foundDSN = true
		}
	}
	if !foundDSN {
		t.Fatal("missing NOMINATIM_DATABASE_DSN")
	}
	claim := job.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName
	if claim != BlueGreenProjectPVCName(parent) {
		t.Fatalf("project claim=%q want %q", claim, BlueGreenProjectPVCName(parent))
	}
}

func TestWaitForJobPrerequisites_BlueGreenWaitsThenArms(t *testing.T) {
	parent := rebuildParent("bg-wait")
	parent.Spec.Database.RebuildStrategy = nominatimv1alpha1.RebuildStrategyBlueGreen
	parent.Spec.Project.Volume = nominatimv1alpha1.VolumeSource{
		VolumeClaimTemplate: &nominatimv1alpha1.VolumeClaimTemplate{
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		},
	}
	blueSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: parent.Status.Database.ConnectionSecretName, Namespace: parent.Namespace},
		Data:       map[string][]byte{"uri": []byte("postgres://b")},
	}
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&nominatimv1alpha1.NominatimInstance{}).
		WithObjects(parent, blueSecret, readyOwnedCNPGCluster(parent)).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}
	op := rebuildOp("op-wait", parent, nil)

	res, wait, err := r.waitForJobPrerequisites(context.Background(), op, parent)
	if err != nil || !wait || res.RequeueAfter == 0 {
		t.Fatalf("want wait for pending: wait=%v res=%v err=%v", wait, res, err)
	}

	// Mark pending Ready + Secret + Database applied.
	_ = c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, parent)
	greenName := BlueGreenClusterName(parent)
	green := &unstructured.Unstructured{}
	green.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: greenName, Namespace: parent.Namespace}, green); err != nil {
		t.Fatalf("pending cluster: %v", err)
	}
	_ = unstructured.SetNestedSlice(green.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	if err := c.Status().Update(context.Background(), green); err != nil {
		// fake client may lack status for unstructured — Update instead
		if err := c.Update(context.Background(), green); err != nil {
			t.Fatalf("mark ready: %v", err)
		}
	}
	db := &unstructured.Unstructured{}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	_ = c.Get(context.Background(), types.NamespacedName{Name: CNPGDatabaseNameForCluster(greenName), Namespace: parent.Namespace}, db)
	_ = unstructured.SetNestedField(db.Object, true, "status", "applied")
	_ = c.Update(context.Background(), db)
	greenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: CNPGAppSecretName(greenName), Namespace: parent.Namespace},
		Data:       map[string][]byte{"uri": []byte("postgres://g")},
	}
	if err := c.Create(context.Background(), greenSecret); err != nil {
		t.Fatalf("green secret: %v", err)
	}

	_ = c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, parent)
	res, wait, err = r.waitForJobPrerequisites(context.Background(), op, parent)
	if err != nil || wait {
		t.Fatalf("want ready to arm Job: wait=%v res=%v err=%v", wait, res, err)
	}
}

func TestReconcile_BlueGreenRejectedForClusterRef(t *testing.T) {
	parent := baseNominatim("bg-reject")
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef:      &nominatimv1alpha1.DatabaseClusterRef{Name: "ext"},
		RebuildStrategy: nominatimv1alpha1.RebuildStrategyBlueGreen,
	}
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode:                 nominatimv1alpha1.DatabaseModeClusterAttached,
		ClusterName:          "ext",
		ConnectionSecretName: "ext-app",
	}
	parent.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ext-app", Namespace: "default"}, Data: map[string][]byte{"uri": []byte("x")}}
	op := rebuildOp("op-reject", parent, nil)
	op.Status.Phase = nominatimv1alpha1.NominatimOperationPhasePending
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&nominatimv1alpha1.NominatimInstance{}, &nominatimv1alpha1.NominatimOperation{}).
		WithObjects(parent, op, secret).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: op.Name, Namespace: op.Namespace}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &nominatimv1alpha1.NominatimOperation{}
	_ = c.Get(context.Background(), types.NamespacedName{Name: op.Name, Namespace: op.Namespace}, got)
	if got.Status.Phase != nominatimv1alpha1.NominatimOperationPhaseFailed {
		t.Fatalf("phase=%s want Failed", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, reasonUnsupportedRebuild) {
		t.Fatalf("message=%q", got.Status.Message)
	}
}

func TestReconcileBlueGreenAfterImport_FailGCsGreen(t *testing.T) {
	parent := rebuildParent("bg-fail")
	parent.Spec.Database.RebuildStrategy = nominatimv1alpha1.RebuildStrategyBlueGreen
	op := rebuildOp("op-fail", parent, nil)
	parent.Status.Rebuild = seedBlueGreenRebuildStatus(parent, op)
	parent.Status.Rebuild.Phase = nominatimv1alpha1.RebuildPhaseImporting
	parent.Status.Rebuild.Message = "import boom"

	green := newCNPGCluster(BlueGreenClusterName(parent))
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&nominatimv1alpha1.NominatimInstance{}, &nominatimv1alpha1.NominatimOperation{}).
		WithObjects(parent, op, green).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	// Force failure branch via advance input by setting Failed through a thin wrapper:
	// reconcileBlueGreenAfterImport only fails when out.Phase is Failed — inject by
	// calling advance path with Failed=true through reconcile after marking Job failed is harder.
	// Exercise deleteOwnedClusterBundle / GC green via act from Failed phase:
	parent.Status.Rebuild.Phase = nominatimv1alpha1.RebuildPhaseFailed
	in := blueGreenInput{Phase: nominatimv1alpha1.RebuildPhaseImporting, Failed: true, Message: "boom", Now: time.Now().UTC()}
	out, act := advanceBlueGreen(in)
	if out.Phase != nominatimv1alpha1.RebuildPhaseFailed || !act.GarbageCollectGreen {
		t.Fatalf("want Fail+GC green: %+v %+v", out, act)
	}
	if err := r.deleteOwnedClusterBundle(context.Background(), parent, BlueGreenClusterName(parent)); err != nil {
		t.Fatalf("GC green: %v", err)
	}
	gone := &unstructured.Unstructured{}
	gone.SetGroupVersionKind(CNPGClusterGVK)
	err := c.Get(context.Background(), types.NamespacedName{Name: BlueGreenClusterName(parent), Namespace: parent.Namespace}, gone)
	if err == nil {
		t.Fatal("expected green deleted")
	}
}

func TestUsesBlueGreenRebuild_Nil(t *testing.T) {
	if usesBlueGreenRebuild(nil) || blueGreenRebuildAllowed(nil) {
		t.Fatal("nil parent")
	}
	parent := rebuildParent("nilcut")
	applyBlueGreenCutover(parent) // no rebuild status — no-op
	if blueGreenJobConnectionSecret(parent) == "" {
		t.Fatal("expected default pending secret name")
	}
}

func TestEnsureBlueGreenPending_WithFlatnodeAndReady(t *testing.T) {
	parent := rebuildParent("bg-flat")
	parent.Spec.Database.RebuildStrategy = nominatimv1alpha1.RebuildStrategyBlueGreen
	parent.Spec.Project.Volume = nominatimv1alpha1.VolumeSource{
		VolumeClaimTemplate: &nominatimv1alpha1.VolumeClaimTemplate{
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		},
	}
	parent.Spec.Flatnode = &nominatimv1alpha1.FlatnodeSpec{
		Volume: nominatimv1alpha1.VolumeSource{
			VolumeClaimTemplate: &nominatimv1alpha1.VolumeClaimTemplate{
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
					},
				},
			},
		},
	}
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&nominatimv1alpha1.NominatimInstance{}).
		WithObjects(parent).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}
	op := rebuildOp("op-flat", parent, nil)

	_, err := r.ensureBlueGreenPending(context.Background(), op, parent)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: BlueGreenFlatnodePVCName(parent), Namespace: parent.Namespace}, pvc); err != nil {
		t.Fatalf("flatnode PVC: %v", err)
	}
	_ = c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, parent)
	if parent.Status.Rebuild == nil || parent.Status.Rebuild.PendingFlatnodePVCName == "" {
		t.Fatalf("rebuild status missing flatnode: %+v", parent.Status.Rebuild)
	}

	// Make pending ready and re-call — expect ready=true.
	greenName := BlueGreenClusterName(parent)
	green := &unstructured.Unstructured{}
	green.SetGroupVersionKind(CNPGClusterGVK)
	_ = c.Get(context.Background(), types.NamespacedName{Name: greenName, Namespace: parent.Namespace}, green)
	_ = unstructured.SetNestedSlice(green.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	_ = c.Update(context.Background(), green)
	db := &unstructured.Unstructured{}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	_ = c.Get(context.Background(), types.NamespacedName{Name: CNPGDatabaseNameForCluster(greenName), Namespace: parent.Namespace}, db)
	_ = unstructured.SetNestedField(db.Object, true, "status", "applied")
	_ = c.Update(context.Background(), db)
	_ = c.Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: CNPGAppSecretName(greenName), Namespace: parent.Namespace},
	})

	ready, err := r.ensureBlueGreenPending(context.Background(), op, parent)
	if err != nil || !ready {
		t.Fatalf("want ready: ready=%v err=%v", ready, err)
	}
}

func TestReconcileBlueGreenAfterImport_JobFailedGCsGreen(t *testing.T) {
	parent := rebuildParent("bg-jobfail")
	parent.Spec.Database.RebuildStrategy = nominatimv1alpha1.RebuildStrategyBlueGreen
	op := rebuildOp("op-jobfail", parent, nil)
	op.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseFailed
	op.Status.Message = "Job failed"
	parent.Status.Rebuild = seedBlueGreenRebuildStatus(parent, op)
	parent.Status.Rebuild.Phase = nominatimv1alpha1.RebuildPhaseImporting
	green := newCNPGCluster(BlueGreenClusterName(parent))
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&nominatimv1alpha1.NominatimInstance{}, &nominatimv1alpha1.NominatimOperation{}).
		WithObjects(parent, op, green).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	_, err := r.reconcileBlueGreenAfterImport(context.Background(), op, parent)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	gone := &unstructured.Unstructured{}
	gone.SetGroupVersionKind(CNPGClusterGVK)
	err = c.Get(context.Background(), types.NamespacedName{Name: BlueGreenClusterName(parent), Namespace: parent.Namespace}, gone)
	if err == nil {
		t.Fatal("expected green GC on Job failure")
	}
	got := &nominatimv1alpha1.NominatimOperation{}
	_ = c.Get(context.Background(), types.NamespacedName{Name: op.Name, Namespace: op.Namespace}, got)
	if got.Status.Phase != nominatimv1alpha1.NominatimOperationPhaseFailed {
		t.Fatalf("phase=%s", got.Status.Phase)
	}
}

func TestBlueGreenRetiredClusterName_UnexpectedLive(t *testing.T) {
	parent := rebuildParent("unexpected")
	parent.Status.Database.ClusterName = "custom-pg"
	if got := blueGreenRetiredClusterName(parent); got != OwnedCNPGClusterName(parent) {
		t.Fatalf("got %q", got)
	}
}

func TestEnsureBlueGreenPending_SeedsEmptyPendingName(t *testing.T) {
	parent := rebuildParent("empty-pend")
	parent.Spec.Database.RebuildStrategy = nominatimv1alpha1.RebuildStrategyBlueGreen
	parent.Spec.Project.Volume = nominatimv1alpha1.VolumeSource{
		VolumeClaimTemplate: &nominatimv1alpha1.VolumeClaimTemplate{
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		},
	}
	op := rebuildOp("op-empty", parent, nil)
	parent.Status.Rebuild = &nominatimv1alpha1.RebuildStatus{
		Phase:         nominatimv1alpha1.RebuildPhaseProvisioning,
		OperationName: op.Name,
		// PendingClusterName intentionally empty — ensure fills it.
	}
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&nominatimv1alpha1.NominatimInstance{}).
		WithObjects(parent).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	_, err := r.ensureBlueGreenPending(context.Background(), op, parent)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	_ = c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, parent)
	if parent.Status.Rebuild.PendingClusterName != BlueGreenClusterName(parent) {
		t.Fatalf("pending=%q", parent.Status.Rebuild.PendingClusterName)
	}
}

func TestBlueGreenPendingReady_NotReadyVariants(t *testing.T) {
	parent := rebuildParent("nr")
	greenName := BlueGreenClusterName(parent)
	scheme := testScheme(t)

	// Cluster exists but no Ready condition
	cluster := newCNPGCluster(greenName)
	cluster.SetNamespace(parent.Namespace)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}
	ready, err := r.blueGreenPendingReady(context.Background(), parent.Namespace, greenName)
	if err != nil || ready {
		t.Fatalf("no conditions: ready=%v err=%v", ready, err)
	}

	// Ready False
	_ = unstructured.SetNestedSlice(cluster.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "False"},
		"skip-me",
	}, "status", "conditions")
	_ = c.Update(context.Background(), cluster)
	ready, err = r.blueGreenPendingReady(context.Background(), parent.Namespace, greenName)
	if err != nil || ready {
		t.Fatalf("Ready=False: ready=%v err=%v", ready, err)
	}

	// Ready True but no secret
	_ = unstructured.SetNestedSlice(cluster.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	_ = c.Update(context.Background(), cluster)
	ready, err = r.blueGreenPendingReady(context.Background(), parent.Namespace, greenName)
	if err != nil || ready {
		t.Fatalf("no secret: ready=%v err=%v", ready, err)
	}
}

func TestLiveOwnedCNPGClusterNameAndDatabaseName(t *testing.T) {
	t.Parallel()
	parent := rebuildParent("live-name")
	if got := LiveOwnedCNPGClusterName(parent); got != OwnedCNPGClusterName(parent) {
		t.Fatalf("default live=%q", got)
	}
	parent.Status.Database.ClusterName = BlueGreenClusterName(parent)
	if got := LiveOwnedCNPGClusterName(parent); got != BlueGreenClusterName(parent) {
		t.Fatalf("status live=%q", got)
	}
	if got := OwnedCNPGDatabaseName(parent); got != CNPGDatabaseNameForCluster(BlueGreenClusterName(parent)) {
		t.Fatalf("db name=%q", got)
	}
	if got := CNPGDatabaseNameForCluster("demo-pg-bg"); got != "demo-pg-bg-nominatim" {
		t.Fatalf("for cluster=%q", got)
	}
}

func TestEnsureOwnedCNPGCluster_RequiresSpec(t *testing.T) {
	parent := baseNominatim("nospec")
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef: &nominatimv1alpha1.DatabaseClusterRef{Name: "x"},
	}
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	err := ensureOwnedCNPGCluster(context.Background(), c, scheme, parent, "x-pg")
	if err == nil {
		t.Fatal("expected error without spec.database.cluster")
	}
}

func TestReconcileBlueGreenAfterImport_NilRebuildStatus(t *testing.T) {
	parent := rebuildParent("nil-rb")
	parent.Spec.Database.RebuildStrategy = nominatimv1alpha1.RebuildStrategyBlueGreen
	parent.Status.Rebuild = nil
	op := rebuildOp("op-nil-rb", parent, nil)
	op.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseSucceeded
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&nominatimv1alpha1.NominatimInstance{}, &nominatimv1alpha1.NominatimOperation{}).
		WithObjects(parent, op).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	_, err := r.reconcileBlueGreenAfterImport(context.Background(), op, parent)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &nominatimv1alpha1.NominatimInstance{}
	_ = c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, got)
	if got.Status.Rebuild == nil {
		t.Fatal("expected status.rebuild seeded")
	}
}

func TestDeleteOwnedClusterBundle_AlreadyGone(t *testing.T) {
	parent := rebuildParent("gone-bundle")
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}
	if err := r.deleteOwnedClusterBundle(context.Background(), parent, BlueGreenClusterName(parent)); err != nil {
		t.Fatalf("IgnoreNotFound: %v", err)
	}
}
