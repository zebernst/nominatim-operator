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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

const deleteRequeueAfter = 5 * time.Second

// reconcileDelete runs ordered teardown before removing the Instance finalizer:
// mark Deleting, drain child Operations, wait for owned CNPG Database/Cluster to go
// away, then drop nominatim.zebernst.dev/finalizer. Caller-owned claimName PVCs are
// never deleted here.
func (r *NominatimInstanceReconciler) reconcileDelete(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(nom, nominatimv1alpha1.NominatimInstanceFinalizer) {
		return ctrl.Result{}, nil
	}

	if err := r.markInstanceDeleting(ctx, nom); err != nil {
		return ctrl.Result{}, err
	}

	remaining, err := r.drainChildOperations(ctx, nom)
	if err != nil {
		return ctrl.Result{}, err
	}
	if remaining > 0 {
		return ctrl.Result{RequeueAfter: deleteRequeueAfter}, nil
	}

	waiting, err := r.deleteOwnedCNPGResources(ctx, nom)
	if err != nil {
		return ctrl.Result{}, err
	}
	if waiting {
		return ctrl.Result{RequeueAfter: deleteRequeueAfter}, nil
	}

	controllerutil.RemoveFinalizer(nom, nominatimv1alpha1.NominatimInstanceFinalizer)
	if err := r.Update(ctx, nom); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *NominatimInstanceReconciler) markInstanceDeleting(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		latest := &nominatimv1alpha1.NominatimInstance{}
		if err := r.Get(ctx, types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}, latest); err != nil {
			return err
		}
		if meta.IsStatusConditionPresentAndEqual(latest.Status.Conditions, nominatimv1alpha1.ConditionDeleting, metav1.ConditionTrue) &&
			meta.IsStatusConditionPresentAndEqual(latest.Status.Conditions, nominatimv1alpha1.ConditionReady, metav1.ConditionFalse) {
			nom.Status = latest.Status
			nom.SetResourceVersion(latest.GetResourceVersion())
			return nil
		}
		conds := append([]metav1.Condition(nil), latest.Status.Conditions...)
		now := metav1.Now()
		meta.SetStatusCondition(&conds, metav1.Condition{
			Type:               nominatimv1alpha1.ConditionDeleting,
			Status:             metav1.ConditionTrue,
			Reason:             "Deleting",
			Message:            "NominatimInstance is draining Operations and owned database resources",
			ObservedGeneration: latest.Generation,
			LastTransitionTime: now,
		})
		meta.SetStatusCondition(&conds, metav1.Condition{
			Type:               nominatimv1alpha1.ConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             "Deleting",
			Message:            "NominatimInstance is being deleted",
			ObservedGeneration: latest.Generation,
			LastTransitionTime: now,
		})
		latest.Status.Conditions = conds
		if err := r.Status().Update(ctx, latest); err != nil {
			return err
		}
		nom.Status = latest.Status
		nom.SetResourceVersion(latest.GetResourceVersion())
		return nil
	})
}

// drainChildOperations deletes NominatimOperations that target nom and reports how many
// still exist (including those already terminating).
func (r *NominatimInstanceReconciler) drainChildOperations(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance) (int, error) {
	log := logf.FromContext(ctx)
	ops, err := r.listOperationsForParent(ctx, nom)
	if err != nil {
		return 0, err
	}
	for i := range ops {
		op := &ops[i]
		if !op.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, op); err != nil && !apierrors.IsNotFound(err) {
			return 0, fmt.Errorf("delete NominatimOperation %q: %w", op.Name, err)
		}
		log.Info("requested deletion of child NominatimOperation", "operation", op.Name)
	}
	ops, err = r.listOperationsForParent(ctx, nom)
	if err != nil {
		return 0, err
	}
	return len(ops), nil
}

// deleteOwnedCNPGResources deletes the operator-owned CNPG Database then Cluster (when
// present) and returns true while either still exists. clusterRef / connectionSecretRef
// Clusters are never deleted — only objects whose controller owner is this Instance.
func (r *NominatimInstanceReconciler) deleteOwnedCNPGResources(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance) (bool, error) {
	log := logf.FromContext(ctx)

	dbWaiting, err := r.deleteOwnedUnstructuredIfController(ctx, nom, CNPGDatabaseGVK, OwnedCNPGDatabaseName(nom))
	if err != nil {
		return false, err
	}
	if dbWaiting {
		log.Info("waiting for owned CNPG Database to terminate", "database", OwnedCNPGDatabaseName(nom))
		return true, nil
	}

	clusterWaiting, err := r.deleteOwnedUnstructuredIfController(ctx, nom, CNPGClusterGVK, OwnedCNPGClusterName(nom))
	if err != nil {
		return false, err
	}
	if clusterWaiting {
		log.Info("waiting for owned CNPG Cluster to terminate", "cluster", OwnedCNPGClusterName(nom))
		return true, nil
	}
	return false, nil
}

// deleteOwnedUnstructuredIfController returns (true, nil) while a controller-owned
// object still exists. Missing objects and non-owned objects are ignored (not deleted).
func (r *NominatimInstanceReconciler) deleteOwnedUnstructuredIfController(
	ctx context.Context,
	nom *nominatimv1alpha1.NominatimInstance,
	gvk schema.GroupVersionKind,
	name string,
) (bool, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: nom.Namespace}, obj)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !metav1.IsControlledBy(obj, nom) {
		return false, nil
	}
	if obj.GetDeletionTimestamp().IsZero() {
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete %s %q: %w", gvk.Kind, name, err)
		}
	}
	return true, nil
}
