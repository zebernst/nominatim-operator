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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

func TestParseSequenceReport(t *testing.T) {
	t.Parallel()
	got, err := parseSequenceReport(`{"europe/monaco":"4862@2026-07-28T20:21:00Z"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got["europe/monaco"] != "4862@2026-07-28T20:21:00Z" {
		t.Fatalf("got %q", got["europe/monaco"])
	}
	if _, err := parseSequenceReport(`nope`); err == nil {
		t.Fatal("expected error")
	}
}

func TestApplySequenceReportMap(t *testing.T) {
	t.Parallel()
	nom := &nominatimv1alpha1.Nominatim{
		Status: nominatimv1alpha1.NominatimStatus{
			Regions: []nominatimv1alpha1.RegionStatus{
				{Name: "europe/monaco"},
				{Name: "europe/andorra"},
			},
		},
	}
	applySequenceReportMap(nom, map[string]string{
		"europe/monaco": "1@t",
		"europe/france": "ignored",
	}, nil)
	if nom.Status.Regions[0].SequenceState != "1@t" {
		t.Fatalf("monaco=%q", nom.Status.Regions[0].SequenceState)
	}
	if nom.Status.Regions[1].SequenceState != "" {
		t.Fatalf("andorra unexpectedly set")
	}
	if len(nom.Status.Regions) != 2 {
		t.Fatal("must not invent regions")
	}
}

func TestSequenceProbeOperation(t *testing.T) {
	t.Parallel()
	op := &nominatimv1alpha1.NominatimOperation{
		Spec:   nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationUpdate},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	if !sequenceProbeOperation(op) {
		t.Fatal("Update Succeeded should probe")
	}
	op.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseRunning
	if sequenceProbeOperation(op) {
		t.Fatal("Running should not probe")
	}
	op.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseSucceeded
	op.Spec.Type = nominatimv1alpha1.NominatimOperationRefresh
	if sequenceProbeOperation(op) {
		t.Fatal("Refresh should not probe")
	}
	op.Spec.Type = nominatimv1alpha1.NominatimOperationMigrate
	if sequenceProbeOperation(op) {
		t.Fatal("Migrate should not probe")
	}
	op.Spec.Type = nominatimv1alpha1.NominatimOperationFreeze
	if sequenceProbeOperation(op) {
		t.Fatal("Freeze should not probe")
	}
}

func TestReconcileSequenceObservation_CreatesProbeAndAppliesConfigMap(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := nominatimWithConnectionSecret("seq-obs")
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "seq-obs-update", Namespace: nom.Namespace},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom, op).WithObjects(nom, op).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileSequenceObservation(ctx, nom, parentOps(t, r, ctx, nom)); err != nil {
		t.Fatalf("reconcileSequenceObservation: %v", err)
	}

	sa := &corev1.ServiceAccount{}
	if err := c.Get(ctx, types.NamespacedName{Name: sequenceProbeSAName(nom), Namespace: nom.Namespace}, sa); err != nil {
		t.Fatalf("SA: %v", err)
	}
	role := &rbacv1.Role{}
	if err := c.Get(ctx, types.NamespacedName{Name: sequenceProbeSAName(nom), Namespace: nom.Namespace}, role); err != nil {
		t.Fatalf("Role: %v", err)
	}
	job := &batchv1.Job{}
	if err := c.Get(ctx, types.NamespacedName{Name: sequenceProbeJobName(op), Namespace: nom.Namespace}, job); err != nil {
		t.Fatalf("Job: %v", err)
	}
	if job.Spec.Template.Spec.ServiceAccountName != sequenceProbeSAName(nom) {
		t.Fatalf("job SA=%q", job.Spec.Template.Spec.ServiceAccountName)
	}
	if len(job.Spec.Template.Spec.Containers) != 1 || job.Spec.Template.Spec.Containers[0].Command[0] != "/opt/nominatim/scripts/report-sequence.sh" {
		t.Fatalf("job command=%v", job.Spec.Template.Spec.Containers[0].Command)
	}
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.PersistentVolumeClaim != nil && !v.PersistentVolumeClaim.ReadOnly {
			t.Fatal("project volume should be read-only on probe")
		}
	}

	// Simulate probe success + ConfigMap write, then observe again.
	job.Status.Succeeded = 1
	if err := c.Status().Update(ctx, job); err != nil {
		t.Fatalf("job status: %v", err)
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{Name: sequenceReportConfigMapName(nom), Namespace: nom.Namespace}, cm); err != nil {
		t.Fatalf("get cm: %v", err)
	}
	cm.Data = map[string]string{sequenceReportCMKey: `{"europe/monaco":"9@now"}`}
	if err := c.Update(ctx, cm); err != nil {
		t.Fatalf("cm update: %v", err)
	}
	if err := r.reconcileSequenceObservation(ctx, nom, parentOps(t, r, ctx, nom)); err != nil {
		t.Fatalf("second observe: %v", err)
	}
	if nom.Status.Regions[0].SequenceState != "9@now" {
		t.Fatalf("SequenceState=%q", nom.Status.Regions[0].SequenceState)
	}
	gotOp := &nominatimv1alpha1.NominatimOperation{}
	if err := c.Get(ctx, types.NamespacedName{Name: op.Name, Namespace: op.Namespace}, gotOp); err != nil {
		t.Fatal(err)
	}
	if gotOp.Annotations[annotationSequenceObserved] != annotationValueTrue {
		t.Fatalf("annotations=%v", gotOp.Annotations)
	}
}

func TestEnsureSequenceProbes_DoesNotApplyReport(t *testing.T) {
	// Ensure creates probe RBAC/Job/ConfigMap. Observation of report.json into
	// status.regions[].sequenceState is applySequenceReportConfigMap only.
	scheme := testScheme(t)
	ctx := context.Background()
	nom := nominatimWithConnectionSecret("seq-ensure-only")
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "seq-ensure-only-update", Namespace: nom.Namespace},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: sequenceReportConfigMapName(nom), Namespace: nom.Namespace},
		Data:       map[string]string{sequenceReportCMKey: `{"europe/monaco":"9@now"}`},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom, op).WithObjects(nom, op, cm).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.ensureSequenceProbes(ctx, nom, []nominatimv1alpha1.NominatimOperation{*op}); err != nil {
		t.Fatalf("ensureSequenceProbes: %v", err)
	}
	if nom.Status.Regions[0].SequenceState != "" {
		t.Fatalf("ensure must not write SequenceState, got %q", nom.Status.Regions[0].SequenceState)
	}
	job := &batchv1.Job{}
	if err := c.Get(ctx, types.NamespacedName{Name: sequenceProbeJobName(op), Namespace: nom.Namespace}, job); err != nil {
		t.Fatalf("expected probe Job: %v", err)
	}

	if err := r.applySequenceReportConfigMap(ctx, nom); err != nil {
		t.Fatalf("applySequenceReportConfigMap: %v", err)
	}
	if nom.Status.Regions[0].SequenceState != "9@now" {
		t.Fatalf("observe SequenceState=%q want 9@now", nom.Status.Regions[0].SequenceState)
	}
}

func TestListOperationsForParent_ErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("ops-list-err")
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: &failingListClient{Client: base}, Scheme: scheme}
	if _, err := r.listOperationsForParent(context.Background(), nom); err == nil {
		t.Fatal("expected List error to propagate")
	}
}

func TestBuildSequenceProbeJob_UsesWorkerImage(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("img")
	nom.Spec.Worker = &nominatimv1alpha1.WorkerSpec{
		Image: &nominatimv1alpha1.ImageSpec{Repository: "example.com/worker", Tag: "probe-test"},
	}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "img-boot", Namespace: nom.Namespace},
	}
	r := &NominatimReconciler{Scheme: scheme}
	job, err := r.buildSequenceProbeJob(nom, op)
	if err != nil {
		t.Fatal(err)
	}
	want := "example.com/worker:probe-test"
	if job.Spec.Template.Spec.Containers[0].Image != want {
		t.Fatalf("image=%q want %q", job.Spec.Template.Spec.Containers[0].Image, want)
	}
}

func TestSequenceProbeJobName_TruncatesLongName(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 70)
	op := &nominatimv1alpha1.NominatimOperation{ObjectMeta: metav1.ObjectMeta{Name: long}}
	got := sequenceProbeJobName(op)
	if len(got) != 63 {
		t.Fatalf("len=%d want 63", len(got))
	}
}

func TestReconcileSequenceObservation_EmptyRegions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	nom := baseNominatim("empty-regions")
	r := &NominatimReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(nom).Build()}
	if err := r.reconcileSequenceObservation(ctx, nom, parentOps(t, r, ctx, nom)); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileSequenceObservation_SkipsAlreadyObserved(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := nominatimWithConnectionSecret("skip-obs")
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "skip-obs-update",
			Namespace:   nom.Namespace,
			Annotations: map[string]string{annotationSequenceObserved: annotationValueTrue},
		},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom, op).WithObjects(nom, op).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileSequenceObservation(ctx, nom, parentOps(t, r, ctx, nom)); err != nil {
		t.Fatal(err)
	}
	job := &batchv1.Job{}
	err := c.Get(ctx, types.NamespacedName{Name: sequenceProbeJobName(op), Namespace: nom.Namespace}, job)
	if err == nil {
		t.Fatal("expected no probe Job for already-observed operation")
	}
}

func TestEnsureSequenceProbeJob_FailedJobMarksObserved(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := nominatimWithConnectionSecret("fail-probe")
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "fail-probe-update", Namespace: nom.Namespace},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom, op).WithObjects(nom, op).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	job, err := r.buildSequenceProbeJob(nom, op)
	if err != nil {
		t.Fatal(err)
	}
	job.Status.Failed = 1
	if err := c.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureSequenceProbeJob(ctx, nom, op); err != nil {
		t.Fatal(err)
	}
	gotOp := &nominatimv1alpha1.NominatimOperation{}
	if err := c.Get(ctx, types.NamespacedName{Name: op.Name, Namespace: op.Namespace}, gotOp); err != nil {
		t.Fatal(err)
	}
	if gotOp.Annotations[annotationSequenceObserved] != annotationValueTrue {
		t.Fatalf("annotations=%v", gotOp.Annotations)
	}
}

func TestApplySequenceReportConfigMap_EmptyAndMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := testScheme(t)
	nom := baseNominatim("cm-edge")
	r := &NominatimReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()}
	if err := r.applySequenceReportConfigMap(ctx, nom); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: sequenceReportConfigMapName(nom), Namespace: nom.Namespace},
		Data:       map[string]string{sequenceReportCMKey: "   "},
	}
	r = &NominatimReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, cm).Build()}
	if err := r.applySequenceReportConfigMap(ctx, nom); err != nil {
		t.Fatal(err)
	}
	badCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: sequenceReportConfigMapName(nom), Namespace: nom.Namespace},
		Data:       map[string]string{sequenceReportCMKey: "not-json"},
	}
	r = &NominatimReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, badCM).Build()}
	if err := r.applySequenceReportConfigMap(ctx, nom); err == nil {
		t.Fatal("expected parse error")
	}
	auxCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: sequenceReportConfigMapName(nom), Namespace: nom.Namespace},
		Data: map[string]string{
			sequenceAuxReportCMKey: `{"wikimediaImportance":true,"usPostcodes":true}`,
		},
	}
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}}
	r = &NominatimReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, auxCM).Build()}
	if err := r.applySequenceReportConfigMap(ctx, nom); err != nil {
		t.Fatal(err)
	}
	if nom.Status.AuxData == nil || !nom.Status.AuxData.WikimediaImportance || !nom.Status.AuxData.USPostcodes {
		t.Fatalf("aux status not applied: %+v", nom.Status.AuxData)
	}
}

func TestApplySequenceReportMap_SkipsUnchangedAndUsesObservedAt(t *testing.T) {
	t.Parallel()
	observed := metav1.Now()
	nom := &nominatimv1alpha1.Nominatim{
		Status: nominatimv1alpha1.NominatimStatus{
			Regions: []nominatimv1alpha1.RegionStatus{
				{Name: "europe/monaco", SequenceState: "same@t"},
			},
		},
	}
	before := nom.Status.Regions[0].LastUpdatedTime
	applySequenceReportMap(nom, map[string]string{"europe/monaco": "same@t", "": "skip"}, &observed)
	if nom.Status.Regions[0].LastUpdatedTime != before {
		t.Fatal("unchanged sequence should not bump LastUpdatedTime")
	}
	applySequenceReportMap(nom, map[string]string{"europe/monaco": "new@t"}, &observed)
	if nom.Status.Regions[0].SequenceState != "new@t" {
		t.Fatalf("SequenceState=%q", nom.Status.Regions[0].SequenceState)
	}
	if nom.Status.Regions[0].LastUpdatedTime == nil || !nom.Status.Regions[0].LastUpdatedTime.Equal(&observed) {
		t.Fatal("expected observedAt timestamp")
	}
}

func TestEnsureSequenceProbeRBAC_UpdatesExisting(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := baseNominatim("rbac-upd")
	saName := sequenceProbeSAName(nom)
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: nom.Namespace}}
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: nom.Namespace},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{"old"}, Resources: []string{"x"}, Verbs: []string{"get"}}},
	}
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: nom.Namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "wrong"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, sa, role, rb).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.ensureSequenceProbeRBAC(ctx, nom); err != nil {
		t.Fatal(err)
	}
	gotRole := &rbacv1.Role{}
	if err := c.Get(ctx, types.NamespacedName{Name: saName, Namespace: nom.Namespace}, gotRole); err != nil {
		t.Fatal(err)
	}
	if gotRole.Rules[0].Resources[0] != "configmaps" {
		t.Fatalf("role rules=%v", gotRole.Rules)
	}
	gotRB := &rbacv1.RoleBinding{}
	if err := c.Get(ctx, types.NamespacedName{Name: saName, Namespace: nom.Namespace}, gotRB); err != nil {
		t.Fatal(err)
	}
	if gotRB.RoleRef.Name != saName {
		t.Fatalf("roleRef=%q", gotRB.RoleRef.Name)
	}
}

func TestEnsureSequenceProbeJob_InProgressNoMark(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := nominatimWithConnectionSecret("run-probe")
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "run-probe-update", Namespace: nom.Namespace},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom, op).WithObjects(nom, op).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	job, err := r.buildSequenceProbeJob(nom, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureSequenceProbeJob(ctx, nom, op); err != nil {
		t.Fatal(err)
	}
	gotOp := &nominatimv1alpha1.NominatimOperation{}
	if err := c.Get(ctx, types.NamespacedName{Name: op.Name, Namespace: op.Namespace}, gotOp); err != nil {
		t.Fatal(err)
	}
	if gotOp.Annotations[annotationSequenceObserved] == annotationValueTrue {
		t.Fatal("in-progress probe must not mark observed")
	}
}

func TestMarkSequenceObserved_Idempotent(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "idem-obs",
			Namespace:   "default",
			Annotations: map[string]string{annotationSequenceObserved: annotationValueTrue},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(op).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.markSequenceObserved(ctx, op); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileSequenceObservation_SkipsNonProbeTypes(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := nominatimWithConnectionSecret("skip-type")
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "skip-type-refresh", Namespace: nom.Namespace},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationRefresh,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom, op).WithObjects(nom, op).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileSequenceObservation(ctx, nom, parentOps(t, r, ctx, nom)); err != nil {
		t.Fatal(err)
	}
	job := &batchv1.Job{}
	if err := c.Get(ctx, types.NamespacedName{Name: sequenceProbeJobName(op), Namespace: nom.Namespace}, job); err == nil {
		t.Fatal("Refresh must not create a sequence probe Job")
	}
}

func TestApplySequenceReportMap_EmptyInputs(t *testing.T) {
	t.Parallel()
	nom := &nominatimv1alpha1.Nominatim{
		Status: nominatimv1alpha1.NominatimStatus{
			Regions: []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}},
		},
	}
	applySequenceReportMap(nom, nil, nil)
	applySequenceReportMap(nom, map[string]string{}, nil)
	empty := &nominatimv1alpha1.Nominatim{}
	applySequenceReportMap(empty, map[string]string{"europe/monaco": "1@t"}, nil)
}

func TestEnsureSequenceReportConfigMap_CreatesAndUpdates(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := baseNominatim("seq-cm")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.ensureSequenceReportConfigMap(ctx, nom); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureSequenceReportConfigMap(ctx, nom); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{Name: sequenceReportConfigMapName(nom), Namespace: nom.Namespace}, cm); err != nil {
		t.Fatal(err)
	}
	if cm.Labels["nominatim.zebernst.dev/component"] != "sequence-report" {
		t.Fatalf("labels=%v", cm.Labels)
	}
}

func TestSequenceProbeOperation_AllWriteTypes(t *testing.T) {
	t.Parallel()
	opTypes := []nominatimv1alpha1.NominatimOperationType{
		nominatimv1alpha1.NominatimOperationBootstrap,
		nominatimv1alpha1.NominatimOperationAddRegions,
		nominatimv1alpha1.NominatimOperationReimport,
		nominatimv1alpha1.NominatimOperationCatchUp,
	}
	for _, typ := range opTypes {
		op := &nominatimv1alpha1.NominatimOperation{
			Spec:   nominatimv1alpha1.NominatimOperationSpec{Type: typ},
			Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
		}
		if !sequenceProbeOperation(op) {
			t.Fatalf("%s Succeeded should probe", typ)
		}
	}
}

func TestMarkSequenceObserved_NotFound(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	r := &NominatimReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "gone", Namespace: "default"},
	}
	if err := r.markSequenceObserved(ctx, op); err != nil {
		t.Fatal(err)
	}
}

func TestBuildSequenceProbeJob_WorkerPullPolicy(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("pull-pol")
	nom.Spec.Worker = &nominatimv1alpha1.WorkerSpec{
		Image: &nominatimv1alpha1.ImageSpec{
			Repository: "example.com/worker",
			Tag:        "v1",
			PullPolicy: corev1.PullAlways,
		},
	}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "pull-pol-op", Namespace: nom.Namespace},
	}
	r := &NominatimReconciler{Scheme: scheme}
	job, err := r.buildSequenceProbeJob(nom, op)
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.Template.Spec.Containers[0].ImagePullPolicy != corev1.PullAlways {
		t.Fatalf("pullPolicy=%q", job.Spec.Template.Spec.Containers[0].ImagePullPolicy)
	}
}
