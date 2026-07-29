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
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
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
	// CNPGEffects optional override for tests; defaults to defaultCNPGEffects (see cnpg_effects.go).
	CNPGEffects CNPGEffects
}

// +kubebuilder:rbac:groups=nominatim.zebernst.dev,resources=nominatimoperations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nominatim.zebernst.dev,resources=nominatimoperations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nominatim.zebernst.dev,resources=nominatimoperations/finalizers,verbs=update
// +kubebuilder:rbac:groups=nominatim.zebernst.dev,resources=nominatims,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile ensures staging PVC + worker Job for a NominatimOperation and syncs status from the Job.
func (r *NominatimOperationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	op := &nominatimv1alpha1.NominatimOperation{}
	if err := r.Get(ctx, req.NamespacedName, op); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !op.DeletionTimestamp.IsZero() {
		return r.reconcileOperationDelete(ctx, op)
	}

	if !controllerutil.ContainsFinalizer(op, nominatimv1alpha1.NominatimOperationFinalizer) {
		controllerutil.AddFinalizer(op, nominatimv1alpha1.NominatimOperationFinalizer)
		if err := r.Update(ctx, op); err != nil {
			return ctrl.Result{}, err
		}
		// Continue in the same pass so callers/tests that reconcile once still progress.
	}

	if isTerminalOperationPhase(op.Status.Phase) {
		// Keep Job-derived status fresh for Succeeded/Failed Jobs; Conflict failures have no Job.
		if op.Status.JobRef != nil {
			if err := r.syncStatusFromJob(ctx, op); err != nil {
				return ctrl.Result{}, err
			}
		}
		// Idempotent: clears any lingering parent activeOperationRefs entry and re-applies
		// resume/runtime CNPG effects (e.g. after an operator restart mid-cleanup).
		if err := r.syncParentSideEffects(ctx, op); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if !isOperationTypeImplemented(op.Spec.Type) {
		return ctrl.Result{}, r.failOperation(ctx, op, reasonNotImplemented,
			fmt.Sprintf("operation type %q is reserved but not implemented yet (no Job will be created)", op.Spec.Type))
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

	if requiresRegionGate(op.Spec.Type) {
		if len(effectiveRegions(op, parent)) == 0 {
			return ctrl.Result{}, r.failOperation(ctx, op, reasonRegionsRequired,
				fmt.Sprintf("no regions configured on Operation %q or Nominatim %q", op.Name, parent.Name))
		}
		if len(parent.Spec.Regions) > 0 && !bootstrapComplete(parent, peers.Items) {
			return ctrl.Result{}, r.failOperation(ctx, op, reasonBootstrapIncomplete,
				fmt.Sprintf("Nominatim %q has not completed a Bootstrap Operation", parent.Name))
		}
	}

	staging := resolveStagingSpec(op, parent)
	if err := r.ensureStagingPVC(ctx, op, staging); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.applyPreJobCNPGEffects(ctx, op, parent); err != nil {
		return ctrl.Result{}, err
	}

	if result, wait, err := r.waitForJobPrerequisites(ctx, op, parent); wait || err != nil {
		return result, err
	}

	if err := r.ensureJob(ctx, op, parent); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.syncStatusFromJob(ctx, op); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.syncParentSideEffects(ctx, op); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// waitForJobPrerequisites blocks Job creation until the connection Secret exists, the CNPG
// Cluster/Database is ready, and (for Reimport) the owned Database has been drop/recreated.
// wait=true means the caller should return result without creating the Job.
func (r *NominatimOperationReconciler) waitForJobPrerequisites(
	ctx context.Context,
	op *nominatimv1alpha1.NominatimOperation,
	parent *nominatimv1alpha1.Nominatim,
) (result ctrl.Result, wait bool, err error) {
	log := logf.FromContext(ctx)

	if parent.Status.Database.ConnectionSecretName == "" {
		log.Info("waiting for parent status.database.connectionSecretName before creating Job",
			"nominatim", parent.Name)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, true, nil
	}

	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{
		Name:      parent.Status.Database.ConnectionSecretName,
		Namespace: parent.Namespace,
	}
	if err := r.Get(ctx, secretKey, secret); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("waiting for database connection Secret before creating Job",
				"secret", secretKey.Name, "nominatim", parent.Name)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, true, nil
		}
		return ctrl.Result{}, false, err
	}

	if ready, err := r.cnpgClusterReadyForJobs(ctx, parent); err != nil {
		return ctrl.Result{}, false, err
	} else if !ready {
		log.Info("waiting for CNPG Cluster/Database readiness before creating Job",
			"nominatim", parent.Name, "cluster", parent.Status.Database.ClusterName,
			"mode", parent.Status.Database.Mode)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, true, nil
	}

	if ready, err := r.ensureReimportDatabaseReset(ctx, op, parent); err != nil {
		return ctrl.Result{}, false, err
	} else if !ready {
		log.Info("waiting for owned CNPG Database drop/recreate before Reimport Job",
			"nominatim", parent.Name, "database", OwnedCNPGDatabaseName(parent))
		return ctrl.Result{RequeueAfter: 5 * time.Second}, true, nil
	}

	return ctrl.Result{}, false, nil
}

// reconcileOperationDelete clears parent ActiveOperationRefs and resumes CNPG side-effects
// (when no matching sibling remains) before removing the Operation finalizer. Without this,
// kubectl-delete of a Running Bootstrap permanently orphans pause-state and suspends the API.
func (r *NominatimOperationReconciler) reconcileOperationDelete(ctx context.Context, op *nominatimv1alpha1.NominatimOperation) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(op, nominatimv1alpha1.NominatimOperationFinalizer) {
		return ctrl.Result{}, nil
	}

	if err := r.syncParentActiveOperationRef(ctx, op, false); err != nil {
		return ctrl.Result{}, err
	}

	if op.Spec.NominatimRef.Name != "" {
		parent := &nominatimv1alpha1.Nominatim{}
		key := types.NamespacedName{Name: op.Spec.NominatimRef.Name, Namespace: op.Namespace}
		if err := r.Get(ctx, key, parent); err != nil {
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		} else if err := r.applyTerminalCNPGEffects(ctx, op, parent); err != nil {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(op, nominatimv1alpha1.NominatimOperationFinalizer)
	if err := r.Update(ctx, op); err != nil {
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
	desired, err := buildOperationJob(op, parent, stagingPVCName(op), workerImageForOperation(op, parent), workerPullPolicyForOperation(op, parent))
	if err != nil {
		return fmt.Errorf("build Operation Job: %w", err)
	}
	if err := controllerutil.SetControllerReference(op, desired, r.Scheme); err != nil {
		return err
	}

	existing := &batchv1.Job{}
	err = r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if err == nil {
		// Jobs are mostly immutable. If an earlier race created a Job without DB env,
		// delete it so the next reconcile can recreate with NOMINATIM_DATABASE_DSN.
		if existing.Status.Succeeded == 0 && !jobHasDatabaseDSN(existing) {
			policy := metav1.DeletePropagationBackground
			if err := r.Delete(ctx, existing, &client.DeleteOptions{PropagationPolicy: &policy}); err != nil {
				return err
			}
			return fmt.Errorf("deleted Job %q missing NOMINATIM_DATABASE_DSN; will recreate", existing.Name)
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return r.Create(ctx, desired)
}

func jobHasDatabaseDSN(job *batchv1.Job) bool {
	for _, c := range job.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == "NOMINATIM_DATABASE_DSN" {
				return true
			}
		}
	}
	return false
}

// cnpgClusterReadyForJobs reports whether Jobs that talk to Postgres may start.
// Connection-secret / degraded mode (no Cluster to wait on) is always ready.
// Owned/attached Clusters must report Ready=True; owned Clusters also wait for the
// declarative Database CR (extensions) to report status.applied=true.
func (r *NominatimOperationReconciler) cnpgClusterReadyForJobs(ctx context.Context, parent *nominatimv1alpha1.Nominatim) (bool, error) {
	if parent.Status.Database.Degraded || parent.Status.Database.Mode == nominatimv1alpha1.DatabaseModeConnectionSecret {
		return true, nil
	}

	// Prefer status; fall back to the owned name from Spec so we never treat an
	// in-progress cluster-create as "ready" just because ClusterName is unset.
	clusterName := parent.Status.Database.ClusterName
	ownedCluster := parent.Spec.Database.Cluster != nil
	if clusterName == "" && ownedCluster {
		clusterName = OwnedCNPGClusterName(parent)
	}
	if clusterName == "" {
		// Attached/ref mode without a name yet — wait for database status.
		if parent.Spec.Database.ClusterRef != nil {
			return false, nil
		}
		return true, nil
	}

	cluster, err := getCNPGCluster(ctx, r.Client, parent.Namespace, clusterName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	conds, found, err := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	if err != nil || !found {
		return false, err
	}
	clusterReady := false
	for _, raw := range conds {
		cond, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "Ready" && cond["status"] == "True" {
			clusterReady = true
			break
		}
	}
	if !clusterReady {
		return false, nil
	}

	// Wait for Database CR when we own the Cluster (by Spec or Mode). Spec wins so a
	// status race cannot start Bootstrap before extensions/roles are applied.
	if ownedCluster || parent.Status.Database.Mode == nominatimv1alpha1.DatabaseModeClusterManaged {
		return cnpgOwnedDatabaseApplied(ctx, r.Client, parent.Namespace, OwnedCNPGDatabaseName(parent))
	}
	return true, nil
}

// cnpgOwnedDatabaseApplied is true when the owned CNPG Database CR has reconciled
// (status.applied=true), including declarative extensions.
func cnpgOwnedDatabaseApplied(ctx context.Context, c client.Client, namespace, name string) (bool, error) {
	db := &unstructured.Unstructured{}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, db); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	applied, found, err := unstructured.NestedBool(db.Object, "status", "applied")
	if err != nil || !found {
		return false, err
	}
	return applied, nil
}

const (
	annotationReimportDBReset   = "nominatim.zebernst.dev/reimport-db-reset"
	annotationReimportDBPrevUID = "nominatim.zebernst.dev/reimport-db-prev-uid"
	reimportDBResetPending      = "pending"
	reimportDBResetDone         = "done"
	reimportDBPrevUIDNone       = "none"
)

// ensureReimportDatabaseReset drops and recreates the owned CNPG Database CR before a
// Reimport Job so PostGIS/hstore are reinstalled by CNPG (superuser) on an empty DB.
// Non-Reimport / non-owned-cluster modes are a no-op (ready=true).
func (r *NominatimOperationReconciler) ensureReimportDatabaseReset(
	ctx context.Context,
	op *nominatimv1alpha1.NominatimOperation,
	parent *nominatimv1alpha1.Nominatim,
) (bool, error) {
	if op.Spec.Type != nominatimv1alpha1.NominatimOperationReimport {
		return true, nil
	}
	owned := parent.Spec.Database.Cluster != nil ||
		parent.Status.Database.Mode == nominatimv1alpha1.DatabaseModeClusterManaged
	if !owned {
		return true, nil
	}

	if op.Annotations[annotationReimportDBReset] == reimportDBResetDone {
		return true, nil
	}

	dbName := OwnedCNPGDatabaseName(parent)
	clusterName := parent.Status.Database.ClusterName
	if clusterName == "" {
		clusterName = OwnedCNPGClusterName(parent)
	}

	db := &unstructured.Unstructured{}
	db.SetGroupVersionKind(CNPGDatabaseGVK)
	err := r.Get(ctx, types.NamespacedName{Name: dbName, Namespace: parent.Namespace}, db)
	prevUID := op.Annotations[annotationReimportDBPrevUID]

	switch {
	case prevUID == "":
		// First pass: record current UID (or none) and delete so CNPG drops the database.
		if apierrors.IsNotFound(err) {
			if err := r.patchReimportResetAnnotations(ctx, op, reimportDBResetPending, reimportDBPrevUIDNone); err != nil {
				return false, err
			}
			return false, nil
		}
		if err != nil {
			return false, err
		}
		uid := string(db.GetUID())
		if err := ensureDatabaseReclaimDelete(ctx, r.Client, db); err != nil {
			return false, err
		}
		// Re-get after reclaim patch so Delete uses the latest resourceVersion.
		fresh := &unstructured.Unstructured{}
		fresh.SetGroupVersionKind(CNPGDatabaseGVK)
		if err := r.Get(ctx, types.NamespacedName{Name: dbName, Namespace: parent.Namespace}, fresh); err != nil {
			if apierrors.IsNotFound(err) {
				if err := r.patchReimportResetAnnotations(ctx, op, reimportDBResetPending, uid); err != nil {
					return false, err
				}
				return false, nil
			}
			return false, err
		}
		if err := r.Delete(ctx, fresh); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete owned CNPG Database %q for Reimport: %w", dbName, err)
		}
		if err := r.patchReimportResetAnnotations(ctx, op, reimportDBResetPending, uid); err != nil {
			return false, err
		}
		return false, nil

	case apierrors.IsNotFound(err):
		// Deleted (or never present): recreate and wait for applied.
		if err := ensureOwnedCNPGDatabase(ctx, r.Client, r.Scheme, parent, clusterName); err != nil {
			return false, err
		}
		return false, nil

	case err != nil:
		return false, err

	default:
		// Replacement exists — wait until it is a new object and applied.
		if prevUID != reimportDBPrevUIDNone && string(db.GetUID()) == prevUID {
			return false, nil
		}
		applied, found, aerr := unstructured.NestedBool(db.Object, "status", "applied")
		if aerr != nil {
			return false, aerr
		}
		if !found || !applied {
			return false, nil
		}
		if err := r.patchReimportResetAnnotations(ctx, op, reimportDBResetDone, prevUID); err != nil {
			return false, err
		}
		return true, nil
	}
}

func ensureDatabaseReclaimDelete(ctx context.Context, c client.Client, db *unstructured.Unstructured) error {
	reclaim, _, _ := unstructured.NestedString(db.Object, "spec", "databaseReclaimPolicy")
	if reclaim == cnpgDatabaseReclaimDelete {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current := &unstructured.Unstructured{}
		current.SetGroupVersionKind(CNPGDatabaseGVK)
		if err := c.Get(ctx, types.NamespacedName{Name: db.GetName(), Namespace: db.GetNamespace()}, current); err != nil {
			return err
		}
		if err := unstructured.SetNestedField(current.Object, cnpgDatabaseReclaimDelete, "spec", "databaseReclaimPolicy"); err != nil {
			return err
		}
		return c.Update(ctx, current)
	})
}

func (r *NominatimOperationReconciler) patchReimportResetAnnotations(ctx context.Context, op *nominatimv1alpha1.NominatimOperation, reset, prevUID string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		latest := &nominatimv1alpha1.NominatimOperation{}
		if err := r.Get(ctx, types.NamespacedName{Name: op.Name, Namespace: op.Namespace}, latest); err != nil {
			return err
		}
		if latest.Annotations == nil {
			latest.Annotations = map[string]string{}
		}
		latest.Annotations[annotationReimportDBReset] = reset
		latest.Annotations[annotationReimportDBPrevUID] = prevUID
		if err := r.Update(ctx, latest); err != nil {
			return err
		}
		op.Annotations = latest.Annotations
		op.SetResourceVersion(latest.GetResourceVersion())
		return nil
	})
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
	if err := r.Status().Update(ctx, op); err != nil {
		return err
	}
	// Best-effort cleanup: an Operation that fails before ever going active (Conflict,
	// ParentNotFound) never had a ref/pause to undo, but this stays correct and idempotent
	// for the case where it fails after having already gone Pending/Running.
	return r.syncParentSideEffects(ctx, op)
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
