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
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// NominatimReconciler reconciles a Nominatim object
type NominatimReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// ControllerName overrides the controller-runtime name (tests only).
	ControllerName string
}

// +kubebuilder:rbac:groups=nominatim.zebernst.dev,resources=nominatims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nominatim.zebernst.dev,resources=nominatims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nominatim.zebernst.dev,resources=nominatims/finalizers,verbs=update
// +kubebuilder:rbac:groups=nominatim.zebernst.dev,resources=nominatimoperations,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=postgresql.cnpg.io,resources=clusters,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=postgresql.cnpg.io,resources=databases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile runs database attach, serving-plane workloads, and status conditions.
func (r *NominatimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	nom := &nominatimv1alpha1.Nominatim{}
	if err := r.Get(ctx, req.NamespacedName, nom); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !nom.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, nom)
	}

	if !controllerutil.ContainsFinalizer(nom, nominatimv1alpha1.NominatimFinalizer) {
		controllerutil.AddFinalizer(nom, nominatimv1alpha1.NominatimFinalizer)
		if err := r.Update(ctx, nom); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if err := r.reconcileDatabase(ctx, nom); err != nil {
		log.Error(err, "failed to reconcile Nominatim database")
		return ctrl.Result{}, err
	}

	// Persist database status before creating Operations/Jobs that read it from the API.
	// reconcileBootstrap creates NominatimOperations immediately; without this write the
	// Operation controller can race and build a worker Job with no NOMINATIM_DATABASE_DSN.
	dbStatus := nom.Status.Database
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		latest := &nominatimv1alpha1.Nominatim{}
		if err := r.Get(ctx, req.NamespacedName, latest); err != nil {
			return err
		}
		latest.Status.Database = dbStatus
		if err := r.Status().Update(ctx, latest); err != nil {
			return err
		}
		// Keep the in-memory object aligned with the version we just wrote so a later
		// syncStatus Status().Update does not conflict on a stale resourceVersion.
		nom.Status = latest.Status
		nom.SetResourceVersion(latest.GetResourceVersion())
		return nil
	}); err != nil {
		log.Error(err, "failed to persist Nominatim database status")
		return ctrl.Result{}, err
	}

	ops, err := r.listOperationsForParent(ctx, nom)
	if err != nil {
		log.Error(err, "failed to list NominatimOperations for parent")
		return ctrl.Result{}, err
	}
	observeRegionsFromSucceededOps(nom, ops)

	// Ensure Bootstrap before serving workloads so status.regions (observed above)
	// can unlock API/UI creation in the same reconcile pass.
	if err := r.ensureBootstrapOperation(ctx, nom, ops); err != nil {
		log.Error(err, "failed to reconcile Nominatim bootstrap")
		return ctrl.Result{}, err
	}

	if err := r.reconcileWorkloads(ctx, nom); err != nil {
		log.Error(err, "failed to reconcile Nominatim workloads")
		return ctrl.Result{}, err
	}

	if err := r.reconcileRegionDrift(ctx, nom); err != nil {
		log.Error(err, "failed to reconcile Nominatim region drift")
		return ctrl.Result{}, err
	}

	if err := r.reconcileSequenceObservation(ctx, nom); err != nil {
		log.Error(err, "failed to reconcile Nominatim sequence observation")
		return ctrl.Result{}, err
	}

	updateResult, err := r.reconcileUpdates(ctx, nom)
	if err != nil {
		log.Error(err, "failed to reconcile Nominatim scheduled updates")
		return ctrl.Result{}, err
	}

	if err := r.syncStatus(ctx, nom); err != nil {
		log.Error(err, "failed to update Nominatim status")
		return ctrl.Result{}, err
	}

	return updateResult, nil
}

func (r *NominatimReconciler) reconcileDelete(ctx context.Context, nom *nominatimv1alpha1.Nominatim) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(nom, nominatimv1alpha1.NominatimFinalizer) {
		return ctrl.Result{}, nil
	}
	// Stub: block deletion while active operations are referenced; later tasks drain Jobs.
	if len(nom.Status.ActiveOperationRefs) > 0 {
		return ctrl.Result{}, fmt.Errorf("cannot remove finalizer while %d active operation(s) remain", len(nom.Status.ActiveOperationRefs))
	}
	controllerutil.RemoveFinalizer(nom, nominatimv1alpha1.NominatimFinalizer)
	if err := r.Update(ctx, nom); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *NominatimReconciler) syncStatus(ctx context.Context, nom *nominatimv1alpha1.Nominatim) error {
	desired := append([]string(nil), nom.Spec.Regions...)
	observed := make([]string, 0, len(nom.Status.Regions))
	for _, rs := range nom.Status.Regions {
		observed = append(observed, rs.Name)
	}

	missing := difference(desired, observed)
	removed := difference(observed, desired)

	now := metav1.Now()
	conds := slices.Clone(nom.Status.Conditions)

	if len(removed) > 0 {
		meta.SetStatusCondition(&conds, metav1.Condition{
			Type:               nominatimv1alpha1.ConditionRegionRemovalUnsupported,
			Status:             metav1.ConditionTrue,
			Reason:             "RegionsRemovedFromSpec",
			Message:            fmt.Sprintf("regions removed from spec but still present in status (DB data is not deleted): %v", removed),
			ObservedGeneration: nom.Generation,
			LastTransitionTime: now,
		})
	} else {
		meta.SetStatusCondition(&conds, metav1.Condition{
			Type:               nominatimv1alpha1.ConditionRegionRemovalUnsupported,
			Status:             metav1.ConditionFalse,
			Reason:             "NoUnsupportedRemovals",
			Message:            "no observed regions are missing from spec",
			ObservedGeneration: nom.Generation,
			LastTransitionTime: now,
		})
	}

	if len(missing) > 0 {
		meta.SetStatusCondition(&conds, metav1.Condition{
			Type:               nominatimv1alpha1.ConditionRegionsDrift,
			Status:             metav1.ConditionTrue,
			Reason:             "DesiredRegionsNotImported",
			Message:            fmt.Sprintf("desired regions not yet in status.regions: %v", missing),
			ObservedGeneration: nom.Generation,
			LastTransitionTime: now,
		})
	} else {
		meta.SetStatusCondition(&conds, metav1.Condition{
			Type:               nominatimv1alpha1.ConditionRegionsDrift,
			Status:             metav1.ConditionFalse,
			Reason:             "RegionsAligned",
			Message:            "desired regions are present in status.regions",
			ObservedGeneration: nom.Generation,
			LastTransitionTime: now,
		})
	}

	// Stub readiness: Progressing while bootstrap/import needed; Ready when desired==observed (incl. both empty).
	needsImport := len(desired) > 0 && len(missing) > 0
	if needsImport || len(removed) > 0 {
		meta.SetStatusCondition(&conds, metav1.Condition{
			Type:               nominatimv1alpha1.ConditionProgressing,
			Status:             metav1.ConditionTrue,
			Reason:             "ReconcilingRegions",
			Message:            "instance is progressing toward desired region set (operations wired in later tasks)",
			ObservedGeneration: nom.Generation,
			LastTransitionTime: now,
		})
		meta.SetStatusCondition(&conds, metav1.Condition{
			Type:               nominatimv1alpha1.ConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             "NotReady",
			Message:            "desired regions are not fully reflected in status",
			ObservedGeneration: nom.Generation,
			LastTransitionTime: now,
		})
	} else {
		meta.SetStatusCondition(&conds, metav1.Condition{
			Type:               nominatimv1alpha1.ConditionProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             "Idle",
			Message:            "no region import work pending from status comparison",
			ObservedGeneration: nom.Generation,
			LastTransitionTime: now,
		})
		meta.SetStatusCondition(&conds, metav1.Condition{
			Type:               nominatimv1alpha1.ConditionReady,
			Status:             metav1.ConditionTrue,
			Reason:             "RegionsSynced",
			Message:            "desired and observed region sets match (workload readiness deferred)",
			ObservedGeneration: nom.Generation,
			LastTransitionTime: now,
		})
	}

	nom.Status.Conditions = conds
	nom.Status.ObservedGeneration = nom.Generation
	syncImportConfigDriftCondition(nom)
	return r.Status().Update(ctx, nom)
}

// difference returns elements in a that are not in b.
func difference(a, b []string) []string {
	set := make(map[string]struct{}, len(b))
	for _, x := range b {
		set[x] = struct{}{}
	}
	var out []string
	for _, x := range a {
		if _, ok := set[x]; !ok {
			out = append(out, x)
		}
	}
	return out
}

// mapOperationToNominatim enqueues the parent Nominatim named by nominatimRef.
func mapOperationToNominatim(_ context.Context, obj client.Object) []reconcile.Request {
	op, ok := obj.(*nominatimv1alpha1.NominatimOperation)
	if !ok || op.Spec.NominatimRef.Name == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Name:      op.Spec.NominatimRef.Name,
			Namespace: op.Namespace,
		},
	}}
}

// mapCNPGClusterToNominatim enqueues owning Nominatim resources and same-namespace clusterRef users.
func mapCNPGClusterToNominatim(c client.Client) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil
		}
		var reqs []reconcile.Request
		seen := map[types.NamespacedName]struct{}{}
		add := func(nn types.NamespacedName) {
			if _, ok := seen[nn]; ok {
				return
			}
			seen[nn] = struct{}{}
			reqs = append(reqs, reconcile.Request{NamespacedName: nn})
		}
		for _, ref := range u.GetOwnerReferences() {
			if ref.Kind == "Nominatim" && ref.APIVersion == nominatimv1alpha1.GroupVersion.String() {
				add(types.NamespacedName{Name: ref.Name, Namespace: u.GetNamespace()})
			}
		}
		if c == nil {
			return reqs
		}
		list := &nominatimv1alpha1.NominatimList{}
		if err := c.List(ctx, list, client.InNamespace(u.GetNamespace())); err != nil {
			return reqs
		}
		for i := range list.Items {
			nom := &list.Items[i]
			if nom.Spec.Database.ClusterRef != nil && nom.Spec.Database.ClusterRef.Name == u.GetName() {
				add(types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace})
			}
		}
		return reqs
	}
}

// SetupWithManager sets up the controller with the Manager.
// Optional GVKs (Gateway API HTTPRoute, CNPG Cluster) are registered only when their
// CRDs exist so the manager can start on clusters that omit those APIs (e.g. CI kind smoke).
func (r *NominatimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return r.setupWithManager(mgr, mgr.GetRESTMapper())
}

func (r *NominatimReconciler) setupWithManager(mgr ctrl.Manager, mapper meta.RESTMapper) error {
	name := "nominatim"
	if r.ControllerName != "" {
		name = r.ControllerName
	}
	b := ctrl.NewControllerManagedBy(mgr).
		For(&nominatimv1alpha1.Nominatim{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.ConfigMap{}).
		Watches(
			&nominatimv1alpha1.NominatimOperation{},
			handler.EnqueueRequestsFromMapFunc(mapOperationToNominatim),
		).
		Named(name)

	if ok, err := gvkAvailableFromMapper(mapper, HTTPRouteGVK); err != nil {
		return err
	} else if ok {
		httpRoute := &unstructured.Unstructured{}
		httpRoute.SetGroupVersionKind(HTTPRouteGVK)
		b = b.Owns(httpRoute)
	}

	if ok, err := gvkAvailableFromMapper(mapper, CNPGClusterGVK); err != nil {
		return err
	} else if ok {
		cnpgCluster := &unstructured.Unstructured{}
		cnpgCluster.SetGroupVersionKind(CNPGClusterGVK)
		b = b.Watches(
			cnpgCluster,
			handler.EnqueueRequestsFromMapFunc(mapCNPGClusterToNominatim(r.Client)),
		)
	}

	if ok, err := gvkAvailableFromMapper(mapper, CNPGDatabaseGVK); err != nil {
		return err
	} else if ok {
		cnpgDB := &unstructured.Unstructured{}
		cnpgDB.SetGroupVersionKind(CNPGDatabaseGVK)
		b = b.Owns(cnpgDB)
	}

	return b.Complete(r)
}

// gvkAvailableFromMapper reports whether mapper knows about gvk.
func gvkAvailableFromMapper(mapper meta.RESTMapper, gvk schema.GroupVersionKind) (bool, error) {
	_, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		if meta.IsNoMatchError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
