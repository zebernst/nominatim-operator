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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// operationRefKind is the Kind used in corev1.ObjectReference entries this controller writes
// to a parent NominatimInstance's status.activeOperationRefs.
const operationRefKind = "NominatimOperation"

// operationObjectReference builds the reference tracked on the parent NominatimInstance's status
// while this Operation is Pending or Running.
func operationObjectReference(op *nominatimv1alpha1.NominatimOperation) corev1.ObjectReference {
	return corev1.ObjectReference{
		APIVersion: nominatimv1alpha1.GroupVersion.String(),
		Kind:       operationRefKind,
		Namespace:  op.Namespace,
		Name:       op.Name,
	}
}

// sameOperationRef compares references the way shouldSuspendAPI/reconcileDelete care about:
// same same-namespace Operation by name (Kind/Namespace included for completeness/dedup).
func sameOperationRef(a, b corev1.ObjectReference) bool {
	return a.Kind == b.Kind && a.Namespace == b.Namespace && a.Name == b.Name
}

// syncParentActiveOperationRef adds (active=true) or removes (active=false) this Operation's
// reference on the parent NominatimInstance's status.activeOperationRefs.
//
// It re-fetches the parent immediately before every write attempt — never reusing an
// earlier in-memory copy — and retries on resourceVersion conflicts, so a concurrent
// NominatimInstance reconcile's status.database write is never stomped by our status.Update. A
// missing parent (already deleted) is treated as a no-op rather than an error.
func (r *NominatimOperationReconciler) syncParentActiveOperationRef(ctx context.Context, op *nominatimv1alpha1.NominatimOperation, active bool) error {
	if op.Spec.NominatimRef.Name == "" {
		return nil
	}
	ref := operationObjectReference(op)
	key := types.NamespacedName{Name: op.Spec.NominatimRef.Name, Namespace: op.Namespace}

	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		parent := &nominatimv1alpha1.NominatimInstance{}
		if err := r.Get(ctx, key, parent); err != nil {
			return client.IgnoreNotFound(err)
		}

		idx := -1
		for i, existing := range parent.Status.ActiveOperationRefs {
			if sameOperationRef(existing, ref) {
				idx = i
				break
			}
		}

		switch {
		case active && idx == -1:
			parent.Status.ActiveOperationRefs = append(parent.Status.ActiveOperationRefs, ref)
		case !active && idx != -1:
			parent.Status.ActiveOperationRefs = append(
				parent.Status.ActiveOperationRefs[:idx],
				parent.Status.ActiveOperationRefs[idx+1:]...,
			)
		default:
			return nil // already in the desired state
		}

		return r.Status().Update(ctx, parent)
	})
}

// syncParentSideEffects keeps the parent NominatimInstance's status.activeOperationRefs current for
// this Operation's phase, and — once the Operation has reached (or already sits at) a
// terminal phase — resumes CNPG backups and reapplies the runtime postgres profile.
//
// Terminal CNPG effects re-fetch the parent themselves (rather than reusing a copy loaded
// earlier in Reconcile) so they always act on the latest status.database. A missing parent
// is tolerated (already deleted) rather than treated as an error.
func (r *NominatimOperationReconciler) syncParentSideEffects(ctx context.Context, op *nominatimv1alpha1.NominatimOperation) error {
	if op.Spec.NominatimRef.Name == "" {
		return nil
	}

	if err := r.syncParentActiveOperationRef(ctx, op, isActiveOperationPhase(op.Status.Phase)); err != nil {
		return err
	}

	if !isTerminalOperationPhase(op.Status.Phase) {
		return nil
	}

	parent := &nominatimv1alpha1.NominatimInstance{}
	key := types.NamespacedName{Name: op.Spec.NominatimRef.Name, Namespace: op.Namespace}
	if err := r.Get(ctx, key, parent); err != nil {
		return client.IgnoreNotFound(err)
	}
	return r.applyTerminalCNPGEffects(ctx, op, parent)
}
