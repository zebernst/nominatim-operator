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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// HTTPRouteGVK is the Gateway API HTTPRoute resource. Referenced as unstructured so the
// operator does not add a go.mod dependency on sigs.k8s.io/gateway-api.
var HTTPRouteGVK = schema.GroupVersionKind{
	Group:   "gateway.networking.k8s.io",
	Version: "v1",
	Kind:    "HTTPRoute",
}

// Default image coordinates and workload component labels.
// Mount paths / volume names share package consts with Operation Jobs (nominatimoperation_resources.go).
const (
	DefaultAPIRepository = "ghcr.io/zebernst/nominatim-api"
	DefaultUIRepository  = "ghcr.io/zebernst/nominatim-ui"
	DefaultImageTag      = "latest"

	ComponentAPI      = "api"
	ComponentUI       = "ui"
	ComponentProject  = "project"
	ComponentFlatnode = "flatnode"

	flatnodeFileEnv = "NOMINATIM_FLATNODE_FILE"

	// apiWorkdirVolumeName is an emptyDir for gunicorn's working directory. The API
	// serving plane must not mount the shared project or flatnode PVCs.
	apiWorkdirVolumeName = "workdir"

	workloadContainerPort = 8080
	workloadServicePort   = 80

	// apiStatusPath is Nominatim's HTTP /status endpoint used for probes.
	apiStatusPath = "/status"
)

// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete

// APIName is the owned API Deployment/Service name for a NominatimInstance.
func APIName(nom *nominatimv1alpha1.NominatimInstance) string { return nom.Name + "-api" }

// UIName is the owned UI Deployment/Service name for a NominatimInstance.
func UIName(nom *nominatimv1alpha1.NominatimInstance) string { return nom.Name + "-ui" }

// ProjectPVCName is the default project PVC name when the template omits metadata.name.
func ProjectPVCName(nom *nominatimv1alpha1.NominatimInstance) string { return nom.Name + "-project" }

// FlatnodePVCName is the default flatnode PVC name when the template omits metadata.name.
func FlatnodePVCName(nom *nominatimv1alpha1.NominatimInstance) string { return nom.Name + "-flatnode" }

// commonLabels returns the standard label set for owned NominatimInstance workload objects.
func commonLabels(nom *nominatimv1alpha1.NominatimInstance, component string) map[string]string {
	labels := map[string]string{
		"app.kubernetes.io/name":     "nominatim",
		"app.kubernetes.io/instance": nom.Name,
	}
	if component != "" {
		labels["app.kubernetes.io/component"] = component
	}
	return labels
}

// reconcileWorkloads reconciles the project/flatnode PVCs and the API/UI serving plane.
// It must run after reconcileDatabase (connection secret known) and preferably after
// ensureBootstrapOperation so status.regions can unlock API/UI in the same pass.
// Project/flatnode PVCs remain for worker Jobs (write plane); the API Deployment does
// not mount them (nominatim-5et.35.1).
func (r *NominatimInstanceReconciler) reconcileWorkloads(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance) error {
	if _, err := r.reconcilePVC(ctx, nom, nom.Spec.Project.Volume, ProjectPVCName(nom), ComponentProject); err != nil {
		return fmt.Errorf("reconcile project volume: %w", err)
	}

	if nom.Spec.Flatnode != nil {
		if _, err := r.reconcilePVC(ctx, nom, nom.Spec.Flatnode.Volume, FlatnodePVCName(nom), ComponentFlatnode); err != nil {
			return fmt.Errorf("reconcile flatnode volume: %w", err)
		}
	}

	if err := r.reconcileAPI(ctx, nom); err != nil {
		return err
	}

	return r.reconcileUI(ctx, nom)
}

// servingWorkloadsAllowed reports whether API/UI Deployments may exist.
// Day-0 Bootstrap must finish first: with desired regions, the serving plane waits
// until status.regions is populated (synced from a Succeeded Bootstrap).
// Instances with no desired regions never Bootstrap, so API/UI may be created
// immediately (smoke / attach-only). suspendDuringOperations does not apply here —
// that knob is day-2 scale-down only.
func servingWorkloadsAllowed(nom *nominatimv1alpha1.NominatimInstance) bool {
	if len(nom.Spec.Regions) == 0 {
		return true
	}
	return len(nom.Status.Regions) > 0
}

// reconcilePVC resolves a VolumeSource into a claim name usable by a Pod spec: it passes
// through an existing ClaimName untouched, or creates (and owns) a PVC from
// VolumeClaimTemplate when ClaimName is empty. Storage size/class are passed through
// verbatim from spec — never hardcoded here.
func (r *NominatimInstanceReconciler) reconcilePVC(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance, vs nominatimv1alpha1.VolumeSource, defaultName, component string) (string, error) {
	if vs.ClaimName != "" {
		return vs.ClaimName, nil
	}
	if vs.VolumeClaimTemplate == nil {
		return "", fmt.Errorf("volume requires claimName and/or volumeClaimTemplate")
	}

	name := vs.VolumeClaimTemplate.Metadata.Name
	if name == "" {
		name = defaultName
	}

	existing := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: nom.Namespace}, existing)
	if err == nil {
		return name, nil
	}
	if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("get PVC %q: %w", name, err)
	}

	labels := commonLabels(nom, component)
	for k, v := range vs.VolumeClaimTemplate.Metadata.Labels {
		labels[k] = v
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   nom.Namespace,
			Labels:      labels,
			Annotations: vs.VolumeClaimTemplate.Metadata.Annotations,
		},
		Spec: vs.VolumeClaimTemplate.Spec,
	}
	if err := controllerutil.SetControllerReference(nom, pvc, r.Scheme); err != nil {
		return "", fmt.Errorf("set controller reference on PVC %q: %w", name, err)
	}
	if err := r.Create(ctx, pvc); err != nil {
		return "", fmt.Errorf("create PVC %q: %w", name, err)
	}
	return name, nil
}

// resolveImage applies the repository default and DefaultImageTag for an optional ImageSpec.
func resolveImage(spec *nominatimv1alpha1.ImageSpec, defaultRepository string) string {
	repo := defaultRepository
	tag := DefaultImageTag
	if spec != nil {
		if spec.Repository != "" {
			repo = spec.Repository
		}
		if spec.Tag != "" {
			tag = spec.Tag
		}
	}
	return repo + ":" + tag
}

// resolvePullPolicy returns the configured pull policy, leaving it empty (API-server
// defaulted) when unset.
func resolvePullPolicy(spec *nominatimv1alpha1.ImageSpec) corev1.PullPolicy {
	if spec != nil {
		return spec.PullPolicy
	}
	return ""
}

// dbEnvVars maps the CNPG/connection-secret conventional keys onto the environment
// variables consumed by the Nominatim API image. Keys are marked optional since
// connectionSecretRef (degraded) secrets are not schema-validated by this operator.
func dbEnvVars(secretName string) []corev1.EnvVar {
	optional := true
	fromKey := func(envName, key string) corev1.EnvVar {
		return corev1.EnvVar{
			Name: envName,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  key,
					Optional:             &optional,
				},
			},
		}
	}
	return []corev1.EnvVar{
		fromKey("NOMINATIM_DATABASE_DSN", "uri"),
		fromKey("PGHOST", "host"),
		fromKey("PGPORT", "port"),
		fromKey("PGDATABASE", "dbname"),
		fromKey("PGUSER", "user"),
		fromKey("PGPASSWORD", "password"),
	}
}

// apiPodFilesystem returns the API pod volumes/mounts/env for a read-only serving
// plane: ephemeral emptyDir workdir plus Nominatim config from the CR (no project
// or flatnode PVCs — those belong to worker Jobs).
func apiPodFilesystem(nom *nominatimv1alpha1.NominatimInstance) ([]corev1.Volume, []corev1.VolumeMount, []corev1.EnvVar) {
	volumes := []corev1.Volume{{
		Name: apiWorkdirVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}}
	mounts := []corev1.VolumeMount{{
		Name:      apiWorkdirVolumeName,
		MountPath: projectMountPath,
	}}
	env := []corev1.EnvVar{
		{Name: "PROJECT_DIR", Value: projectMountPath},
	}
	env = append(env, effectiveNominatimConfigEnv(nom)...)
	env = append(env, effectiveAPIEnv(nom.Spec.API)...)
	return volumes, mounts, env
}

// defaultAPIHTTPProbe returns an HTTP GET probe against the Nominatim /status endpoint.
func defaultAPIHTTPProbe() corev1.ProbeHandler {
	return corev1.ProbeHandler{
		HTTPGet: &corev1.HTTPGetAction{
			Path: apiStatusPath,
			Port: intstr.FromString("http"),
		},
	}
}

// applyDefaultAPIProbes sets startup/readiness/liveness on /status when not already
// provided (e.g. by a user podSpec overlay after merge). Call after mergePodSpecOverlay.
func applyDefaultAPIProbes(c *corev1.Container) {
	handler := defaultAPIHTTPProbe()
	if c.StartupProbe == nil {
		c.StartupProbe = &corev1.Probe{
			ProbeHandler:     handler,
			PeriodSeconds:    5,
			TimeoutSeconds:   3,
			FailureThreshold: 36, // ~3m for pg_isready + placex gate + gunicorn
		}
	}
	if c.ReadinessProbe == nil {
		c.ReadinessProbe = &corev1.Probe{
			ProbeHandler:     handler,
			PeriodSeconds:    10,
			TimeoutSeconds:   5,
			FailureThreshold: 3,
		}
	}
	if c.LivenessProbe == nil {
		c.LivenessProbe = &corev1.Probe{
			ProbeHandler:     handler,
			PeriodSeconds:    30,
			TimeoutSeconds:   5,
			FailureThreshold: 3,
		}
	}
}

// operationImpactMatches reports whether a NominatimOperation of the given type should
// trigger the side effect selected by impact.
func operationImpactMatches(impact nominatimv1alpha1.OperationImpact, opType nominatimv1alpha1.NominatimOperationType) bool {
	switch impact {
	case nominatimv1alpha1.OperationImpactAll:
		return true
	case nominatimv1alpha1.OperationImpactWriteHeavy:
		return isWriteHeavyOperation(opType)
	case nominatimv1alpha1.OperationImpactBootstrapRebuild:
		return opType == nominatimv1alpha1.NominatimOperationBootstrap ||
			opType == nominatimv1alpha1.NominatimOperationRebuild
	case nominatimv1alpha1.OperationImpactNever, "":
		return false
	default:
		return false
	}
}

// shouldSuspendAPI checks the parent's active operation refs against impact, fetching
// each NominatimOperation to inspect its Type. Missing operations (already completed and
// pruned) are skipped rather than treated as errors.
//
// Rebuild always suspends the API regardless of suspendDuringOperations: the Operation
// drops the owned CNPG Database (reclaim=delete) before the worker Job starts, and open
// API connections block DROP DATABASE indefinitely.
func (r *NominatimInstanceReconciler) shouldSuspendAPI(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance, impact nominatimv1alpha1.OperationImpact) (bool, error) {
	if impact == "" {
		impact = nominatimv1alpha1.OperationImpactNever
	}
	for _, ref := range nom.Status.ActiveOperationRefs {
		op := &nominatimv1alpha1.NominatimOperation{}
		err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: nom.Namespace}, op)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("get active operation %q: %w", ref.Name, err)
		}
		if op.Spec.Type == nominatimv1alpha1.NominatimOperationRebuild {
			return true, nil
		}
		if impact == nominatimv1alpha1.OperationImpactNever {
			continue
		}
		if operationImpactMatches(impact, op.Spec.Type) {
			return true, nil
		}
	}
	return false, nil
}

// reconcileAPI reconciles the API Deployment, Service, and optional HTTPRoute.
// Until Bootstrap has populated status.regions (when regions are desired), no API
// objects are created — and any leftover owned API Deployment/Service/HTTPRoute
// are removed. suspendDuringOperations only scales an already-allowed API.
func (r *NominatimInstanceReconciler) reconcileAPI(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance) error {
	if !servingWorkloadsAllowed(nom) {
		return r.deleteServingComponent(ctx, nom, APIName(nom), ComponentAPI)
	}

	if nom.Status.Database.ConnectionSecretName == "" {
		return fmt.Errorf("cannot reconcile API workload: status.database.connectionSecretName is empty")
	}

	apiSpec := nom.Spec.API
	if apiSpec == nil {
		apiSpec = &nominatimv1alpha1.APISpec{}
	}

	suspend, err := r.shouldSuspendAPI(ctx, nom, apiSpec.SuspendDuringOperations)
	if err != nil {
		return fmt.Errorf("evaluate suspendDuringOperations: %w", err)
	}

	replicas := int32(1)
	if apiSpec.Replicas != nil {
		replicas = *apiSpec.Replicas
	}
	if suspend {
		replicas = 0
	}

	name := APIName(nom)
	labels := commonLabels(nom, ComponentAPI)
	volumes, mounts, env := apiPodFilesystem(nom)
	env = append(env, dbEnvVars(nom.Status.Database.ConnectionSecretName)...)

	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nom.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		if err := controllerutil.SetControllerReference(nom, deploy, r.Scheme); err != nil {
			return err
		}
		deploy.Labels = labels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}

		podSpec := corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:            "api",
					Image:           resolveImage(apiSpec.Image, DefaultAPIRepository),
					ImagePullPolicy: resolvePullPolicy(apiSpec.Image),
					Ports: []corev1.ContainerPort{
						{Name: "http", ContainerPort: workloadContainerPort},
					},
					Env:          env,
					VolumeMounts: mounts,
				},
			},
			Volumes: volumes,
		}
		merged, merr := mergePodSpecOverlay(
			podSpec,
			apiSpec.PodSpec,
			"api",
			resolveImage(apiSpec.Image, DefaultAPIRepository),
			resolvePullPolicy(apiSpec.Image),
		)
		if merr != nil {
			return merr
		}
		for i := range merged.Containers {
			if merged.Containers[i].Name == "api" {
				applyDefaultAPIProbes(&merged.Containers[i])
				break
			}
		}
		deploy.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec:       merged,
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconcile API deployment: %w", err)
	}

	if err := r.reconcileService(ctx, nom, name, labels, ComponentAPI); err != nil {
		return fmt.Errorf("reconcile API service: %w", err)
	}

	if apiSpec.Route != nil {
		if err := r.reconcileHTTPRoute(ctx, nom, name, name, apiSpec.Route, ComponentAPI); err != nil {
			return fmt.Errorf("reconcile API HTTPRoute: %w", err)
		}
	}

	return nil
}

// reconcileUI reconciles the optional UI Deployment, Service, and HTTPRoute. It is a
// no-op when spec.ui is unset. Like the API, UI objects are not created until
// Bootstrap has populated status.regions when regions are desired.
func (r *NominatimInstanceReconciler) reconcileUI(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance) error {
	uiSpec := nom.Spec.UI
	if uiSpec == nil {
		// Still clean up a stray UI if Bootstrap is in progress and UI was removed from spec.
		if !servingWorkloadsAllowed(nom) {
			return r.deleteServingComponent(ctx, nom, UIName(nom), ComponentUI)
		}
		return nil
	}

	if !servingWorkloadsAllowed(nom) {
		return r.deleteServingComponent(ctx, nom, UIName(nom), ComponentUI)
	}

	replicas := int32(1)
	if uiSpec.Replicas != nil {
		replicas = *uiSpec.Replicas
	}

	name := UIName(nom)
	labels := commonLabels(nom, ComponentUI)

	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nom.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		if err := controllerutil.SetControllerReference(nom, deploy, r.Scheme); err != nil {
			return err
		}
		deploy.Labels = labels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}

		podSpec := corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:            "ui",
					Image:           resolveImage(uiSpec.Image, DefaultUIRepository),
					ImagePullPolicy: resolvePullPolicy(uiSpec.Image),
					Ports: []corev1.ContainerPort{
						{Name: "http", ContainerPort: workloadContainerPort},
					},
				},
			},
		}
		merged, merr := mergePodSpecOverlay(
			podSpec,
			uiSpec.PodSpec,
			"ui",
			resolveImage(uiSpec.Image, DefaultUIRepository),
			resolvePullPolicy(uiSpec.Image),
		)
		if merr != nil {
			return merr
		}
		deploy.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec:       merged,
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconcile UI deployment: %w", err)
	}

	if err := r.reconcileService(ctx, nom, name, labels, ComponentUI); err != nil {
		return fmt.Errorf("reconcile UI service: %w", err)
	}

	if uiSpec.Route != nil {
		if err := r.reconcileHTTPRoute(ctx, nom, name, name, uiSpec.Route, ComponentUI); err != nil {
			return fmt.Errorf("reconcile UI HTTPRoute: %w", err)
		}
	}

	return nil
}

// deleteServingComponent removes an owned API/UI Deployment, Service, and HTTPRoute
// if present (IgnoreNotFound / no CRD). Used while Bootstrap has not yet unlocked
// the serving plane.
func (r *NominatimInstanceReconciler) deleteServingComponent(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance, name, component string) error {
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nom.Namespace}}
	if err := r.Delete(ctx, deploy); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete %s deployment %q: %w", component, name, err)
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nom.Namespace}}
	if err := r.Delete(ctx, svc); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete %s service %q: %w", component, name, err)
	}
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(HTTPRouteGVK)
	route.SetName(name)
	route.SetNamespace(nom.Namespace)
	if err := r.Delete(ctx, route); err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
		return fmt.Errorf("delete %s HTTPRoute %q: %w", component, name, err)
	}
	return nil
}

// reconcileService reconciles a ClusterIP Service fronting a workload Deployment on
// workloadServicePort (80) -> workloadContainerPort (8080).
func (r *NominatimInstanceReconciler) reconcileService(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance, name string, selector map[string]string, component string) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nom.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := controllerutil.SetControllerReference(nom, svc, r.Scheme); err != nil {
			return err
		}
		svc.Labels = commonLabels(nom, component)
		svc.Spec.Selector = selector
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "http",
				Protocol:   corev1.ProtocolTCP,
				Port:       workloadServicePort,
				TargetPort: intstr.FromInt32(workloadContainerPort),
			},
		}
		return nil
	})
	return err
}

// reconcileHTTPRoute reconciles an unstructured gateway.networking.k8s.io/v1 HTTPRoute
// attaching to route.ParentRefs/Hostnames and backending to serviceName on
// workloadServicePort.
func (r *NominatimInstanceReconciler) reconcileHTTPRoute(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance, name, serviceName string, route *nominatimv1alpha1.RouteSpec, component string) error {
	httpRoute := &unstructured.Unstructured{}
	httpRoute.SetGroupVersionKind(HTTPRouteGVK)
	httpRoute.SetName(name)
	httpRoute.SetNamespace(nom.Namespace)

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, httpRoute, func() error {
		if err := controllerutil.SetControllerReference(nom, httpRoute, r.Scheme); err != nil {
			return err
		}
		httpRoute.SetLabels(commonLabels(nom, component))

		parentRefs := make([]interface{}, 0, len(route.ParentRefs))
		for _, ref := range route.ParentRefs {
			pr := map[string]interface{}{"name": ref.Name}
			if ref.Group != nil {
				pr["group"] = *ref.Group
			}
			if ref.Kind != nil {
				pr["kind"] = *ref.Kind
			}
			if ref.Namespace != nil {
				pr["namespace"] = *ref.Namespace
			}
			if ref.SectionName != nil {
				pr["sectionName"] = *ref.SectionName
			}
			parentRefs = append(parentRefs, pr)
		}
		if err := unstructured.SetNestedSlice(httpRoute.Object, parentRefs, "spec", "parentRefs"); err != nil {
			return err
		}

		if len(route.Hostnames) > 0 {
			hostnames := make([]interface{}, len(route.Hostnames))
			for i, h := range route.Hostnames {
				hostnames[i] = h
			}
			// "spec" is already a validated map from the parentRefs write above, so this
			// cannot fail (mirrors the equivalent pattern in nominatim_database.go).
			_ = unstructured.SetNestedSlice(httpRoute.Object, hostnames, "spec", "hostnames")
		} else {
			unstructured.RemoveNestedField(httpRoute.Object, "spec", "hostnames")
		}

		rules := []interface{}{
			map[string]interface{}{
				"backendRefs": []interface{}{
					map[string]interface{}{
						"name": serviceName,
						"port": int64(workloadServicePort),
					},
				},
			},
		}
		return unstructured.SetNestedSlice(httpRoute.Object, rules, "spec", "rules")
	})
	return err
}
