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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// seedBlueGreenRebuildStatus returns a RebuildStatus initialized for a new BlueGreen swap.
func seedBlueGreenRebuildStatus(parent *nominatimv1alpha1.NominatimInstance, op *nominatimv1alpha1.NominatimOperation) *nominatimv1alpha1.RebuildStatus {
	active := LiveOwnedCNPGClusterName(parent)
	pending := BlueGreenClusterName(parent)
	// When live is already the -bg sibling (prior swap), provision the default name as green.
	if active == pending {
		pending = OwnedCNPGClusterName(parent)
	}
	st := &nominatimv1alpha1.RebuildStatus{
		Phase:                       nominatimv1alpha1.RebuildPhaseProvisioning,
		OperationName:               op.Name,
		ActiveClusterName:           active,
		PendingClusterName:          pending,
		ActiveConnectionSecretName:  CNPGAppSecretName(active),
		PendingConnectionSecretName: CNPGAppSecretName(pending),
		PendingProjectPVCName:       BlueGreenProjectPVCName(parent),
	}
	if parent.Spec.Flatnode != nil {
		st.PendingFlatnodePVCName = BlueGreenFlatnodePVCName(parent)
	}
	return st
}

// blueGreenRetiredClusterName is the Cluster to garbage-collect after cutover (not the live one).
func blueGreenRetiredClusterName(parent *nominatimv1alpha1.NominatimInstance) string {
	live := parent.Status.Database.ClusterName
	def := OwnedCNPGClusterName(parent)
	bg := BlueGreenClusterName(parent)
	if live == bg {
		return def
	}
	if live == def {
		return bg
	}
	// Fallback: prefer default name when status is empty/unexpected.
	return def
}

// applyBlueGreenCutover flips serving attachment from blue → green on the parent status.
// API Deployment env follows status.database.connectionSecretName on the next Instance reconcile.
func applyBlueGreenCutover(parent *nominatimv1alpha1.NominatimInstance) {
	if parent.Status.Rebuild == nil {
		return
	}
	rb := parent.Status.Rebuild
	parent.Status.Database.ClusterName = rb.PendingClusterName
	parent.Status.Database.ConnectionSecretName = rb.PendingConnectionSecretName
	rb.ActiveClusterName = rb.PendingClusterName
	rb.ActiveConnectionSecretName = rb.PendingConnectionSecretName
}

// blueGreenJobConnectionSecret returns the Secret name the Rebuild Job must use.
// During BlueGreen import this is the pending (green) Secret, not the live API Secret.
func blueGreenJobConnectionSecret(parent *nominatimv1alpha1.NominatimInstance) string {
	if parent.Status.Rebuild != nil && parent.Status.Rebuild.PendingConnectionSecretName != "" {
		return parent.Status.Rebuild.PendingConnectionSecretName
	}
	return CNPGAppSecretName(BlueGreenClusterName(parent))
}

// ensureBlueGreenPending creates the green Cluster, Database CR, and project(/flatnode) PVCs.
// ready is true when the green Cluster reports Ready, its app Secret exists, and Database is applied.
func (r *NominatimOperationReconciler) ensureBlueGreenPending(
	ctx context.Context,
	op *nominatimv1alpha1.NominatimOperation,
	parent *nominatimv1alpha1.NominatimInstance,
) (ready bool, err error) {
	if parent.Status.Rebuild == nil {
		parent.Status.Rebuild = seedBlueGreenRebuildStatus(parent, op)
		if err := r.patchParentRebuildStatus(ctx, parent); err != nil {
			return false, err
		}
	}
	rb := parent.Status.Rebuild
	pending := rb.PendingClusterName
	if pending == "" {
		pending = BlueGreenClusterName(parent)
		rb.PendingClusterName = pending
		rb.PendingConnectionSecretName = CNPGAppSecretName(pending)
		if err := r.patchParentRebuildStatus(ctx, parent); err != nil {
			return false, err
		}
	}

	if err := ensureOwnedCNPGCluster(ctx, r.Client, r.Scheme, parent, pending); err != nil {
		return false, err
	}
	if err := ensureOwnedCNPGDatabase(ctx, r.Client, r.Scheme, parent, pending); err != nil {
		return false, err
	}

	inst := &NominatimInstanceReconciler{Client: r.Client, Scheme: r.Scheme}
	if _, err := inst.reconcilePVC(ctx, parent, parent.Spec.Project.Volume, BlueGreenProjectPVCName(parent), ComponentProject); err != nil {
		return false, fmt.Errorf("blue/green project PVC: %w", err)
	}
	if parent.Spec.Flatnode != nil {
		if _, err := inst.reconcilePVC(ctx, parent, parent.Spec.Flatnode.Volume, BlueGreenFlatnodePVCName(parent), ComponentFlatnode); err != nil {
			return false, fmt.Errorf("blue/green flatnode PVC: %w", err)
		}
	}

	return r.blueGreenPendingReady(ctx, parent.Namespace, pending)
}

func (r *NominatimOperationReconciler) blueGreenPendingReady(ctx context.Context, namespace, clusterName string) (bool, error) {
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	if err := r.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	conds, found, err := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	clusterReady := false
	for _, raw := range conds {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] == "Ready" && m["status"] == "True" {
			clusterReady = true
			break
		}
	}
	if !clusterReady {
		return false, nil
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: CNPGAppSecretName(clusterName), Namespace: namespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	applied, err := cnpgOwnedDatabaseApplied(ctx, r.Client, namespace, CNPGDatabaseNameForCluster(clusterName))
	if err != nil || !applied {
		return false, err
	}
	return true, nil
}

// reconcileBlueGreenAfterImport advances Cutover → RollbackWindow → GC after the import Job succeeds.
// The NominatimOperation stays non-terminal until rebuild phase Succeeded/Failed.
func (r *NominatimOperationReconciler) reconcileBlueGreenAfterImport(
	ctx context.Context,
	op *nominatimv1alpha1.NominatimOperation,
	parent *nominatimv1alpha1.NominatimInstance,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if parent.Status.Rebuild == nil {
		parent.Status.Rebuild = seedBlueGreenRebuildStatus(parent, op)
	}
	rb := parent.Status.Rebuild

	phase := rb.Phase
	if phase == nominatimv1alpha1.RebuildPhaseImporting || phase == nominatimv1alpha1.RebuildPhaseProvisioning || phase == "" {
		phase = nominatimv1alpha1.RebuildPhaseImporting
	}

	in := blueGreenInput{
		Phase:           phase,
		ActiveCluster:   rb.ActiveClusterName,
		PendingCluster:  rb.PendingClusterName,
		ImportSucceeded: op.Status.Phase != nominatimv1alpha1.NominatimOperationPhaseFailed,
		Failed: op.Status.Phase == nominatimv1alpha1.NominatimOperationPhaseFailed &&
			(rb.Phase == nominatimv1alpha1.RebuildPhaseImporting ||
				rb.Phase == nominatimv1alpha1.RebuildPhaseProvisioning ||
				rb.Phase == nominatimv1alpha1.RebuildPhaseReadyToCutover ||
				rb.Phase == ""),
		APICutoverReady: parent.Status.Database.ConnectionSecretName == rb.PendingConnectionSecretName &&
			parent.Status.Database.ClusterName == rb.PendingClusterName,
		RollbackUntil: rb.RollbackUntil,
		Now:           time.Now().UTC(),
		Message:       rb.Message,
	}
	if in.Failed && in.Message == "" {
		in.Message = op.Status.Message
	}

	retired := blueGreenRetiredClusterName(parent)
	if phase == nominatimv1alpha1.RebuildPhaseGarbageCollect || phase == nominatimv1alpha1.RebuildPhaseRollbackWindow {
		gone, err := r.clusterGone(ctx, parent.Namespace, retired)
		if err != nil {
			return ctrl.Result{}, err
		}
		in.BlueGone = gone
	}

	out, act := advanceBlueGreen(in)
	rb.Phase = out.Phase
	rb.RollbackUntil = out.RollbackUntil
	if out.Message != "" {
		rb.Message = out.Message
	}

	if act.CutoverAPI {
		applyBlueGreenCutover(parent)
		rb = parent.Status.Rebuild
		rb.Phase = out.Phase
		rb.RollbackUntil = out.RollbackUntil
	}

	if act.GarbageCollectBlue {
		if err := r.deleteOwnedClusterBundle(ctx, parent, retired); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("BlueGreen GC requested for retired blue cluster", "cluster", retired)
	}

	if act.GarbageCollectGreen {
		green := rb.PendingClusterName
		if green == "" {
			green = BlueGreenClusterName(parent)
		}
		if err := r.deleteOwnedClusterBundle(ctx, parent, green); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.patchParentRebuildStatus(ctx, parent); err != nil {
		return ctrl.Result{}, err
	}

	switch out.Phase {
	case nominatimv1alpha1.RebuildPhaseSucceeded:
		op.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseSucceeded
		op.Status.Message = "BlueGreen Rebuild succeeded"
		now := metav1.Now()
		op.Status.CompletionTime = &now
		if err := r.Status().Update(ctx, op); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	case nominatimv1alpha1.RebuildPhaseFailed:
		return ctrl.Result{}, r.failOperation(ctx, op, "BlueGreenFailed", rb.Message)
	case nominatimv1alpha1.RebuildPhaseRollbackWindow:
		until := time.Now().UTC().Add(defaultBlueGreenRollbackWindow)
		if rb.RollbackUntil != nil {
			until = rb.RollbackUntil.Time
		}
		delay := time.Until(until)
		if delay < 5*time.Second {
			delay = 5 * time.Second
		}
		op.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseRunning
		op.Status.Message = "BlueGreen rollback window; retiring blue after " + until.UTC().Format(time.RFC3339)
		op.Status.CompletionTime = nil
		if err := r.Status().Update(ctx, op); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: delay}, nil
	default:
		op.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseRunning
		op.Status.Message = "BlueGreen phase " + string(out.Phase)
		op.Status.CompletionTime = nil
		if err := r.Status().Update(ctx, op); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
}

func (r *NominatimOperationReconciler) clusterGone(ctx context.Context, namespace, name string) (bool, error) {
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, cluster)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	return false, err
}

func (r *NominatimOperationReconciler) deleteOwnedClusterBundle(ctx context.Context, parent *nominatimv1alpha1.NominatimInstance, clusterName string) error {
	db := &unstructured.Unstructured{}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	db.SetName(CNPGDatabaseNameForCluster(clusterName))
	db.SetNamespace(parent.Namespace)
	if err := client.IgnoreNotFound(r.Delete(ctx, db)); err != nil {
		return err
	}
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName(clusterName)
	cluster.SetNamespace(parent.Namespace)
	return client.IgnoreNotFound(r.Delete(ctx, cluster))
}

func (r *NominatimOperationReconciler) patchParentRebuildStatus(ctx context.Context, parent *nominatimv1alpha1.NominatimInstance) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		latest := &nominatimv1alpha1.NominatimInstance{}
		if err := r.Get(ctx, types.NamespacedName{Name: parent.Name, Namespace: parent.Namespace}, latest); err != nil {
			return err
		}
		latest.Status.Rebuild = parent.Status.Rebuild
		latest.Status.Database.ClusterName = parent.Status.Database.ClusterName
		latest.Status.Database.ConnectionSecretName = parent.Status.Database.ConnectionSecretName
		latest.Status.Database.Mode = parent.Status.Database.Mode
		latest.Status.Database.Degraded = parent.Status.Database.Degraded
		if err := r.Status().Update(ctx, latest); err != nil {
			return err
		}
		parent.Status = latest.Status
		parent.SetResourceVersion(latest.GetResourceVersion())
		return nil
	})
}
