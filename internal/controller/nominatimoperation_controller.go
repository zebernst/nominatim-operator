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
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// NominatimOperationReconciler reconciles a NominatimOperation object
type NominatimOperationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=nominatim.zebernst.dev,resources=nominatimoperations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nominatim.zebernst.dev,resources=nominatimoperations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nominatim.zebernst.dev,resources=nominatimoperations/finalizers,verbs=update
// +kubebuilder:rbac:groups=nominatim.zebernst.dev,resources=nominatims,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete

// Reconcile ensures staging PVC + worker Job for a NominatimOperation and syncs status from the Job.
func (r *NominatimOperationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	op := &nominatimv1alpha1.NominatimOperation{}
	if err := r.Get(ctx, req.NamespacedName, op); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if isTerminalOperationPhase(op.Status.Phase) {
		// Keep Job-derived status fresh for Succeeded/Failed Jobs; Conflict failures have no Job.
		if op.Status.JobRef != nil {
			if err := r.syncStatusFromJob(ctx, op); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	parent := &nominatimv1alpha1.Nominatim{}
	parentKey := types.NamespacedName{Name: op.Spec.NominatimRef.Name, Namespace: op.Namespace}
	if err := r.Get(ctx, parentKey, parent); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.failOperation(ctx, op, reasonParentNotFound,
				fmt.Sprintf("Nominatim %q not found in namespace %q", op.Spec.NominatimRef.Name, op.Namespace))
		}
		return ctrl.Result{}, err
	}

	if err := r.ensureOperationOwnerRef(ctx, op, parent); err != nil {
		return ctrl.Result{}, err
	}

	peers := &nominatimv1alpha1.NominatimOperationList{}
	if err := r.List(ctx, peers, client.InNamespace(op.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	if conflict := findConflictingOperation(op, peers.Items); conflict != nil {
		log.Info("operation conflict", "operation", op.Name, "peer", conflict.Name)
		return ctrl.Result{}, r.failOperation(ctx, op, reasonConflict, conflictMessage(conflict))
	}

	staging := resolveStagingSpec(op, parent)
	if err := r.ensureStagingPVC(ctx, op, staging); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ensureJob(ctx, op, parent); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.syncStatusFromJob(ctx, op); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *NominatimOperationReconciler) ensureOperationOwnerRef(ctx context.Context, op *nominatimv1alpha1.NominatimOperation, parent *nominatimv1alpha1.Nominatim) error {
	before := op.DeepCopy()
	if err := controllerutil.SetControllerReference(parent, op, r.Scheme); err != nil {
		return err
	}
	if equality.Semantic.DeepEqual(before.OwnerReferences, op.OwnerReferences) {
		return nil
	}
	return r.Update(ctx, op)
}

func (r *NominatimOperationReconciler) ensureStagingPVC(ctx context.Context, op *nominatimv1alpha1.NominatimOperation, staging nominatimv1alpha1.StagingSpec) error {
	desired := buildStagingPVC(op, staging)
	if err := controllerutil.SetControllerReference(op, desired, r.Scheme); err != nil {
		return err
	}

	existing := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return r.Create(ctx, desired)
}

func (r *NominatimOperationReconciler) ensureJob(ctx context.Context, op *nominatimv1alpha1.NominatimOperation, parent *nominatimv1alpha1.Nominatim) error {
	desired := buildOperationJob(op, parent, stagingPVCName(op), workerImageForOperation(op))
	if err := controllerutil.SetControllerReference(op, desired, r.Scheme); err != nil {
		return err
	}

	existing := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return r.Create(ctx, desired)
}

func (r *NominatimOperationReconciler) syncStatusFromJob(ctx context.Context, op *nominatimv1alpha1.NominatimOperation) error {
	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Name: op.Name, Namespace: op.Namespace}, job); err != nil {
		return client.IgnoreNotFound(err)
	}

	phase := phaseFromJob(job.Status.Succeeded, job.Status.Failed, job.Status.Active)
	op.Status.Phase = phase
	op.Status.JobRef = &nominatimv1alpha1.LocalObjectReference{Name: job.Name}
	op.Status.ObservedGeneration = op.Generation

	if job.Status.StartTime != nil {
		op.Status.StartTime = job.Status.StartTime
	}
	if phase == nominatimv1alpha1.NominatimOperationPhaseSucceeded || phase == nominatimv1alpha1.NominatimOperationPhaseFailed {
		if job.Status.CompletionTime != nil {
			op.Status.CompletionTime = job.Status.CompletionTime
		} else if op.Status.CompletionTime == nil {
			now := metav1.Now()
			op.Status.CompletionTime = &now
		}
		if phase == nominatimv1alpha1.NominatimOperationPhaseFailed {
			op.Status.Message = "Job failed"
		} else {
			op.Status.Message = "Job succeeded"
		}
	} else if phase == nominatimv1alpha1.NominatimOperationPhaseRunning {
		op.Status.Message = "Job running"
		op.Status.CompletionTime = nil
	} else {
		op.Status.Message = "Job pending"
		op.Status.CompletionTime = nil
	}

	return r.Status().Update(ctx, op)
}

func (r *NominatimOperationReconciler) failOperation(ctx context.Context, op *nominatimv1alpha1.NominatimOperation, reason, message string) error {
	now := metav1.Now()
	op.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseFailed
	if reason != "" && !strings.HasPrefix(message, reason) {
		op.Status.Message = reason + ": " + message
	} else {
		op.Status.Message = message
	}
	op.Status.ObservedGeneration = op.Generation
	op.Status.CompletionTime = &now
	if op.Status.StartTime == nil {
		op.Status.StartTime = &now
	}
	return r.Status().Update(ctx, op)
}

// SetupWithManager sets up the controller with the Manager.
func (r *NominatimOperationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nominatimv1alpha1.NominatimOperation{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Named("nominatimoperation").
		Complete(r)
}
