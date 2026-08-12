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
	"time"

	"github.com/robfig/cron/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// reconcileUpdates creates scheduled Update NominatimOperations from spec.updates
// (no batch/v1 CronJob). It persists status.lastUpdateScheduleTime as the schedule
// cursor and returns RequeueAfter until the next cron fire.
// ops is the Reconcile-scoped parent Operation list (one list per pass).
func (r *NominatimInstanceReconciler) reconcileUpdates(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance, ops []nominatimv1alpha1.NominatimOperation) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if nom.Spec.Updates == nil || !nom.Spec.Updates.Enabled || nom.Spec.Updates.Schedule == "" {
		return ctrl.Result{}, nil
	}

	// Updates only make sense once regions exist (bootstrap finished).
	if len(nom.Status.Regions) == 0 {
		return ctrl.Result{}, nil
	}

	sched, err := cron.ParseStandard(nom.Spec.Updates.Schedule)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("parse updates.schedule %q: %w", nom.Spec.Updates.Schedule, err)
	}

	now := time.Now().UTC()
	last := scheduleCursor(nom)
	missed := mostRecentScheduleTime(sched, last, now)
	next := sched.Next(now)
	requeue := ctrl.Result{}
	if !next.IsZero() {
		requeue.RequeueAfter = next.Sub(now)
		if requeue.RequeueAfter < time.Second {
			requeue.RequeueAfter = time.Second
		}
	}

	if missed == nil {
		return requeue, nil
	}

	// Probe against a synthetic Update — same evaluateWritePlane module as claim.
	// ScheduleBusy (not Decision alone): creation-race peers must block scheduling
	// even when the probe name would win the lex race.
	probe := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "__schedule-probe__", Namespace: nom.Namespace},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationUpdate,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
	}
	if ev := evaluateWritePlane(probe, ops); ev.ScheduleBusy() {
		conflictName := ""
		if ev.BusyPeer != nil {
			conflictName = ev.BusyPeer.Name
		}
		log.Info("skipping scheduled Update due to active conflict",
			"conflict", conflictName, "fire", missed.UTC().Format(time.RFC3339))
		// Do not advance the cursor — retry after a short delay while write-heavy work runs.
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	opName := scheduledUpdateOperationName(nom, *missed)
	for i := range ops {
		if ops[i].Name == opName {
			// Already created for this fire (e.g. status cursor lag); advance cursor.
			t := metav1.NewTime(*missed)
			nom.Status.LastUpdateScheduleTime = &t
			return requeue, nil
		}
	}

	regions := regionsForUpdate(nom)
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opName,
			Namespace: nom.Namespace,
		},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:                 nominatimv1alpha1.NominatimOperationUpdate,
			NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			Regions:              regions,
		},
	}
	if err := controllerutil.SetControllerReference(nom, op, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("set controller reference on Update operation: %w", err)
	}
	if err := r.Create(ctx, op); err != nil {
		return ctrl.Result{}, fmt.Errorf("create scheduled Update operation: %w", err)
	}

	t := metav1.NewTime(*missed)
	nom.Status.LastUpdateScheduleTime = &t
	log.Info("created scheduled Update operation", "operation", op.Name, "fire", missed.UTC().Format(time.RFC3339))
	return requeue, nil
}

func scheduleCursor(nom *nominatimv1alpha1.NominatimInstance) time.Time {
	if nom.Status.LastUpdateScheduleTime != nil {
		return nom.Status.LastUpdateScheduleTime.Time
	}
	if !nom.CreationTimestamp.IsZero() {
		return nom.CreationTimestamp.Time
	}
	return time.Now().UTC().Add(-time.Second)
}

// mostRecentScheduleTime returns the latest schedule fire in (last, now], or nil if none.
func mostRecentScheduleTime(sched cron.Schedule, last, now time.Time) *time.Time {
	var mostRecent *time.Time
	for t := sched.Next(last); !t.After(now); t = sched.Next(t) {
		tt := t
		mostRecent = &tt
		// Guard against pathological schedules that never advance.
		if !t.After(last) {
			break
		}
		last = t
	}
	return mostRecent
}

func scheduledUpdateOperationName(nom *nominatimv1alpha1.NominatimInstance, fire time.Time) string {
	return fmt.Sprintf("%s-update-%d", nom.Name, fire.UTC().Unix())
}

func regionsForUpdate(nom *nominatimv1alpha1.NominatimInstance) []string {
	if len(nom.Status.Regions) > 0 {
		out := make([]string, 0, len(nom.Status.Regions))
		for _, rs := range nom.Status.Regions {
			out = append(out, rs.Name)
		}
		return out
	}
	return append([]string(nil), nom.Spec.Regions...)
}
