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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

func fakeConnectionSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Data:       map[string][]byte{"uri": []byte("postgresql://n:p@h/db")},
	}
}

// Shared postgres profile values used across side-effects tests (goconst).
const (
	testImportSharedBuffers  = "2GB"
	testRuntimeSharedBuffers = "256MB"
	testImportWorkMem        = "64MB"
)

// stubKindStatusClient fails Status().Update only for objects of the given Go type.
type stubKindStatusClient struct {
	client.Client
	failFor client.Object
}

func (s stubKindStatusClient) Status() client.StatusWriter {
	return stubKindStatusWriter{inner: s.Client.Status(), failFor: s.failFor}
}

type stubKindStatusWriter struct {
	inner   client.StatusWriter
	failFor client.Object
}

func (s stubKindStatusWriter) matches(obj client.Object) bool {
	return fmt.Sprintf("%T", obj) == fmt.Sprintf("%T", s.failFor)
}

func (s stubKindStatusWriter) Create(ctx context.Context, obj client.Object, sub client.Object, opts ...client.SubResourceCreateOption) error {
	if s.matches(obj) {
		return fmt.Errorf("status create failed for %T", obj)
	}
	return s.inner.Create(ctx, obj, sub, opts...)
}

func (s stubKindStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if s.matches(obj) {
		return fmt.Errorf("status update failed for %T", obj)
	}
	return s.inner.Update(ctx, obj, opts...)
}

func (s stubKindStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if s.matches(obj) {
		return fmt.Errorf("status patch failed for %T", obj)
	}
	return s.inner.Patch(ctx, obj, patch, opts...)
}

// pauseOKProfileErrEffects succeeds Pause/Resume but always fails ApplyParameters, letting
// tests reach the "pause/resume succeeded but profile apply failed" propagation branch.
type pauseOKProfileErrEffects struct {
	pauseCalls, resumeCalls, profileCalls int
}

func (e *pauseOKProfileErrEffects) PauseBackups(context.Context, *unstructured.Unstructured) error {
	e.pauseCalls++
	return nil
}

func (e *pauseOKProfileErrEffects) ResumeBackups(context.Context, *unstructured.Unstructured) error {
	e.resumeCalls++
	return nil
}

func (e *pauseOKProfileErrEffects) ApplyParameters(context.Context, *unstructured.Unstructured, map[string]string, []string) error {
	e.profileCalls++
	return fmt.Errorf("apply boom")
}

func newCNPGCluster(name string) *unstructured.Unstructured {
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName(name)
	cluster.SetNamespace("default")
	// Default Ready so Operation Jobs can proceed in unit tests unless a case overrides.
	_ = unstructured.SetNestedSlice(cluster.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	return cluster
}

// --- defaultCNPGEffects: annotation-based pause + spec.postgresql.parameters merge ---

func TestDefaultCNPGEffects_PauseAnnotationSetAndCleared(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newCNPGCluster("pg")).Build()
	effects := defaultCNPGEffects{Client: c}
	ctx := context.Background()
	key := types.NamespacedName{Name: "pg", Namespace: "default"}

	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(ctx, key, live); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := effects.PauseBackups(ctx, live); err != nil {
		t.Fatalf("PauseBackups: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(ctx, key, got); err != nil {
		t.Fatalf("get after pause: %v", err)
	}
	if got.GetAnnotations()[CNPGBackupPausedAnnotation] != annotationValueTrue {
		t.Fatalf("expected pause annotation set, got %#v", got.GetAnnotations())
	}

	if err := effects.ResumeBackups(ctx, got); err != nil {
		t.Fatalf("ResumeBackups: %v", err)
	}
	got2 := &unstructured.Unstructured{}
	got2.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(ctx, key, got2); err != nil {
		t.Fatalf("get after resume: %v", err)
	}
	if _, ok := got2.GetAnnotations()[CNPGBackupPausedAnnotation]; ok {
		t.Fatalf("expected pause annotation cleared, got %#v", got2.GetAnnotations())
	}
}

func TestDefaultCNPGEffects_ResumeWithNoAnnotationsIsNoop(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newCNPGCluster("pg-noann")).Build()
	effects := defaultCNPGEffects{Client: c}
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: "pg-noann", Namespace: "default"}, live); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := effects.ResumeBackups(context.Background(), live); err != nil {
		t.Fatalf("ResumeBackups on cluster with no annotations: %v", err)
	}
}

func TestDefaultCNPGEffects_ApplyParametersMerges(t *testing.T) {
	scheme := testScheme(t)
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"postgresql": map[string]interface{}{
				"parameters": map[string]interface{}{
					"max_connections": "100",
				},
			},
		},
	}}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("pg2")
	cluster.SetNamespace("default")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	effects := defaultCNPGEffects{Client: c}
	ctx := context.Background()
	key := types.NamespacedName{Name: "pg2", Namespace: "default"}

	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(ctx, key, live); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := effects.ApplyParameters(ctx, live, map[string]string{"shared_buffers": testImportSharedBuffers}, nil); err != nil {
		t.Fatalf("ApplyParameters: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(ctx, key, got); err != nil {
		t.Fatalf("get after apply: %v", err)
	}
	params, found, err := unstructured.NestedStringMap(got.Object, "spec", "postgresql", "parameters")
	if err != nil || !found {
		t.Fatalf("params found=%v err=%v", found, err)
	}
	if params["max_connections"] != "100" {
		t.Fatalf("expected pre-existing param preserved, got %#v", params)
	}
	if params["shared_buffers"] != testImportSharedBuffers {
		t.Fatalf("expected new param merged, got %#v", params)
	}
}

func TestDefaultCNPGEffects_ApplyParametersRemovesKeys(t *testing.T) {
	scheme := testScheme(t)
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"postgresql": map[string]interface{}{
				"parameters": map[string]interface{}{
					"max_connections": "100",
					"work_mem":        testImportWorkMem,
					"shared_buffers":  testImportSharedBuffers,
				},
			},
		},
	}}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("pg-rm")
	cluster.SetNamespace("default")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	effects := defaultCNPGEffects{Client: c}
	ctx := context.Background()
	key := types.NamespacedName{Name: "pg-rm", Namespace: "default"}

	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(ctx, key, live); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := effects.ApplyParameters(ctx, live, map[string]string{"shared_buffers": testRuntimeSharedBuffers}, []string{"work_mem"}); err != nil {
		t.Fatalf("ApplyParameters: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(ctx, key, got); err != nil {
		t.Fatalf("get after apply: %v", err)
	}
	params, _, err := unstructured.NestedStringMap(got.Object, "spec", "postgresql", "parameters")
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	if params["max_connections"] != "100" {
		t.Fatalf("expected unrelated param preserved, got %#v", params)
	}
	if params["shared_buffers"] != testRuntimeSharedBuffers {
		t.Fatalf("expected updated param, got %#v", params)
	}
	if _, ok := params["work_mem"]; ok {
		t.Fatalf("expected work_mem removed, got %#v", params)
	}
}

func TestDefaultCNPGEffects_ApplyParametersEmptyIsNoop(t *testing.T) {
	effects := defaultCNPGEffects{}
	if err := effects.ApplyParameters(context.Background(), &unstructured.Unstructured{}, nil, nil); err != nil {
		t.Fatalf("expected no-op for empty params, got %v", err)
	}
}

func TestCNPGEffects_DefaultAccessors(t *testing.T) {
	opR := &NominatimOperationReconciler{}
	if _, ok := opR.cnpgEffects().(defaultCNPGEffects); !ok {
		t.Fatalf("expected default effects when unset, got %T", opR.cnpgEffects())
	}
	effects := &recordingCNPGEffects{}
	opR.CNPGEffects = effects
	if opR.cnpgEffects() != CNPGEffects(effects) {
		t.Fatal("expected override to be returned when set")
	}
}

func TestSetBackupPaused_NotYetAttachedNoOp(t *testing.T) {
	effects := &recordingCNPGEffects{}
	parent := baseNominatim("np-unattached")
	if err := setBackupPaused(context.Background(), nil, effects, parent, true); err != nil {
		t.Fatalf("expected no-op when status.database.mode is unset, got %v", err)
	}
	if effects.pauseCalls != 0 {
		t.Fatalf("must not pause before attach, got %d", effects.pauseCalls)
	}
}

func TestSetBackupPaused_Attached(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newCNPGCluster("pg")).Build()
	effects := &recordingCNPGEffects{}
	parent := baseNominatim("np-attached")
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode: nominatimv1alpha1.DatabaseModeClusterAttached, ClusterName: "pg", ConnectionSecretName: "pg-app",
	}

	if err := setBackupPaused(context.Background(), c, effects, parent, true); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if effects.pauseCalls != 1 {
		t.Fatalf("expected pause called, got %d", effects.pauseCalls)
	}

	parent.Status.Database.ClusterName = ""
	if err := setBackupPaused(context.Background(), c, effects, parent, true); err == nil {
		t.Fatal("expected error for missing cluster name")
	}
}

func TestApplyPostgresProfile_NotYetAttachedNoOp(t *testing.T) {
	effects := &recordingCNPGEffects{}
	parent := baseNominatim("app-unattached")
	if err := applyPostgresProfile(context.Background(), nil, effects, parent, postgresProfileImport); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
	if effects.profileCalls != 0 {
		t.Fatalf("must not apply profile before attach, got %d", effects.profileCalls)
	}
}

func TestApplyPostgresProfile_Attached(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newCNPGCluster("pg")).Build()
	effects := &recordingCNPGEffects{}
	parent := baseNominatim("app-attached")
	parent.Spec.Database.PostgresProfiles = &nominatimv1alpha1.PostgresProfiles{Import: map[string]string{"work_mem": testImportWorkMem}}
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode: nominatimv1alpha1.DatabaseModeClusterAttached, ClusterName: "pg", ConnectionSecretName: "pg-app",
	}

	if err := applyPostgresProfile(context.Background(), c, effects, parent, postgresProfileImport); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if effects.profileCalls != 1 || effects.lastParams["work_mem"] != testImportWorkMem {
		t.Fatalf("expected profile applied, got calls=%d params=%v", effects.profileCalls, effects.lastParams)
	}
}

// --- setBackupPaused / applyPostgresProfile (moved from nominatim_database_test) ---

func TestApplyPostgresProfile_UnknownWhich(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("prof")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef: &nominatimv1alpha1.DatabaseClusterRef{Name: "pg"},
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode:        nominatimv1alpha1.DatabaseModeClusterAttached,
		ClusterName: "pg",
	}
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("pg")
	cluster.SetNamespace("default")

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, cluster).Build()
	if err := applyPostgresProfile(context.Background(), c, &recordingCNPGEffects{}, nom, "nope"); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestSetBackupPaused_ErrorsAndDefaults(t *testing.T) {
	scheme := testScheme(t)
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("pg")
	cluster.SetNamespace("default")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	defaults := defaultCNPGEffects{Client: c}

	nom := baseNominatim("bp")
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode: nominatimv1alpha1.DatabaseModeClusterAttached,
	}
	if err := setBackupPaused(context.Background(), c, defaults, nom, true); err == nil {
		t.Fatal("expected missing cluster name error")
	}

	nom.Status.Database.ClusterName = "missing"
	if err := setBackupPaused(context.Background(), c, defaults, nom, true); err == nil {
		t.Fatal("expected get cluster error")
	}

	nom.Status.Database.ClusterName = "pg"
	if err := setBackupPaused(context.Background(), c, defaults, nom, true); err != nil {
		t.Fatalf("default PauseBackups: %v", err)
	}
	if err := setBackupPaused(context.Background(), c, defaults, nom, false); err != nil {
		t.Fatalf("default ResumeBackups: %v", err)
	}

	// Mode ConnectionSecret without Degraded still no-ops.
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{Mode: nominatimv1alpha1.DatabaseModeConnectionSecret}
	effects := &recordingCNPGEffects{}
	if err := setBackupPaused(context.Background(), c, effects, nom, true); err != nil {
		t.Fatal(err)
	}
	if effects.pauseCalls != 0 {
		t.Fatal("ConnectionSecret mode must not pause")
	}
}

func TestApplyPostgresProfile_Branches(t *testing.T) {
	scheme := testScheme(t)
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("pg")
	cluster.SetNamespace("default")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	effects := &recordingCNPGEffects{}
	ctx := context.Background()

	nom := baseNominatim("ap")
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{Mode: nominatimv1alpha1.DatabaseModeConnectionSecret}
	if err := applyPostgresProfile(ctx, c, effects, nom, postgresProfileImport); err != nil {
		t.Fatal(err)
	}
	if effects.profileCalls != 0 {
		t.Fatal("degraded/secret mode must skip profiles")
	}

	nom.Spec.Database.PostgresProfiles = &nominatimv1alpha1.PostgresProfiles{
		Import: map[string]string{"work_mem": "64MB"},
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode: nominatimv1alpha1.DatabaseModeClusterAttached,
	}
	if err := applyPostgresProfile(ctx, c, effects, nom, postgresProfileImport); err == nil {
		t.Fatal("expected missing cluster name")
	}
	nom.Status.Database.ClusterName = "missing"
	if err := applyPostgresProfile(ctx, c, effects, nom, postgresProfileImport); err == nil {
		t.Fatal("expected get error")
	}
	nom.Status.Database.ClusterName = "pg"
	if err := applyPostgresProfile(ctx, c, effects, nom, postgresProfileImport); err != nil {
		t.Fatal(err)
	}
	if effects.profileCalls != 1 || effects.lastParams["work_mem"] != "64MB" {
		t.Fatalf("import profile not applied: %#v", effects)
	}
	// empty runtime still clears import-only managed keys (work_mem)
	if err := applyPostgresProfile(ctx, c, effects, nom, postgresProfileRuntime); err != nil {
		t.Fatal(err)
	}
	if effects.profileCalls != 2 {
		t.Fatalf("empty runtime should still clear import-only keys, got %d calls", effects.profileCalls)
	}
	// nil profiles + import
	nom.Spec.Database.PostgresProfiles = nil
	if err := applyPostgresProfile(ctx, c, effects, nom, postgresProfileImport); err != nil {
		t.Fatal(err)
	}
	if effects.profileCalls != 2 {
		t.Fatalf("nil profiles must be a no-op, got %d", effects.profileCalls)
	}
}

func TestDefaultCNPGEffects_ApplyParameters(t *testing.T) {
	scheme := testScheme(t)
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("pg")
	cluster.SetNamespace("default")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	nom := baseNominatim("defeff")
	nom.Spec.Database.PostgresProfiles = &nominatimv1alpha1.PostgresProfiles{
		Runtime: map[string]string{"a": "b"},
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode:        nominatimv1alpha1.DatabaseModeClusterAttached,
		ClusterName: "pg",
	}
	if err := applyPostgresProfile(context.Background(), c, defaultCNPGEffects{Client: c}, nom, postgresProfileRuntime); err != nil {
		t.Fatal(err)
	}
}

// --- applyPreJobCNPGEffects / applyTerminalCNPGEffects policy gate + error propagation ---

func TestApplyPreJobCNPGEffects_PolicyMismatchNoOp(t *testing.T) {
	r := &NominatimOperationReconciler{}
	parent := baseNominatim("pj-never")
	parent.Spec.Database.PauseBackupsDuringOperations = nominatimv1alpha1.OperationImpactNever
	op := &nominatimv1alpha1.NominatimOperation{Spec: nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationBootstrap}}
	if err := r.applyPreJobCNPGEffects(context.Background(), op, parent); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}

func TestApplyPreJobCNPGEffects_PausesAndAppliesImport(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newCNPGCluster("pg")).Build()
	effects := &recordingCNPGEffects{}
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme, CNPGEffects: effects}

	parent := baseNominatim("pj-pause")
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		PauseBackupsDuringOperations: nominatimv1alpha1.OperationImpactWriteHeavy,
		PostgresProfiles:             &nominatimv1alpha1.PostgresProfiles{Import: map[string]string{"a": "b"}},
	}
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{Mode: nominatimv1alpha1.DatabaseModeClusterAttached, ClusterName: "pg", ConnectionSecretName: "pg-app"}
	op := &nominatimv1alpha1.NominatimOperation{Spec: nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationBootstrap}}

	if err := r.applyPreJobCNPGEffects(context.Background(), op, parent); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if effects.pauseCalls != 1 || effects.profileCalls != 1 {
		t.Fatalf("expected pause+import, got pause=%d profile=%d", effects.pauseCalls, effects.profileCalls)
	}
}

func TestApplyPreJobCNPGEffects_PauseErrorPropagates(t *testing.T) {
	r := &NominatimOperationReconciler{}
	parent := baseNominatim("pj-pauseerr")
	parent.Spec.Database.PauseBackupsDuringOperations = nominatimv1alpha1.OperationImpactAll
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{Mode: nominatimv1alpha1.DatabaseModeClusterAttached} // no cluster name
	op := &nominatimv1alpha1.NominatimOperation{Spec: nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationUpdate}}
	if err := r.applyPreJobCNPGEffects(context.Background(), op, parent); err == nil {
		t.Fatal("expected error to propagate from setBackupPaused")
	}
}

func TestApplyPreJobCNPGEffects_ProfileErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newCNPGCluster("pg")).Build()
	effects := &pauseOKProfileErrEffects{}
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme, CNPGEffects: effects}

	parent := baseNominatim("pj-profileerr")
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		PauseBackupsDuringOperations: nominatimv1alpha1.OperationImpactAll,
		PostgresProfiles:             &nominatimv1alpha1.PostgresProfiles{Import: map[string]string{"a": "b"}},
	}
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{Mode: nominatimv1alpha1.DatabaseModeClusterAttached, ClusterName: "pg", ConnectionSecretName: "pg-app"}
	op := &nominatimv1alpha1.NominatimOperation{Spec: nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationUpdate}}

	if err := r.applyPreJobCNPGEffects(context.Background(), op, parent); err == nil {
		t.Fatal("expected error to propagate from applyPostgresProfile")
	}
	if effects.pauseCalls != 1 {
		t.Fatalf("expected pause attempted before profile failure, got %d", effects.pauseCalls)
	}
}

func TestApplyTerminalCNPGEffects_PolicyMismatchNoOp(t *testing.T) {
	r := &NominatimOperationReconciler{}
	parent := baseNominatim("tc-never")
	parent.Spec.Database.PauseBackupsDuringOperations = nominatimv1alpha1.OperationImpactNever
	op := &nominatimv1alpha1.NominatimOperation{Spec: nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationBootstrap}}
	if err := r.applyTerminalCNPGEffects(context.Background(), op, parent); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}

func TestApplyTerminalCNPGEffects_ResumesAndAppliesRuntime(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newCNPGCluster("pg")).Build()
	effects := &recordingCNPGEffects{}
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme, CNPGEffects: effects}

	parent := baseNominatim("tc-resume")
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		PauseBackupsDuringOperations: nominatimv1alpha1.OperationImpactAll,
		PostgresProfiles:             &nominatimv1alpha1.PostgresProfiles{Runtime: map[string]string{"a": "b"}},
	}
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{Mode: nominatimv1alpha1.DatabaseModeClusterAttached, ClusterName: "pg", ConnectionSecretName: "pg-app"}
	op := &nominatimv1alpha1.NominatimOperation{Spec: nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationUpdate}}

	if err := r.applyTerminalCNPGEffects(context.Background(), op, parent); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if effects.resumeCalls != 1 || effects.profileCalls != 1 {
		t.Fatalf("expected resume+runtime, got resume=%d profile=%d", effects.resumeCalls, effects.profileCalls)
	}
}

func TestApplyTerminalCNPGEffects_ResumeErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}
	parent := baseNominatim("tc-resumeerr")
	parent.Spec.Database.PauseBackupsDuringOperations = nominatimv1alpha1.OperationImpactAll
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{Mode: nominatimv1alpha1.DatabaseModeClusterAttached}
	op := &nominatimv1alpha1.NominatimOperation{Spec: nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationUpdate}}
	if err := r.applyTerminalCNPGEffects(context.Background(), op, parent); err == nil {
		t.Fatal("expected error to propagate from setBackupPaused")
	}
}

func TestApplyTerminalCNPGEffects_ProfileErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newCNPGCluster("pg")).Build()
	effects := &pauseOKProfileErrEffects{}
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme, CNPGEffects: effects}

	parent := baseNominatim("tc-profileerr")
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		PauseBackupsDuringOperations: nominatimv1alpha1.OperationImpactAll,
		PostgresProfiles:             &nominatimv1alpha1.PostgresProfiles{Runtime: map[string]string{"a": "b"}},
	}
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{Mode: nominatimv1alpha1.DatabaseModeClusterAttached, ClusterName: "pg", ConnectionSecretName: "pg-app"}
	op := &nominatimv1alpha1.NominatimOperation{Spec: nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationUpdate}}

	if err := r.applyTerminalCNPGEffects(context.Background(), op, parent); err == nil {
		t.Fatal("expected error to propagate from applyPostgresProfile")
	}
	if effects.resumeCalls != 1 {
		t.Fatalf("expected resume attempted before profile failure, got %d", effects.resumeCalls)
	}
}

func TestSyncParentActiveOperationRef_NoNominatimInstanceRefIsNoop(t *testing.T) {
	r := &NominatimOperationReconciler{}
	op := &nominatimv1alpha1.NominatimOperation{}
	if err := r.syncParentActiveOperationRef(context.Background(), op, true); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}

func TestSyncParentActiveOperationRef_MissingParentIsNoop(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "op-x", Namespace: "default"},
		Spec:       nominatimv1alpha1.NominatimOperationSpec{NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: "gone"}},
	}
	if err := r.syncParentActiveOperationRef(context.Background(), op, true); err != nil {
		t.Fatalf("expected no-op for missing parent, got %v", err)
	}
}

func TestSyncParentActiveOperationRef_AddRemoveAndAlreadyDesiredIsNoop(t *testing.T) {
	scheme := testScheme(t)
	parent := baseNominatim("adr")
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent).WithObjects(parent).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "op-adr", Namespace: "default"},
		Spec:       nominatimv1alpha1.NominatimOperationSpec{NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name}},
	}

	// Removing an absent ref is a no-op.
	if err := r.syncParentActiveOperationRef(context.Background(), op, false); err != nil {
		t.Fatalf("expected no-op removing absent ref, got %v", err)
	}

	// Adding it, then adding again, is idempotent (second call hits the "already desired" branch).
	if err := r.syncParentActiveOperationRef(context.Background(), op, true); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := r.syncParentActiveOperationRef(context.Background(), op, true); err != nil {
		t.Fatalf("expected no-op re-adding present ref, got %v", err)
	}

	got := &nominatimv1alpha1.NominatimInstance{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: "default"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Status.ActiveOperationRefs) != 1 || got.Status.ActiveOperationRefs[0].Name != op.Name {
		t.Fatalf("expected exactly one ref for %q, got %#v", op.Name, got.Status.ActiveOperationRefs)
	}

	// Removing it clears the list.
	if err := r.syncParentActiveOperationRef(context.Background(), op, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got2 := &nominatimv1alpha1.NominatimInstance{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: "default"}, got2); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got2.Status.ActiveOperationRefs) != 0 {
		t.Fatalf("expected ref removed, got %#v", got2.Status.ActiveOperationRefs)
	}
}

func TestSyncParentSideEffects_NoNominatimInstanceRefIsNoop(t *testing.T) {
	r := &NominatimOperationReconciler{}
	op := &nominatimv1alpha1.NominatimOperation{}
	if err := r.syncParentSideEffects(context.Background(), op); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}

func TestSyncParentSideEffects_NonTerminalStopsAfterRefSync(t *testing.T) {
	scheme := testScheme(t)
	parent := baseNominatim("sse2")
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent).WithObjects(parent).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "op-sse2", Namespace: "default"},
		Spec:       nominatimv1alpha1.NominatimOperationSpec{NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name}},
		Status:     nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseRunning},
	}
	if err := r.syncParentSideEffects(context.Background(), op); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := &nominatimv1alpha1.NominatimInstance{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: parent.Name, Namespace: "default"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Status.ActiveOperationRefs) != 1 {
		t.Fatalf("expected ref added for Running phase, got %#v", got.Status.ActiveOperationRefs)
	}
}

func TestSyncParentSideEffects_TerminalMissingParentIsNoop(t *testing.T) {
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "op-sse3", Namespace: "default"},
		Spec:       nominatimv1alpha1.NominatimOperationSpec{NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: "gone"}},
		Status:     nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	if err := r.syncParentSideEffects(context.Background(), op); err != nil {
		t.Fatalf("expected no-op for missing parent, got %v", err)
	}
}

func TestOperationReconcile_WriteHeavyBootstrap_PauseImportThenTerminalResumeRuntime(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()

	parent := baseNominatim("vzw-write-heavy")
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef:                   &nominatimv1alpha1.DatabaseClusterRef{Name: "pg"},
		PauseBackupsDuringOperations: nominatimv1alpha1.OperationImpactWriteHeavy,
		PostgresProfiles: &nominatimv1alpha1.PostgresProfiles{
			Import:  map[string]string{"shared_buffers": testImportSharedBuffers},
			Runtime: map[string]string{"shared_buffers": testRuntimeSharedBuffers},
		},
	}
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode: nominatimv1alpha1.DatabaseModeClusterAttached, ClusterName: "pg", ConnectionSecretName: "pg-app",
	}

	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-1", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name},
		},
	}

	effects := &recordingCNPGEffects{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent, op).WithObjects(parent, fakeConnectionSecret("pg-app"), newCNPGCluster("pg"), op).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme, CNPGEffects: effects}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: op.Name, Namespace: "default"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if effects.pauseCalls != 1 {
		t.Fatalf("expected 1 pause call, got %d", effects.pauseCalls)
	}
	if effects.profileCalls != 1 || effects.lastParams["shared_buffers"] != testImportSharedBuffers {
		t.Fatalf("expected import profile applied, got calls=%d params=%v", effects.profileCalls, effects.lastParams)
	}

	gotParent := &nominatimv1alpha1.NominatimInstance{}
	if err := c.Get(ctx, types.NamespacedName{Name: parent.Name, Namespace: "default"}, gotParent); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if len(gotParent.Status.ActiveOperationRefs) != 1 || gotParent.Status.ActiveOperationRefs[0].Name != op.Name {
		t.Fatalf("expected activeOperationRefs to contain %q while Pending, got %#v", op.Name, gotParent.Status.ActiveOperationRefs)
	}

	// shouldSuspendAPI reflects the freshly-written ref while the Operation is active.
	nomR := &NominatimInstanceReconciler{Client: c, Scheme: scheme}
	suspend, err := nomR.shouldSuspendAPI(ctx, gotParent, nominatimv1alpha1.OperationImpactWriteHeavy)
	if err != nil {
		t.Fatalf("shouldSuspendAPI: %v", err)
	}
	if !suspend {
		t.Fatal("expected suspend=true while a write-heavy Operation is active")
	}

	// Mark the Job Running, then Succeeded, driving the Operation through its lifecycle.
	job := &batchv1.Job{}
	if err := c.Get(ctx, types.NamespacedName{Name: op.Name, Namespace: "default"}, job); err != nil {
		t.Fatalf("get job: %v", err)
	}
	job.Status.Active = 1
	if err := c.Status().Update(ctx, job); err != nil {
		t.Fatalf("update job status running: %v", err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile (running): %v", err)
	}

	gotOp := &nominatimv1alpha1.NominatimOperation{}
	if err := c.Get(ctx, req.NamespacedName, gotOp); err != nil {
		t.Fatalf("get op: %v", err)
	}
	if gotOp.Status.Phase != nominatimv1alpha1.NominatimOperationPhaseRunning {
		t.Fatalf("expected Running phase, got %q", gotOp.Status.Phase)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: parent.Name, Namespace: "default"}, gotParent); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if len(gotParent.Status.ActiveOperationRefs) != 1 {
		t.Fatalf("expected ref to remain while Running, got %#v", gotParent.Status.ActiveOperationRefs)
	}
	// Still only one pause/import call so far — pre-job effects don't re-fire every reconcile pass
	// beyond what's idempotently necessary, but they *are* re-applied since Pending->Running still
	// isn't terminal; assert we haven't resumed/runtime'd yet.
	if effects.resumeCalls != 0 {
		t.Fatalf("expected no resume before terminal phase, got %d", effects.resumeCalls)
	}

	job.Status.Active = 0
	job.Status.Succeeded = 1
	if err := c.Status().Update(ctx, job); err != nil {
		t.Fatalf("update job status succeeded: %v", err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile (succeeded): %v", err)
	}

	if effects.resumeCalls != 1 {
		t.Fatalf("expected 1 resume call, got %d", effects.resumeCalls)
	}
	// This reconcile pass still observes a non-terminal phase at the top (Running), so it re-applies
	// the import profile once more (pause=3rd call not tracked separately, profile 3rd import call)
	// before syncStatusFromJob flips the phase to Succeeded and the terminal branch applies runtime
	// on top (4th profile call) — both idempotent on a real Cluster, so the extra write is harmless.
	if effects.profileCalls != 4 || effects.lastParams["shared_buffers"] != testRuntimeSharedBuffers {
		t.Fatalf("expected runtime profile applied last, got calls=%d params=%v", effects.profileCalls, effects.lastParams)
	}

	if err := c.Get(ctx, types.NamespacedName{Name: parent.Name, Namespace: "default"}, gotParent); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if len(gotParent.Status.ActiveOperationRefs) != 0 {
		t.Fatalf("expected activeOperationRefs cleared once Succeeded, got %#v", gotParent.Status.ActiveOperationRefs)
	}

	suspend, err = nomR.shouldSuspendAPI(ctx, gotParent, nominatimv1alpha1.OperationImpactWriteHeavy)
	if err != nil {
		t.Fatalf("shouldSuspendAPI: %v", err)
	}
	if suspend {
		t.Fatal("expected suspend=false once the Operation is terminal")
	}

	// A subsequent reconcile of the now-terminal Operation is idempotent (no duplicate effects).
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile (terminal, repeat): %v", err)
	}
	if effects.resumeCalls != 2 {
		// applyTerminalCNPGEffects is re-applied on every terminal reconcile by design (safe/idempotent
		// on the real Cluster); recordingCNPGEffects just counts calls rather than de-duplicating.
		t.Fatalf("expected resume re-applied idempotently on repeat terminal reconcile, got %d", effects.resumeCalls)
	}
}

func TestOperationReconcile_NeverImpact_NoCNPGCallsButRefStillTracked(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()

	parent := baseNominatim("vzw-never")
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef:                   &nominatimv1alpha1.DatabaseClusterRef{Name: "pg"},
		PauseBackupsDuringOperations: nominatimv1alpha1.OperationImpactNever,
	}
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode: nominatimv1alpha1.DatabaseModeClusterAttached, ClusterName: "pg", ConnectionSecretName: "pg-app",
	}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-never", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name},
		},
	}
	effects := &recordingCNPGEffects{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent, op).WithObjects(parent, fakeConnectionSecret("pg-app"), newCNPGCluster("pg"), op).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme, CNPGEffects: effects}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: op.Name, Namespace: "default"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if effects.pauseCalls != 0 || effects.profileCalls != 0 {
		t.Fatalf("Never policy must not call CNPG effects, got pause=%d profile=%d", effects.pauseCalls, effects.profileCalls)
	}

	gotParent := &nominatimv1alpha1.NominatimInstance{}
	if err := c.Get(ctx, types.NamespacedName{Name: parent.Name, Namespace: "default"}, gotParent); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if len(gotParent.Status.ActiveOperationRefs) != 1 {
		t.Fatalf("expected activeOperationRefs still tracked regardless of CNPG policy, got %#v", gotParent.Status.ActiveOperationRefs)
	}
}

func TestOperationReconcile_DegradedParent_NoCNPGCalls(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()

	parent := baseNominatim("vzw-degraded")
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ConnectionSecretRef:          &nominatimv1alpha1.LocalObjectReference{Name: "ext-pg"},
		PauseBackupsDuringOperations: nominatimv1alpha1.OperationImpactWriteHeavy,
	}
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode: nominatimv1alpha1.DatabaseModeConnectionSecret, Degraded: true, ConnectionSecretName: "ext-pg",
	}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-degraded", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name},
		},
	}
	effects := &recordingCNPGEffects{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent, op).WithObjects(parent, fakeConnectionSecret("ext-pg"), op).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme, CNPGEffects: effects}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: op.Name, Namespace: "default"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if effects.pauseCalls != 0 || effects.profileCalls != 0 {
		t.Fatalf("degraded parent must never call CNPG effects, got pause=%d profile=%d", effects.pauseCalls, effects.profileCalls)
	}
}

func TestOperationReconcile_ParentNotYetAttached_NoCNPGCallsNoError(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()

	parent := baseNominatim("vzw-unattached")
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef:                   &nominatimv1alpha1.DatabaseClusterRef{Name: "pg"},
		PauseBackupsDuringOperations: nominatimv1alpha1.OperationImpactWriteHeavy,
	}
	// parent.Status.Database left zero-value: the NominatimInstance controller hasn't reconciled yet.
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-unattached", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name},
		},
	}
	effects := &recordingCNPGEffects{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent, op).WithObjects(parent, op).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme, CNPGEffects: effects}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: op.Name, Namespace: "default"}}
	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile should not fail while parent database isn't attached yet: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected requeue while waiting for parent connectionSecretName, got %#v", res)
	}
	job := &batchv1.Job{}
	if err := c.Get(ctx, types.NamespacedName{Name: op.Name, Namespace: "default"}, job); err == nil {
		t.Fatal("expected no Job before parent status.database.connectionSecretName is set")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get job: %v", err)
	}
	if effects.pauseCalls != 0 || effects.profileCalls != 0 {
		t.Fatalf("expected no CNPG calls before parent attaches a database, got pause=%d profile=%d", effects.pauseCalls, effects.profileCalls)
	}
}

func TestOperationReconcile_TerminalSyncStatusFromJobErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()

	parent := baseNominatim("vzw-term-job-err")
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-term-job-err", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{
			Phase:  nominatimv1alpha1.NominatimOperationPhaseSucceeded,
			JobRef: &nominatimv1alpha1.LocalObjectReference{Name: "boot-term-job-err"},
		},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-term-job-err", Namespace: "default"},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent, op).WithObjects(parent, job, op).Build()
	c := stubKindStatusClient{Client: base, failFor: &nominatimv1alpha1.NominatimOperation{}}
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: op.Name, Namespace: "default"}}
	if _, err := r.Reconcile(ctx, req); err == nil {
		t.Fatal("expected syncStatusFromJob error to propagate from the terminal branch")
	}
}

func TestOperationReconcile_MainPathSyncParentSideEffectsErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()

	parent := baseNominatim("vzw-main-parent-err")
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{Mode: nominatimv1alpha1.DatabaseModeClusterAttached, ClusterName: "pg", ConnectionSecretName: "pg-app"}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-main-parent-err", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name},
		},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent, op).WithObjects(parent, fakeConnectionSecret("pg-app"), newCNPGCluster("pg"), op).Build()
	c := stubKindStatusClient{Client: base, failFor: &nominatimv1alpha1.NominatimInstance{}}
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme, CNPGEffects: &recordingCNPGEffects{}}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: op.Name, Namespace: "default"}}
	if _, err := r.Reconcile(ctx, req); err == nil {
		t.Fatal("expected syncParentSideEffects error to propagate from the main (non-terminal) path")
	}
}

func TestFailOperation_StatusUpdateErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	parent := baseNominatim("vzw-failop-err")
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "op-failop-err", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name},
		},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent, op).WithObjects(parent, op).Build()
	c := stubKindStatusClient{Client: base, failFor: &nominatimv1alpha1.NominatimOperation{}}
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme}

	if err := r.failOperation(context.Background(), op, reasonConflict, "boom"); err == nil {
		t.Fatal("expected status update error to propagate from failOperation")
	}
}

func TestOperationReconcile_ConflictFailure_SyncsParentSideEffectsWithoutError(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()

	parent := baseNominatim("vzw-conflict")
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef:                   &nominatimv1alpha1.DatabaseClusterRef{Name: "pg"},
		PauseBackupsDuringOperations: nominatimv1alpha1.OperationImpactWriteHeavy,
		PostgresProfiles: &nominatimv1alpha1.PostgresProfiles{
			Import:  map[string]string{"shared_buffers": testImportSharedBuffers, "work_mem": testImportWorkMem},
			Runtime: map[string]string{"shared_buffers": testRuntimeSharedBuffers},
		},
	}
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode: nominatimv1alpha1.DatabaseModeClusterAttached, ClusterName: "pg", ConnectionSecretName: "pg-app",
	}
	running := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "op-running", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseRunning},
	}
	effects := &recordingCNPGEffects{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent, running).WithObjects(parent, fakeConnectionSecret("pg-app"), newCNPGCluster("pg")).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme, CNPGEffects: effects}

	if err := c.Create(ctx, running); err != nil {
		t.Fatalf("create running op: %v", err)
	}
	running.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseRunning
	if err := c.Status().Update(ctx, running); err != nil {
		t.Fatalf("set running status: %v", err)
	}

	conflicting := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "op-conflict", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationRebuild,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name},
		},
	}
	if err := c.Create(ctx, conflicting); err != nil {
		t.Fatalf("create conflicting op: %v", err)
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: conflicting.Name, Namespace: "default"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &nominatimv1alpha1.NominatimOperation{}
	if err := c.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != nominatimv1alpha1.NominatimOperationPhaseFailed {
		t.Fatalf("expected Failed phase, got %q", got.Status.Phase)
	}

	gotParent := &nominatimv1alpha1.NominatimInstance{}
	if err := c.Get(ctx, types.NamespacedName{Name: parent.Name, Namespace: "default"}, gotParent); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if len(gotParent.Status.ActiveOperationRefs) != 0 {
		t.Fatalf("a Conflict-failed Operation that never went active must not leave a ref, got %#v", gotParent.Status.ActiveOperationRefs)
	}
	if effects.resumeCalls != 0 || effects.profileCalls != 0 {
		t.Fatalf("conflict failure must not resume/restore while a write-heavy sibling is Running; resume=%d profile=%d",
			effects.resumeCalls, effects.profileCalls)
	}
}

func TestApplyPostgresProfile_RemovesImportOnlyKeysOnRuntime(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"postgresql": map[string]interface{}{
				"parameters": map[string]interface{}{
					"shared_buffers":  testImportSharedBuffers,
					"work_mem":        testImportWorkMem,
					"max_connections": "100",
				},
			},
		},
	}}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("pg-profile-rm")
	cluster.SetNamespace("default")

	nom := baseNominatim("profile-rm")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		PostgresProfiles: &nominatimv1alpha1.PostgresProfiles{
			Import:  map[string]string{"shared_buffers": testImportSharedBuffers, "work_mem": testImportWorkMem},
			Runtime: map[string]string{"shared_buffers": testRuntimeSharedBuffers},
		},
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode: nominatimv1alpha1.DatabaseModeClusterAttached, ClusterName: "pg-profile-rm",
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, cluster).Build()
	if err := applyPostgresProfile(ctx, c, defaultCNPGEffects{Client: c}, nom, postgresProfileRuntime); err != nil {
		t.Fatalf("applyPostgresProfile: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(ctx, types.NamespacedName{Name: "pg-profile-rm", Namespace: "default"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	params, _, err := unstructured.NestedStringMap(got.Object, "spec", "postgresql", "parameters")
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	if params["shared_buffers"] != testRuntimeSharedBuffers {
		t.Fatalf("expected runtime shared_buffers, got %#v", params)
	}
	if _, ok := params["work_mem"]; ok {
		t.Fatalf("import-only work_mem must be removed on runtime apply, got %#v", params)
	}
	if params["max_connections"] != "100" {
		t.Fatalf("unrelated param must be preserved, got %#v", params)
	}
}

func TestOperationReconcile_DeleteMidFlight_ClearsRefAndResumes(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()

	parent := baseNominatim("vzw-delete")
	parent.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef:                   &nominatimv1alpha1.DatabaseClusterRef{Name: "pg"},
		PauseBackupsDuringOperations: nominatimv1alpha1.OperationImpactWriteHeavy,
		PostgresProfiles: &nominatimv1alpha1.PostgresProfiles{
			Import:  map[string]string{"shared_buffers": testImportSharedBuffers},
			Runtime: map[string]string{"shared_buffers": testRuntimeSharedBuffers},
		},
	}
	parent.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode: nominatimv1alpha1.DatabaseModeClusterAttached, ClusterName: "pg", ConnectionSecretName: "pg-app",
	}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-del", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: parent.Name},
		},
	}
	effects := &recordingCNPGEffects{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent, op).WithObjects(parent, fakeConnectionSecret("pg-app"), newCNPGCluster("pg"), op).Build()
	r := &NominatimOperationReconciler{Client: c, Scheme: scheme, CNPGEffects: effects}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: op.Name, Namespace: "default"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if effects.pauseCalls != 1 {
		t.Fatalf("expected pause before delete, got %d", effects.pauseCalls)
	}

	gotOp := &nominatimv1alpha1.NominatimOperation{}
	if err := c.Get(ctx, req.NamespacedName, gotOp); err != nil {
		t.Fatalf("get op: %v", err)
	}
	if !controllerutil.ContainsFinalizer(gotOp, nominatimv1alpha1.NominatimOperationFinalizer) {
		t.Fatal("expected operation finalizer after first reconcile")
	}

	now := metav1.Now()
	gotOp.DeletionTimestamp = &now
	if err := c.Update(ctx, gotOp); err != nil {
		// fake client may require Delete to set DeletionTimestamp
		if err := c.Delete(ctx, gotOp); err != nil {
			t.Fatalf("delete op: %v", err)
		}
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile delete: %v", err)
	}

	gotParent := &nominatimv1alpha1.NominatimInstance{}
	if err := c.Get(ctx, types.NamespacedName{Name: parent.Name, Namespace: "default"}, gotParent); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if len(gotParent.Status.ActiveOperationRefs) != 0 {
		t.Fatalf("expected ActiveOperationRefs cleared on delete, got %#v", gotParent.Status.ActiveOperationRefs)
	}
	if effects.resumeCalls != 1 {
		t.Fatalf("expected resume on delete with no siblings, got %d", effects.resumeCalls)
	}
	if effects.profileCalls < 2 || effects.lastParams["shared_buffers"] != testRuntimeSharedBuffers {
		t.Fatalf("expected runtime profile on delete, calls=%d params=%v", effects.profileCalls, effects.lastParams)
	}

	// Finalizer removed → object can finish deletion (Gone or no finalizer).
	remaining := &nominatimv1alpha1.NominatimOperation{}
	err := c.Get(ctx, req.NamespacedName, remaining)
	if err == nil && controllerutil.ContainsFinalizer(remaining, nominatimv1alpha1.NominatimOperationFinalizer) {
		t.Fatal("expected finalizer removed after delete reconcile")
	}
}
