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
	"time"

	"github.com/robfig/cron/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

func TestMostRecentScheduleTime(t *testing.T) {
	sched, err := cron.ParseStandard("0 * * * *") // hourly
	if err != nil {
		t.Fatal(err)
	}
	last := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 26, 12, 30, 0, 0, time.UTC)
	got := mostRecentScheduleTime(sched, last, now)
	if got == nil {
		t.Fatal("expected a missed fire")
	}
	want := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %v want %v", got, want)
	}

	if mostRecentScheduleTime(sched, now, now) != nil {
		t.Fatal("expected nil when no fire in (last, now]")
	}
}

func TestReconcileUpdates_DisabledNoOp(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("upd-off")
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/liechtenstein"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	res, err := r.reconcileUpdates(context.Background(), nom, parentOps(t, r, context.Background(), nom))
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", res)
	}
	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(context.Background(), ops); err != nil {
		t.Fatal(err)
	}
	if len(ops.Items) != 0 {
		t.Fatalf("expected no operations, got %d", len(ops.Items))
	}
}

func TestReconcileUpdates_DueCreatesUpdateAndCursor(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()

	nom := baseNominatim("upd-due")
	nom.CreationTimestamp = metav1.NewTime(time.Now().UTC().Add(-2 * time.Hour))
	nom.Spec.Updates = &nominatimv1alpha1.UpdatesSpec{
		Enabled:  true,
		Schedule: "* * * * *", // every minute — definitely due since creation
	}
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/germany"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	res, err := r.reconcileUpdates(ctx, nom, parentOps(t, r, ctx, nom))
	if err != nil {
		t.Fatalf("reconcileUpdates: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected RequeueAfter until next fire, got %v", res)
	}
	if nom.Status.LastUpdateScheduleTime == nil {
		t.Fatal("expected lastUpdateScheduleTime cursor set")
	}

	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(ctx, ops); err != nil {
		t.Fatal(err)
	}
	if len(ops.Items) != 1 {
		t.Fatalf("expected 1 Update operation, got %d", len(ops.Items))
	}
	op := ops.Items[0]
	if op.Spec.Type != nominatimv1alpha1.NominatimOperationUpdate {
		t.Fatalf("type=%q", op.Spec.Type)
	}
	if len(op.Spec.Regions) != 1 || op.Spec.Regions[0] != "europe/germany" {
		t.Fatalf("regions=%v", op.Spec.Regions)
	}

	// Second pass must not create another op for the same fire.
	before := nom.Status.LastUpdateScheduleTime.DeepCopy()
	if _, err := r.reconcileUpdates(ctx, nom, parentOps(t, r, ctx, nom)); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	ops = &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(ctx, ops); err != nil {
		t.Fatal(err)
	}
	if len(ops.Items) != 1 {
		t.Fatalf("expected still 1 operation, got %d", len(ops.Items))
	}
	if !nom.Status.LastUpdateScheduleTime.Equal(before) {
		t.Fatalf("cursor changed unexpectedly: %v -> %v", before, nom.Status.LastUpdateScheduleTime)
	}
}

func TestReconcileUpdates_SkipsOnWriteHeavyConflict(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()

	nom := baseNominatim("upd-conflict")
	nom.CreationTimestamp = metav1.NewTime(time.Now().UTC().Add(-2 * time.Hour))
	nom.Spec.Updates = &nominatimv1alpha1.UpdatesSpec{Enabled: true, Schedule: "* * * * *"}
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/liechtenstein"}}

	boot := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-active", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom, boot).WithObjects(nom, boot).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	res, err := r.reconcileUpdates(ctx, nom, parentOps(t, r, ctx, nom))
	if err != nil {
		t.Fatalf("reconcileUpdates: %v", err)
	}
	if nom.Status.LastUpdateScheduleTime != nil {
		t.Fatal("must not advance cursor while write-heavy conflict is active")
	}
	if res.RequeueAfter != 30*time.Second {
		t.Fatalf("expected short requeue on conflict, got %v", res)
	}

	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(ctx, ops); err != nil {
		t.Fatal(err)
	}
	var updates int
	for _, op := range ops.Items {
		if op.Spec.Type == nominatimv1alpha1.NominatimOperationUpdate {
			updates++
		}
	}
	if updates != 0 {
		t.Fatalf("expected no Update created during conflict, got %d", updates)
	}
}

func TestReconcileUpdates_SkipsOnCreationRacePeer(t *testing.T) {
	// Schedule must stay busy while a write-heavy peer is still in the creation
	// race (no Job yet) — even though evaluateWritePlane Decision for the
	// synthetic probe may be Ok (lex-winner).
	scheme := testScheme(t)
	ctx := context.Background()

	nom := baseNominatim("upd-race")
	nom.CreationTimestamp = metav1.NewTime(time.Now().UTC().Add(-2 * time.Hour))
	nom.Spec.Updates = &nominatimv1alpha1.UpdatesSpec{Enabled: true, Schedule: "* * * * *"}
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/liechtenstein"}}

	boot := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-racing", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: ""},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom, boot).WithObjects(nom, boot).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	res, err := r.reconcileUpdates(ctx, nom, parentOps(t, r, ctx, nom))
	if err != nil {
		t.Fatalf("reconcileUpdates: %v", err)
	}
	if nom.Status.LastUpdateScheduleTime != nil {
		t.Fatal("must not advance cursor while a write-heavy peer is still racing")
	}
	if res.RequeueAfter != 30*time.Second {
		t.Fatalf("expected short requeue on schedule-busy, got %v", res)
	}

	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(ctx, ops); err != nil {
		t.Fatal(err)
	}
	for _, op := range ops.Items {
		if op.Spec.Type == nominatimv1alpha1.NominatimOperationUpdate {
			t.Fatalf("expected no Update during creation race, got %s", op.Name)
		}
	}
}

func TestReconcileUpdates_NoCronJobCreated(t *testing.T) {
	// Guardrail: reconcileUpdates must only create NominatimOperation, never batch/v1 CronJob.
	scheme := testScheme(t)
	nom := baseNominatim("upd-nocron")
	nom.CreationTimestamp = metav1.NewTime(time.Now().UTC().Add(-time.Hour))
	nom.Spec.Updates = &nominatimv1alpha1.UpdatesSpec{Enabled: true, Schedule: "*/5 * * * *"}
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/liechtenstein"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom).Build()
	r := &NominatimInstanceReconciler{Client: c, Scheme: scheme}

	if _, err := r.reconcileUpdates(context.Background(), nom, parentOps(t, r, context.Background(), nom)); err != nil {
		t.Fatal(err)
	}
	ops := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(context.Background(), ops); err != nil {
		t.Fatal(err)
	}
	if len(ops.Items) != 1 {
		t.Fatalf("expected one scheduled Update, got %d", len(ops.Items))
	}
	if ops.Items[0].Spec.Type != nominatimv1alpha1.NominatimOperationUpdate {
		t.Fatalf("expected Update type, got %q", ops.Items[0].Spec.Type)
	}
}

func TestRegionsForUpdate(t *testing.T) {
	nom := baseNominatim("regs")
	nom.Spec.Regions = []string{"a", "b"}
	if got := regionsForUpdate(nom); len(got) != 2 || got[0] != "a" {
		t.Fatalf("spec fallback: %v", got)
	}
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "c"}}
	if got := regionsForUpdate(nom); len(got) != 1 || got[0] != "c" {
		t.Fatalf("status preferred: %v", got)
	}
}
