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
	"encoding/json"
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// kindOf identifies the workload object kind for targeted failure injection in tests.
func kindOf(obj client.Object) string {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		return "Deployment"
	case *corev1.Service:
		return "Service"
	case *corev1.PersistentVolumeClaim:
		return "PersistentVolumeClaim"
	case *nominatimv1alpha1.NominatimOperation:
		return "NominatimOperation"
	case *unstructured.Unstructured:
		return o.GetKind()
	}
	return ""
}

// failSpec targets a specific object kind (+ optional exact name) for simulated failures.
type failSpec struct {
	kind string
	name string
}

func (f failSpec) matches(obj client.Object) bool {
	if f.kind != kindOf(obj) {
		return false
	}
	return f.name == "" || f.name == obj.GetName()
}

// failingClient wraps a real client.Client to inject targeted Create/Get failures for
// exercising error-handling branches that are otherwise hard to reach with a fake client.
type failingClient struct {
	client.Client
	failCreate []failSpec
	failGet    map[string]error
}

func (f *failingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	for _, fs := range f.failCreate {
		if fs.matches(obj) {
			return fmt.Errorf("simulated create failure for %s/%s", kindOf(obj), obj.GetName())
		}
	}
	return f.Client.Create(ctx, obj, opts...)
}

func (f *failingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err, ok := f.failGet[key.Name]; ok {
		return err
	}
	return f.Client.Get(ctx, key, obj, opts...)
}

// emptySchemeReconciler returns a reconciler whose scheme has no Nominatim types
// registered, so controllerutil.SetControllerReference always fails (matches the
// existing nominatim_database_test.go pattern for exercising that branch). The Nominatim
// object passed to reconcile calls is only used in-memory and must not be seeded into the
// fake client, since the client tracker would itself reject an unregistered type.
func emptySchemeReconciler() *NominatimReconciler {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	return &NominatimReconciler{Client: c, Scheme: s}
}

const testConnectionSecretName = "pg-secret"

func nominatimWithConnectionSecret(name string) *nominatimv1alpha1.Nominatim {
	nom := baseNominatim(name)
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{Name: testConnectionSecretName},
	}
	return nom
}

func TestReconcilePVC_ClaimNamePassthrough(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("pvc-claimname")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	name, err := r.reconcilePVC(context.Background(), nom, nominatimv1alpha1.VolumeSource{ClaimName: "existing-pvc"}, "default-name", ComponentProject)
	if err != nil {
		t.Fatalf("reconcilePVC: %v", err)
	}
	if name != "existing-pvc" {
		t.Fatalf("name=%q want existing-pvc", name)
	}

	// Must not create a PVC object when using an existing claim name.
	got := &corev1.PersistentVolumeClaim{}
	err = c.Get(context.Background(), types.NamespacedName{Name: "existing-pvc", Namespace: "default"}, got)
	if err == nil {
		t.Fatal("expected no PVC to be created for claimName passthrough")
	}
}

func TestReconcilePVC_CreateFromTemplate_NoHardcodedStorageClass(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("pvc-template")
	sc := "premium-nvme"
	nom.Spec.Project.Volume = nominatimv1alpha1.VolumeSource{
		VolumeClaimTemplate: &nominatimv1alpha1.VolumeClaimTemplate{
			Spec: corev1.PersistentVolumeClaimSpec{
				StorageClassName: &sc,
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	name, err := r.reconcilePVC(context.Background(), nom, nom.Spec.Project.Volume, ProjectPVCName(nom), ComponentProject)
	if err != nil {
		t.Fatalf("reconcilePVC: %v", err)
	}
	wantName := ProjectPVCName(nom)
	if name != wantName {
		t.Fatalf("name=%q want %q", name, wantName)
	}

	got := &corev1.PersistentVolumeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: wantName, Namespace: "default"}, got); err != nil {
		t.Fatalf("get created PVC: %v", err)
	}
	if got.Spec.StorageClassName == nil || *got.Spec.StorageClassName != "premium-nvme" {
		t.Fatalf("storageClassName=%v want premium-nvme (must passthrough, not hardcode)", got.Spec.StorageClassName)
	}
	qty := got.Spec.Resources.Requests[corev1.ResourceStorage]
	if qty.String() != "20Gi" {
		t.Fatalf("storage size=%q want 20Gi", qty.String())
	}
	owners := got.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Name != nom.Name {
		t.Fatalf("ownerRefs=%v", owners)
	}

	// Idempotent: second call must not error and must not attempt to recreate/mutate.
	name2, err := r.reconcilePVC(context.Background(), nom, nom.Spec.Project.Volume, ProjectPVCName(nom), ComponentProject)
	if err != nil || name2 != wantName {
		t.Fatalf("second reconcilePVC: name=%q err=%v", name2, err)
	}
}

func TestReconcilePVC_ExplicitTemplateName(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("pvc-explicit-name")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	vs := nominatimv1alpha1.VolumeSource{
		VolumeClaimTemplate: &nominatimv1alpha1.VolumeClaimTemplate{
			Metadata: metav1.ObjectMeta{Name: "custom-name", Labels: map[string]string{"custom": "label"}},
			Spec: corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("5Gi")},
				},
			},
		},
	}
	name, err := r.reconcilePVC(context.Background(), nom, vs, "unused-default", ComponentFlatnode)
	if err != nil {
		t.Fatalf("reconcilePVC: %v", err)
	}
	if name != "custom-name" {
		t.Fatalf("name=%q want custom-name", name)
	}
	got := &corev1.PersistentVolumeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "custom-name", Namespace: "default"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Labels["custom"] != "label" {
		t.Fatalf("expected custom label preserved, got %v", got.Labels)
	}
}

func TestReconcilePVC_ErrorsWithoutClaimNameOrTemplate(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("pvc-empty")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if _, err := r.reconcilePVC(context.Background(), nom, nominatimv1alpha1.VolumeSource{}, "default-name", ComponentProject); err == nil {
		t.Fatal("expected error for empty volume source")
	}
}

func TestReconcileAPI_RequiresConnectionSecretName(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("api-nosecret")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", ""); err == nil {
		t.Fatal("expected error when connectionSecretName is empty")
	}
}

func TestReconcileAPI_CreatesDeploymentAndServiceWithDBEnv(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("api-basic")
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileAPI(context.Background(), nom, "api-basic-project", ""); err != nil {
		t.Fatalf("reconcileAPI: %v", err)
	}

	deploy := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: APIName(nom), Namespace: "default"}, deploy); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != 1 {
		t.Fatalf("replicas=%v want 1", deploy.Spec.Replicas)
	}
	container := deploy.Spec.Template.Spec.Containers[0]
	if container.Image != DefaultAPIRepository+":"+DefaultImageTag {
		t.Fatalf("image=%q", container.Image)
	}
	foundDSN := false
	for _, e := range container.Env {
		if e.Name == "NOMINATIM_DATABASE_DSN" {
			foundDSN = true
			if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil || e.ValueFrom.SecretKeyRef.Name != testConnectionSecretName {
				t.Fatalf("NOMINATIM_DATABASE_DSN not wired to secret: %+v", e)
			}
		}
	}
	if !foundDSN {
		t.Fatal("expected NOMINATIM_DATABASE_DSN env var from secret")
	}
	foundProjectMount := false
	for _, m := range container.VolumeMounts {
		if m.Name == "project" && m.MountPath == projectMountPath {
			foundProjectMount = true
		}
	}
	if !foundProjectMount {
		t.Fatalf("expected project volume mount at %s, got %+v", projectMountPath, container.VolumeMounts)
	}

	owners := deploy.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Name != nom.Name {
		t.Fatalf("ownerRefs=%v", owners)
	}

	svc := &corev1.Service{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: APIName(nom), Namespace: "default"}, svc); err != nil {
		t.Fatalf("get service: %v", err)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != workloadServicePort || svc.Spec.Ports[0].TargetPort.IntVal != workloadContainerPort {
		t.Fatalf("service ports=%+v", svc.Spec.Ports)
	}
}

func TestReconcileAPI_FlatnodeVolumeAndEnv(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("api-flatnode")
	nom.Spec.Flatnode = &nominatimv1alpha1.FlatnodeSpec{
		Volume: nominatimv1alpha1.VolumeSource{ClaimName: "flatnode-pvc"},
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", "flatnode-pvc"); err != nil {
		t.Fatalf("reconcileAPI: %v", err)
	}

	deploy := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: APIName(nom), Namespace: "default"}, deploy); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	container := deploy.Spec.Template.Spec.Containers[0]

	foundFlatnodeMount := false
	for _, m := range container.VolumeMounts {
		if m.Name == "flatnode" && m.MountPath == flatnodeMountPath {
			foundFlatnodeMount = true
		}
	}
	if !foundFlatnodeMount {
		t.Fatalf("expected flatnode volume mount at %s, got %+v", flatnodeMountPath, container.VolumeMounts)
	}

	foundEnv := false
	for _, e := range container.Env {
		if e.Name == flatnodeFileEnv {
			foundEnv = true
			if e.Value != flatnodeFilePath {
				t.Fatalf("%s=%q want %q", flatnodeFileEnv, e.Value, flatnodeFilePath)
			}
		}
	}
	if !foundEnv {
		t.Fatalf("expected %s env var", flatnodeFileEnv)
	}

	foundClaim := false
	for _, v := range deploy.Spec.Template.Spec.Volumes {
		if v.Name == "flatnode" && v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "flatnode-pvc" {
			foundClaim = true
		}
	}
	if !foundClaim {
		t.Fatalf("expected flatnode PVC volume, got %+v", deploy.Spec.Template.Spec.Volumes)
	}
}

func TestReconcileAPI_RouteCreatesHTTPRoute(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("api-route")
	group := "gateway.networking.k8s.io"
	kind := "Gateway"
	nom.Spec.API = &nominatimv1alpha1.APISpec{
		Route: &nominatimv1alpha1.RouteSpec{
			ParentRefs: []nominatimv1alpha1.ParentReference{
				{Name: "my-gateway", Group: &group, Kind: &kind},
			},
			Hostnames: []string{"nominatim.example.com"},
		},
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", ""); err != nil {
		t.Fatalf("reconcileAPI: %v", err)
	}

	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(HTTPRouteGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: APIName(nom), Namespace: "default"}, route); err != nil {
		t.Fatalf("get HTTPRoute: %v", err)
	}

	hostnames, found, err := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
	if err != nil || !found || len(hostnames) != 1 || hostnames[0] != "nominatim.example.com" {
		t.Fatalf("hostnames=%v found=%v err=%v", hostnames, found, err)
	}

	parentRefs, found, err := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	if err != nil || !found || len(parentRefs) != 1 {
		t.Fatalf("parentRefs=%v found=%v err=%v", parentRefs, found, err)
	}
	pr := parentRefs[0].(map[string]interface{})
	if pr["name"] != "my-gateway" || pr["group"] != group || pr["kind"] != kind {
		t.Fatalf("parentRef=%v", pr)
	}

	rules, found, err := unstructured.NestedSlice(route.Object, "spec", "rules")
	if err != nil || !found || len(rules) != 1 {
		t.Fatalf("rules=%v found=%v err=%v", rules, found, err)
	}
	rule := rules[0].(map[string]interface{})
	backends := rule["backendRefs"].([]interface{})
	backend := backends[0].(map[string]interface{})
	if backend["name"] != APIName(nom) {
		t.Fatalf("backendRef name=%v want %v", backend["name"], APIName(nom))
	}

	owners := route.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Name != nom.Name {
		t.Fatalf("HTTPRoute ownerRefs=%v", owners)
	}
}

func TestReconcileUI_NoopWhenUnset(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("ui-unset")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileUI(context.Background(), nom); err != nil {
		t.Fatalf("reconcileUI: %v", err)
	}
	deploy := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: UIName(nom), Namespace: "default"}, deploy); err == nil {
		t.Fatal("expected no UI deployment when spec.ui is unset")
	}
}

func TestReconcileUI_CreatesDeploymentServiceRoute(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("ui-set")
	group := "gateway.networking.k8s.io"
	nom.Spec.UI = &nominatimv1alpha1.UISpec{
		Route: &nominatimv1alpha1.RouteSpec{
			ParentRefs: []nominatimv1alpha1.ParentReference{{Name: "gw", Group: &group}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileUI(context.Background(), nom); err != nil {
		t.Fatalf("reconcileUI: %v", err)
	}

	deploy := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: UIName(nom), Namespace: "default"}, deploy); err != nil {
		t.Fatalf("get UI deployment: %v", err)
	}
	if deploy.Spec.Template.Spec.Containers[0].Image != DefaultUIRepository+":"+DefaultImageTag {
		t.Fatalf("image=%q", deploy.Spec.Template.Spec.Containers[0].Image)
	}

	svc := &corev1.Service{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: UIName(nom), Namespace: "default"}, svc); err != nil {
		t.Fatalf("get UI service: %v", err)
	}

	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(HTTPRouteGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: UIName(nom), Namespace: "default"}, route); err != nil {
		t.Fatalf("get UI HTTPRoute: %v", err)
	}
}

func TestShouldSuspendAPI_ImpactMatrix(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("suspend")
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "op-update", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
	}
	nom.Status.ActiveOperationRefs = []corev1.ObjectReference{{Name: "op-update"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, op).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	cases := []struct {
		impact nominatimv1alpha1.OperationImpact
		want   bool
	}{
		{nominatimv1alpha1.OperationImpactNever, false},
		{nominatimv1alpha1.OperationImpactBootstrapReimport, false},
		{nominatimv1alpha1.OperationImpactWriteHeavy, false},
		{nominatimv1alpha1.OperationImpactAll, true},
		// Empty impact defaults to Never; Update is not suspended.
		{"", false},
	}
	for _, tc := range cases {
		got, err := r.shouldSuspendAPI(context.Background(), nom, tc.impact)
		if err != nil {
			t.Fatalf("impact=%q err=%v", tc.impact, err)
		}
		if got != tc.want {
			t.Fatalf("impact=%q got=%v want=%v", tc.impact, got, tc.want)
		}
	}
}

func TestShouldSuspendAPI_SkipsMissingOperations(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("suspend-missing")
	nom.Status.ActiveOperationRefs = []corev1.ObjectReference{{Name: "gone"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	got, err := r.shouldSuspendAPI(context.Background(), nom, nominatimv1alpha1.OperationImpactAll)
	if err != nil {
		t.Fatalf("shouldSuspendAPI: %v", err)
	}
	if got {
		t.Fatal("expected false when active operation ref no longer exists")
	}
}

func TestServingWorkloadsAllowed(t *testing.T) {
	cases := []struct {
		name   string
		spec   []string
		status []nominatimv1alpha1.RegionStatus
		want   bool
	}{
		{name: "no desired regions", want: true},
		{name: "desired regions waiting on bootstrap", spec: []string{"europe/monaco"}, want: false},
		{
			name:   "bootstrap synced regions",
			spec:   []string{"europe/monaco"},
			status: []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco", Phase: "Imported"}},
			want:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nom := &nominatimv1alpha1.Nominatim{
				Spec:   nominatimv1alpha1.NominatimSpec{Regions: tc.spec},
				Status: nominatimv1alpha1.NominatimStatus{Regions: tc.status},
			}
			if got := servingWorkloadsAllowed(nom); got != tc.want {
				t.Fatalf("servingWorkloadsAllowed=%v want %v", got, tc.want)
			}
		})
	}
}

func TestReconcileAPI_SkipsUntilBootstrapRegionsSynced(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("api-pre-bootstrap")
	nom.Spec.Regions = []string{"europe/monaco"}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	// Pre-existing API from an older Never-during-bootstrap behavior — must be deleted.
	existing := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: APIName(nom), Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "x"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "x"}}},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, existing).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", ""); err != nil {
		t.Fatalf("reconcileAPI: %v", err)
	}
	deploy := &appsv1.Deployment{}
	err := c.Get(context.Background(), types.NamespacedName{Name: APIName(nom), Namespace: "default"}, deploy)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected API deployment absent until bootstrap, got err=%v deploy=%v", err, deploy.Name)
	}
}

func TestReconcileAPI_CreatesAfterBootstrapRegionsSynced(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("api-post-bootstrap")
	nom.Spec.Regions = []string{"europe/monaco"}
	nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco", Phase: "Imported"}}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", ""); err != nil {
		t.Fatalf("reconcileAPI: %v", err)
	}
	deploy := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: APIName(nom), Namespace: "default"}, deploy); err != nil {
		t.Fatalf("expected API after bootstrap: %v", err)
	}
}

func TestReconcileUI_SkipsUntilBootstrapRegionsSynced(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("ui-pre-bootstrap")
	nom.Spec.Regions = []string{"europe/monaco"}
	nom.Spec.UI = &nominatimv1alpha1.UISpec{}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileUI(context.Background(), nom); err != nil {
		t.Fatalf("reconcileUI: %v", err)
	}
	deploy := &appsv1.Deployment{}
	err := c.Get(context.Background(), types.NamespacedName{Name: UIName(nom), Namespace: "default"}, deploy)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected UI deployment absent until bootstrap, got err=%v", err)
	}
}

func TestReconcileAPI_SuspendDuringOperationsScalesToZero(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("api-suspend")
	nom.Spec.API = &nominatimv1alpha1.APISpec{SuspendDuringOperations: nominatimv1alpha1.OperationImpactAll}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}

	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "busy-op", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
	}
	nom.Status.ActiveOperationRefs = []corev1.ObjectReference{{Name: "busy-op"}}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, op).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", ""); err != nil {
		t.Fatalf("reconcileAPI: %v", err)
	}

	deploy := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: APIName(nom), Namespace: "default"}, deploy); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != 0 {
		t.Fatalf("replicas=%v want 0 (suspended)", deploy.Spec.Replicas)
	}
}

func TestReconcileAPI_DefaultNeverKeepsAPIUp(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("api-default-never")
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "busy-op", Namespace: "default"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
		},
	}
	nom.Status.ActiveOperationRefs = []corev1.ObjectReference{{Name: "busy-op"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, op).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", ""); err != nil {
		t.Fatalf("reconcileAPI: %v", err)
	}
	deploy := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: APIName(nom), Namespace: "default"}, deploy); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != 1 {
		t.Fatalf("replicas=%v want 1 (default Never must not suspend)", deploy.Spec.Replicas)
	}
}

func TestReconcileAPI_ExplicitReplicasHonoredWhenNotSuspended(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("api-explicit-replicas")
	three := int32(3)
	nom.Spec.API = &nominatimv1alpha1.APISpec{Replicas: &three}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", ""); err != nil {
		t.Fatalf("reconcileAPI: %v", err)
	}
	deploy := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: APIName(nom), Namespace: "default"}, deploy); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != 3 {
		t.Fatalf("replicas=%v want 3", deploy.Spec.Replicas)
	}
}

func TestReconcileAPI_CustomImage(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("api-custom-image")
	nom.Spec.API = &nominatimv1alpha1.APISpec{
		Image: &nominatimv1alpha1.ImageSpec{Repository: "example.com/custom-api", Tag: "v1.2.3", PullPolicy: corev1.PullAlways},
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", ""); err != nil {
		t.Fatalf("reconcileAPI: %v", err)
	}
	deploy := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: APIName(nom), Namespace: "default"}, deploy); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	container := deploy.Spec.Template.Spec.Containers[0]
	if container.Image != "example.com/custom-api:v1.2.3" {
		t.Fatalf("image=%q", container.Image)
	}
	if container.ImagePullPolicy != corev1.PullAlways {
		t.Fatalf("pullPolicy=%q", container.ImagePullPolicy)
	}
}

func TestReconcileAPI_PodSpecOverlay(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("api-podspec")
	overlay, _ := json.Marshal(corev1.PodSpec{
		NodeSelector: map[string]string{"disk": "nvme"},
		Containers: []corev1.Container{{
			Name:  "api",
			Image: "evil:ignore-me",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			},
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready"}},
			},
			Env: []corev1.EnvVar{
				{Name: "NOMINATIM_DATABASE_DSN", Value: "user-should-lose"},
				{Name: "EXTRA", Value: "1"},
			},
		}},
	})
	nom.Spec.API = &nominatimv1alpha1.APISpec{
		Image:   &nominatimv1alpha1.ImageSpec{Repository: "example.com/api", Tag: "sealed"},
		PodSpec: &runtime.RawExtension{Raw: overlay},
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", ""); err != nil {
		t.Fatalf("reconcileAPI: %v", err)
	}
	deploy := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: APIName(nom), Namespace: "default"}, deploy); err != nil {
		t.Fatalf("get: %v", err)
	}
	spec := deploy.Spec.Template.Spec
	if spec.NodeSelector["disk"] != "nvme" {
		t.Fatalf("nodeSelector=%v", spec.NodeSelector)
	}
	c0 := spec.Containers[0]
	if c0.Image != "example.com/api:sealed" {
		t.Fatalf("image seal=%q", c0.Image)
	}
	if len(c0.Ports) != 1 || c0.Ports[0].ContainerPort != workloadContainerPort {
		t.Fatalf("ports=%v", c0.Ports)
	}
	if c0.Resources.Requests.Cpu().String() != "100m" {
		t.Fatalf("cpu=%v", c0.Resources.Requests)
	}
	if c0.LivenessProbe == nil || c0.LivenessProbe.HTTPGet == nil || c0.LivenessProbe.HTTPGet.Path != "/ready" {
		t.Fatalf("probe=%v", c0.LivenessProbe)
	}
	env := envMap(c0.Env)
	if env["EXTRA"] != "1" {
		t.Fatalf("extra env missing: %v", env)
	}
	if env["NOMINATIM_DATABASE_DSN"] == "user-should-lose" {
		t.Fatal("reserved env must not take overlay value")
	}
}

func TestReconcileAPI_PodSpecInvalidJSON(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("api-bad-podspec")
	nom.Spec.API = &nominatimv1alpha1.APISpec{
		PodSpec: &runtime.RawExtension{Raw: []byte(`{`)},
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", ""); err == nil {
		t.Fatal("expected error for invalid podSpec JSON")
	}
}

func TestBuildOperationJob_WorkerPodSpecOverlay(t *testing.T) {
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "job-podspec", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "mynom"},
		},
	}
	overlay, _ := json.Marshal(corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyAlways,
		Tolerations:   []corev1.Toleration{{Key: "spot", Operator: corev1.TolerationOpExists}},
		Containers: []corev1.Container{{
			Name: "worker",
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
			},
		}},
	})
	parent := &nominatimv1alpha1.Nominatim{
		ObjectMeta: metav1.ObjectMeta{Name: "mynom", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimSpec{
			Project: nominatimv1alpha1.ProjectSpec{
				Volume: nominatimv1alpha1.VolumeSource{ClaimName: "project-pvc"},
			},
			Worker: &nominatimv1alpha1.WorkerSpec{
				PodSpec: &runtime.RawExtension{Raw: overlay},
			},
		},
		Status: nominatimv1alpha1.NominatimStatus{
			Database: nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: "db"},
		},
	}
	job := mustBuildOperationJob(t, op, parent, "staging-pvc", "worker:v1", corev1.PullIfNotPresent)
	spec := job.Spec.Template.Spec
	if len(spec.Tolerations) != 1 || spec.Tolerations[0].Key != "spot" {
		t.Fatalf("tolerations=%v", spec.Tolerations)
	}
	if spec.Containers[0].Image != "worker:v1" {
		t.Fatalf("image=%q", spec.Containers[0].Image)
	}
	if spec.Containers[0].Resources.Limits.Memory().String() != "2Gi" {
		t.Fatalf("memory=%v", spec.Containers[0].Resources.Limits)
	}
	if len(spec.Containers[0].Ports) != 0 {
		t.Fatalf("worker overlay must not invent ports, got %+v", spec.Containers[0].Ports)
	}
	if spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("restartPolicy=%q", spec.RestartPolicy)
	}
}

func TestReconcileWorkloads_FullFlow(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("workloads-flow")
	nom.Spec.UI = &nominatimv1alpha1.UISpec{}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileWorkloads(context.Background(), nom); err != nil {
		t.Fatalf("reconcileWorkloads: %v", err)
	}

	apiDeploy := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: APIName(nom), Namespace: "default"}, apiDeploy); err != nil {
		t.Fatalf("get API deployment: %v", err)
	}
	uiDeploy := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: UIName(nom), Namespace: "default"}, uiDeploy); err != nil {
		t.Fatalf("get UI deployment: %v", err)
	}

	// baseNominatim uses an existing claimName ("project"), so no PVC object should be created.
	projectPVC := &corev1.PersistentVolumeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "project", Namespace: "default"}, projectPVC); err == nil {
		t.Fatal("expected no PVC to be created for claimName passthrough")
	}
}

func TestReconcileWorkloads_PropagatesProjectVolumeError(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("workloads-badvol")
	nom.Spec.Project.Volume = nominatimv1alpha1.VolumeSource{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileWorkloads(context.Background(), nom); err == nil {
		t.Fatal("expected error for invalid project volume source")
	}
}

func TestReconcileWorkloads_PropagatesFlatnodeVolumeError(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("workloads-badflatnode")
	nom.Spec.Flatnode = &nominatimv1alpha1.FlatnodeSpec{Volume: nominatimv1alpha1.VolumeSource{}}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileWorkloads(context.Background(), nom); err == nil {
		t.Fatal("expected error for invalid flatnode volume source")
	}
}

func TestOperationImpactMatches_AllCombinations(t *testing.T) {
	opTypes := []nominatimv1alpha1.NominatimOperationType{
		nominatimv1alpha1.NominatimOperationBootstrap,
		nominatimv1alpha1.NominatimOperationAddRegions,
		nominatimv1alpha1.NominatimOperationReimport,
		nominatimv1alpha1.NominatimOperationUpdate,
		nominatimv1alpha1.NominatimOperationCatchUp,
	}
	for _, opType := range opTypes {
		if operationImpactMatches(nominatimv1alpha1.OperationImpactNever, opType) {
			t.Fatalf("Never must never match (type=%s)", opType)
		}
		if !operationImpactMatches(nominatimv1alpha1.OperationImpactAll, opType) {
			t.Fatalf("All must always match (type=%s)", opType)
		}
	}
	if !operationImpactMatches(nominatimv1alpha1.OperationImpactBootstrapReimport, nominatimv1alpha1.NominatimOperationBootstrap) {
		t.Fatal("BootstrapReimport should match Bootstrap")
	}
	if !operationImpactMatches(nominatimv1alpha1.OperationImpactBootstrapReimport, nominatimv1alpha1.NominatimOperationReimport) {
		t.Fatal("BootstrapReimport should match Reimport")
	}
	if operationImpactMatches(nominatimv1alpha1.OperationImpactBootstrapReimport, nominatimv1alpha1.NominatimOperationAddRegions) {
		t.Fatal("BootstrapReimport should not match AddRegions")
	}
	if !operationImpactMatches(nominatimv1alpha1.OperationImpactWriteHeavy, nominatimv1alpha1.NominatimOperationAddRegions) {
		t.Fatal("WriteHeavy should match AddRegions")
	}
	if operationImpactMatches(nominatimv1alpha1.OperationImpactWriteHeavy, nominatimv1alpha1.NominatimOperationUpdate) {
		t.Fatal("WriteHeavy should not match Update")
	}
	if operationImpactMatches("bogus", nominatimv1alpha1.NominatimOperationUpdate) {
		t.Fatal("unknown impact should not match")
	}
}

func TestResolveImageAndPullPolicy_Defaults(t *testing.T) {
	if got := resolveImage(nil, DefaultAPIRepository); got != DefaultAPIRepository+":"+DefaultImageTag {
		t.Fatalf("resolveImage(nil)=%q", got)
	}
	spec := &nominatimv1alpha1.ImageSpec{Repository: "custom/repo"}
	if got := resolveImage(spec, DefaultAPIRepository); got != "custom/repo:"+DefaultImageTag {
		t.Fatalf("resolveImage(partial)=%q", got)
	}
	if got := resolvePullPolicy(nil); got != "" {
		t.Fatalf("resolvePullPolicy(nil)=%q", got)
	}
	if got := resolvePullPolicy(&nominatimv1alpha1.ImageSpec{PullPolicy: corev1.PullIfNotPresent}); got != corev1.PullIfNotPresent {
		t.Fatalf("resolvePullPolicy=%q", got)
	}
}

func TestReconcileWorkloads_PropagatesAPIError(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("workloads-api-error")
	// No database status set -> reconcileAPI must fail on empty connectionSecretName,
	// and reconcileWorkloads must propagate that error.
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileWorkloads(context.Background(), nom); err == nil {
		t.Fatal("expected reconcileWorkloads to propagate reconcileAPI error")
	}
}

func TestReconcilePVC_GetError(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("pvc-get-error")
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	fc := &failingClient{Client: base, failGet: map[string]error{
		ProjectPVCName(nom): fmt.Errorf("boom"),
	}}
	r := &NominatimReconciler{Client: fc, Scheme: scheme}

	vs := nominatimv1alpha1.VolumeSource{
		VolumeClaimTemplate: &nominatimv1alpha1.VolumeClaimTemplate{
			Spec: corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		},
	}
	if _, err := r.reconcilePVC(context.Background(), nom, vs, ProjectPVCName(nom), ComponentProject); err == nil {
		t.Fatal("expected get error to propagate")
	}
}

func TestReconcilePVC_CreateError(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("pvc-create-error")
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	fc := &failingClient{Client: base, failCreate: []failSpec{{kind: "PersistentVolumeClaim"}}}
	r := &NominatimReconciler{Client: fc, Scheme: scheme}

	vs := nominatimv1alpha1.VolumeSource{
		VolumeClaimTemplate: &nominatimv1alpha1.VolumeClaimTemplate{
			Spec: corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		},
	}
	if _, err := r.reconcilePVC(context.Background(), nom, vs, ProjectPVCName(nom), ComponentProject); err == nil {
		t.Fatal("expected create error to propagate")
	}
}

func TestReconcilePVC_SetControllerReferenceError(t *testing.T) {
	nom := baseNominatim("pvc-owner-error")
	r := emptySchemeReconciler()

	vs := nominatimv1alpha1.VolumeSource{
		VolumeClaimTemplate: &nominatimv1alpha1.VolumeClaimTemplate{
			Spec: corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		},
	}
	if _, err := r.reconcilePVC(context.Background(), nom, vs, ProjectPVCName(nom), ComponentProject); err == nil {
		t.Fatal("expected SetControllerReference error to propagate")
	}
}

func TestReconcileAPI_SetControllerReferenceError(t *testing.T) {
	nom := nominatimWithConnectionSecret("api-owner-error")
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	r := emptySchemeReconciler()
	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", ""); err == nil {
		t.Fatal("expected SetControllerReference error to propagate from API deployment")
	}
}

func TestReconcileUI_SetControllerReferenceError(t *testing.T) {
	nom := baseNominatim("ui-owner-error")
	nom.Spec.UI = &nominatimv1alpha1.UISpec{}
	r := emptySchemeReconciler()
	if err := r.reconcileUI(context.Background(), nom); err == nil {
		t.Fatal("expected SetControllerReference error to propagate from UI deployment")
	}
}

func TestReconcileService_SetControllerReferenceError(t *testing.T) {
	nom := baseNominatim("svc-owner-error")
	r := emptySchemeReconciler()
	if err := r.reconcileService(context.Background(), nom, "svc-name", map[string]string{"a": "b"}, ComponentAPI); err == nil {
		t.Fatal("expected SetControllerReference error to propagate from Service")
	}
}

func TestReconcileHTTPRoute_SetControllerReferenceError(t *testing.T) {
	nom := baseNominatim("route-owner-error")
	r := emptySchemeReconciler()
	route := &nominatimv1alpha1.RouteSpec{ParentRefs: []nominatimv1alpha1.ParentReference{{Name: "gw"}}}
	if err := r.reconcileHTTPRoute(context.Background(), nom, "route-name", "svc-name", route, ComponentAPI); err == nil {
		t.Fatal("expected SetControllerReference error to propagate from HTTPRoute")
	}
}

// TestReconcileHTTPRoute_SetNestedSliceError pre-seeds a malformed HTTPRoute (spec is a
// scalar, not a map) so unstructured.SetNestedSlice fails while writing spec.parentRefs,
// mirroring the existing CNPG "not-a-map" test pattern in nominatim_database_test.go.
func TestReconcileHTTPRoute_SetNestedSliceError(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("route-badspec")
	existing := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": HTTPRouteGVK.GroupVersion().String(),
		"kind":       HTTPRouteGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      APIName(nom),
			"namespace": "default",
		},
		"spec": "not-a-map",
	}}
	existing.SetGroupVersionKind(HTTPRouteGVK)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, existing).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	route := &nominatimv1alpha1.RouteSpec{ParentRefs: []nominatimv1alpha1.ParentReference{{Name: "gw"}}}
	if err := r.reconcileHTTPRoute(context.Background(), nom, APIName(nom), APIName(nom), route, ComponentAPI); err == nil {
		t.Fatal("expected SetNestedSlice failure on malformed existing spec")
	}
}

func TestReconcileHTTPRoute_ParentRefNamespaceAndSectionName(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("route-fullparent")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	ns := "gw-ns"
	section := "https"
	route := &nominatimv1alpha1.RouteSpec{
		ParentRefs: []nominatimv1alpha1.ParentReference{
			{Name: "gw", Namespace: &ns, SectionName: &section},
		},
	}
	if err := r.reconcileHTTPRoute(context.Background(), nom, "full-route", "svc", route, ComponentAPI); err != nil {
		t.Fatalf("reconcileHTTPRoute: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(HTTPRouteGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: "full-route", Namespace: "default"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	parentRefs, found, err := unstructured.NestedSlice(got.Object, "spec", "parentRefs")
	if err != nil || !found || len(parentRefs) != 1 {
		t.Fatalf("parentRefs=%v found=%v err=%v", parentRefs, found, err)
	}
	pr := parentRefs[0].(map[string]interface{})
	if pr["namespace"] != ns || pr["sectionName"] != section {
		t.Fatalf("parentRef=%v", pr)
	}
}

func TestReconcileAPI_ServiceErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("api-svc-error")
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	fc := &failingClient{Client: base, failCreate: []failSpec{{kind: "Service", name: APIName(nom)}}}
	r := &NominatimReconciler{Client: fc, Scheme: scheme}

	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", ""); err == nil {
		t.Fatal("expected reconcileAPI to propagate Service creation error")
	}
	// Deployment must still have been created before the Service failure.
	deploy := &appsv1.Deployment{}
	if err := base.Get(context.Background(), types.NamespacedName{Name: APIName(nom), Namespace: "default"}, deploy); err != nil {
		t.Fatalf("expected API deployment to exist despite Service failure: %v", err)
	}
}

func TestReconcileAPI_HTTPRouteErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("api-route-error")
	nom.Spec.API = &nominatimv1alpha1.APISpec{
		Route: &nominatimv1alpha1.RouteSpec{ParentRefs: []nominatimv1alpha1.ParentReference{{Name: "gw"}}},
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	fc := &failingClient{Client: base, failCreate: []failSpec{{kind: "HTTPRoute", name: APIName(nom)}}}
	r := &NominatimReconciler{Client: fc, Scheme: scheme}

	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", ""); err == nil {
		t.Fatal("expected reconcileAPI to propagate HTTPRoute creation error")
	}
}

func TestReconcileUI_ServiceErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("ui-svc-error")
	two := int32(2)
	nom.Spec.UI = &nominatimv1alpha1.UISpec{Replicas: &two}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	fc := &failingClient{Client: base, failCreate: []failSpec{{kind: "Service", name: UIName(nom)}}}
	r := &NominatimReconciler{Client: fc, Scheme: scheme}

	if err := r.reconcileUI(context.Background(), nom); err == nil {
		t.Fatal("expected reconcileUI to propagate Service creation error")
	}
	deploy := &appsv1.Deployment{}
	if err := base.Get(context.Background(), types.NamespacedName{Name: UIName(nom), Namespace: "default"}, deploy); err != nil {
		t.Fatalf("expected UI deployment to exist despite Service failure: %v", err)
	}
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != 2 {
		t.Fatalf("replicas=%v want 2", deploy.Spec.Replicas)
	}
}

func TestReconcileUI_HTTPRouteErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("ui-route-error")
	nom.Spec.UI = &nominatimv1alpha1.UISpec{
		Route: &nominatimv1alpha1.RouteSpec{ParentRefs: []nominatimv1alpha1.ParentReference{{Name: "gw"}}},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	fc := &failingClient{Client: base, failCreate: []failSpec{{kind: "HTTPRoute", name: UIName(nom)}}}
	r := &NominatimReconciler{Client: fc, Scheme: scheme}

	if err := r.reconcileUI(context.Background(), nom); err == nil {
		t.Fatal("expected reconcileUI to propagate HTTPRoute creation error")
	}
}

func TestShouldSuspendAPI_GetErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("suspend-get-error")
	nom.Status.ActiveOperationRefs = []corev1.ObjectReference{{Name: "op-x"}}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	fc := &failingClient{Client: base, failGet: map[string]error{"op-x": fmt.Errorf("boom")}}
	r := &NominatimReconciler{Client: fc, Scheme: scheme}

	if _, err := r.shouldSuspendAPI(context.Background(), nom, nominatimv1alpha1.OperationImpactAll); err == nil {
		t.Fatal("expected shouldSuspendAPI to propagate non-NotFound Get error")
	}
}

func TestReconcileAPI_SuspendEvaluationErrorPropagates(t *testing.T) {
	scheme := testScheme(t)
	nom := nominatimWithConnectionSecret("api-suspend-eval-error")
	nom.Spec.API = &nominatimv1alpha1.APISpec{SuspendDuringOperations: nominatimv1alpha1.OperationImpactAll}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}
	nom.Status.ActiveOperationRefs = []corev1.ObjectReference{{Name: "op-x"}}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	fc := &failingClient{Client: base, failGet: map[string]error{"op-x": fmt.Errorf("boom")}}
	r := &NominatimReconciler{Client: fc, Scheme: scheme}

	if err := r.reconcileAPI(context.Background(), nom, "project-pvc", ""); err == nil {
		t.Fatal("expected reconcileAPI to propagate suspend evaluation error")
	}
}

func TestReconcile_PropagatesWorkloadsError(t *testing.T) {
	scheme := testScheme(t)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"}}
	nom := baseNominatim("reconcile-workloads-error")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{Name: "s"},
	}
	// Force reconcileWorkloads to fail: flatnode set with an empty (invalid) volume source.
	nom.Spec.Flatnode = &nominatimv1alpha1.FlatnodeSpec{Volume: nominatimv1alpha1.VolumeSource{}}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom, secret).Build()
	r := &NominatimReconciler{Client: base, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}}

	// First reconcile only adds the finalizer.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	_, err := r.Reconcile(context.Background(), req)
	if err == nil {
		t.Fatal("expected Reconcile to propagate reconcileWorkloads error")
	}
}
