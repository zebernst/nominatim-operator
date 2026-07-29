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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

type recordingCNPGEffects struct {
	pauseCalls   int
	resumeCalls  int
	profileCalls int
	lastPaused   *bool
	lastParams   map[string]string
	lastCluster  string
}

func (r *recordingCNPGEffects) PauseBackups(_ context.Context, cluster *unstructured.Unstructured) error {
	r.pauseCalls++
	t := true
	r.lastPaused = &t
	r.lastCluster = cluster.GetName()
	return nil
}

func (r *recordingCNPGEffects) ResumeBackups(_ context.Context, cluster *unstructured.Unstructured) error {
	r.resumeCalls++
	f := false
	r.lastPaused = &f
	r.lastCluster = cluster.GetName()
	return nil
}

func (r *recordingCNPGEffects) ApplyParameters(_ context.Context, cluster *unstructured.Unstructured, params map[string]string, _ []string) error {
	r.profileCalls++
	r.lastParams = params
	r.lastCluster = cluster.GetName()
	return nil
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := nominatimv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return s
}

func baseNominatim(name string) *nominatimv1alpha1.Nominatim {
	return &nominatimv1alpha1.Nominatim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID("test-uid-" + name),
		},
		Spec: nominatimv1alpha1.NominatimSpec{
			Project: nominatimv1alpha1.ProjectSpec{
				Volume: nominatimv1alpha1.VolumeSource{ClaimName: "project"},
			},
		},
	}
}

func TestReconcileDatabase_ConnectionSecretRef_Degraded(t *testing.T) {
	scheme := testScheme(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ext-pg", Namespace: "default"},
		Data:       map[string][]byte{"uri": []byte("postgres://x")},
	}
	nom := baseNominatim("deg")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{Name: "ext-pg"},
		PostgresProfiles: &nominatimv1alpha1.PostgresProfiles{
			Import:  map[string]string{"shared_buffers": "2GB"},
			Runtime: map[string]string{"shared_buffers": "256MB"},
		},
		PauseBackupsDuringOperations: nominatimv1alpha1.OperationImpactWriteHeavy,
	}

	effects := &recordingCNPGEffects{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, secret).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme, CNPGEffects: effects}

	if err := r.reconcileDatabase(context.Background(), nom); err != nil {
		t.Fatalf("reconcileDatabase: %v", err)
	}

	if !nom.Status.Database.Degraded {
		t.Fatal("expected degraded=true")
	}
	if nom.Status.Database.Mode != nominatimv1alpha1.DatabaseModeConnectionSecret {
		t.Fatalf("mode=%q", nom.Status.Database.Mode)
	}
	if nom.Status.Database.ConnectionSecretName != "ext-pg" {
		t.Fatalf("secret=%q", nom.Status.Database.ConnectionSecretName)
	}
	if nom.Status.Database.ClusterName != "" {
		t.Fatalf("clusterName should be empty, got %q", nom.Status.Database.ClusterName)
	}

	// Degraded path must never attempt Barman/backup pause or profile apply.
	if err := r.SetBackupPaused(context.Background(), nom, true); err != nil {
		t.Fatalf("SetBackupPaused: %v", err)
	}
	if err := r.ApplyPostgresProfile(context.Background(), nom, "import"); err != nil {
		t.Fatalf("ApplyPostgresProfile: %v", err)
	}
	if effects.pauseCalls != 0 || effects.resumeCalls != 0 || effects.profileCalls != 0 {
		t.Fatalf("degraded mode must not call CNPG effects; got pause=%d resume=%d profile=%d",
			effects.pauseCalls, effects.resumeCalls, effects.profileCalls)
	}
}

func TestReconcileDatabase_ConnectionSecretRef_MissingSecret(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("missing-sec")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{Name: "nope"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme, CNPGEffects: &recordingCNPGEffects{}}

	if err := r.reconcileDatabase(context.Background(), nom); err == nil {
		t.Fatal("expected error for missing secret")
	}
}

func TestReconcileDatabase_ClusterRef_Attach(t *testing.T) {
	scheme := testScheme(t)
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("existing-pg")
	cluster.SetNamespace("default")
	_ = unstructured.SetNestedField(cluster.Object, int64(1), "spec", "instances")

	nom := baseNominatim("attach")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef: &nominatimv1alpha1.DatabaseClusterRef{Name: "existing-pg"},
		PostgresProfiles: &nominatimv1alpha1.PostgresProfiles{
			Runtime: map[string]string{"max_connections": "200"},
		},
	}

	effects := &recordingCNPGEffects{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, cluster).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme, CNPGEffects: effects}

	if err := r.reconcileDatabase(context.Background(), nom); err != nil {
		t.Fatalf("reconcileDatabase: %v", err)
	}

	if nom.Status.Database.Degraded {
		t.Fatal("expected degraded=false")
	}
	if nom.Status.Database.Mode != nominatimv1alpha1.DatabaseModeClusterAttached {
		t.Fatalf("mode=%q", nom.Status.Database.Mode)
	}
	if nom.Status.Database.ClusterName != "existing-pg" {
		t.Fatalf("clusterName=%q", nom.Status.Database.ClusterName)
	}
	wantSecret := CNPGAppSecretName("existing-pg")
	if nom.Status.Database.ConnectionSecretName != wantSecret {
		t.Fatalf("secret=%q want %q", nom.Status.Database.ConnectionSecretName, wantSecret)
	}

	if err := r.SetBackupPaused(context.Background(), nom, true); err != nil {
		t.Fatalf("SetBackupPaused: %v", err)
	}
	if effects.pauseCalls != 1 || effects.lastCluster != "existing-pg" {
		t.Fatalf("expected PauseBackups on attached cluster, got pause=%d cluster=%q",
			effects.pauseCalls, effects.lastCluster)
	}

	if err := r.ApplyPostgresProfile(context.Background(), nom, "runtime"); err != nil {
		t.Fatalf("ApplyPostgresProfile: %v", err)
	}
	if effects.profileCalls != 1 || effects.lastParams["max_connections"] != "200" {
		t.Fatalf("expected runtime profile apply, got calls=%d params=%v", effects.profileCalls, effects.lastParams)
	}
}

func TestReconcileDatabase_ClusterRef_CustomConnectionSecret(t *testing.T) {
	scheme := testScheme(t)
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("existing-pg")
	cluster.SetNamespace("default")
	_ = unstructured.SetNestedField(cluster.Object, int64(1), "spec", "instances")

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-pg-creds", Namespace: "default"},
		Data:       map[string][]byte{"uri": []byte("postgresql://u:p@h/db")},
	}

	nom := baseNominatim("attach-custom-secret")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef: &nominatimv1alpha1.DatabaseClusterRef{
			Name: "existing-pg",
			ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{
				Name: "custom-pg-creds",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, cluster, secret).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileDatabase(context.Background(), nom); err != nil {
		t.Fatalf("reconcileDatabase: %v", err)
	}
	if nom.Status.Database.Mode != nominatimv1alpha1.DatabaseModeClusterAttached {
		t.Fatalf("mode=%q", nom.Status.Database.Mode)
	}
	if nom.Status.Database.ConnectionSecretName != "custom-pg-creds" {
		t.Fatalf("secret=%q want custom-pg-creds", nom.Status.Database.ConnectionSecretName)
	}
	if nom.Status.Database.Degraded {
		t.Fatal("custom secret on clusterRef must stay ClusterAttached (not degraded)")
	}
}

func TestReconcileDatabase_ClusterRef_CustomConnectionSecretMissing(t *testing.T) {
	scheme := testScheme(t)
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("existing-pg")
	cluster.SetNamespace("default")
	_ = unstructured.SetNestedField(cluster.Object, int64(1), "spec", "instances")

	nom := baseNominatim("attach-missing-secret")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef: &nominatimv1alpha1.DatabaseClusterRef{
			Name: "existing-pg",
			ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{
				Name: "missing-creds",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, cluster).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileDatabase(context.Background(), nom); err == nil {
		t.Fatal("expected error for missing clusterRef.connectionSecretRef")
	}
}

func TestReconcileDatabase_ClusterRef_Missing(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("missing-cl")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef: &nominatimv1alpha1.DatabaseClusterRef{Name: "gone"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileDatabase(context.Background(), nom); err == nil {
		t.Fatal("expected error for missing cluster")
	}
}

func ownedClusterNominatim(name, storageClass string, instances int32) *nominatimv1alpha1.Nominatim {
	nom := baseNominatim(name)
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		Cluster: &nominatimv1alpha1.DatabaseClusterCreate{
			Instances: &instances,
			Storage: &nominatimv1alpha1.VolumeClaimTemplate{
				Spec: corev1.PersistentVolumeClaimSpec{
					StorageClassName: &storageClass,
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("50Gi"),
						},
					},
				},
			},
		},
	}
	return nom
}

func assertOwnedClusterStatus(t *testing.T, nom *nominatimv1alpha1.Nominatim, wantName string) {
	t.Helper()
	if nom.Status.Database.Mode != nominatimv1alpha1.DatabaseModeClusterManaged {
		t.Fatalf("mode=%q", nom.Status.Database.Mode)
	}
	if nom.Status.Database.ClusterName != wantName {
		t.Fatalf("clusterName=%q want %q", nom.Status.Database.ClusterName, wantName)
	}
	if nom.Status.Database.ConnectionSecretName != CNPGAppSecretName(wantName) {
		t.Fatalf("secret=%q", nom.Status.Database.ConnectionSecretName)
	}
	if nom.Status.Database.Degraded {
		t.Fatal("expected degraded=false")
	}
}

func assertOwnedClusterStorageAndOwner(t *testing.T, got *unstructured.Unstructured, nom *nominatimv1alpha1.Nominatim) {
	t.Helper()
	inst, found, err := unstructured.NestedInt64(got.Object, "spec", "instances")
	if err != nil || !found || inst != 2 {
		t.Fatalf("instances=%v found=%v err=%v", inst, found, err)
	}
	size, found, err := unstructured.NestedString(got.Object, "spec", "storage", "size")
	if err != nil || !found || size != "50Gi" {
		t.Fatalf("storage.size=%q found=%v err=%v", size, found, err)
	}
	class, found, err := unstructured.NestedString(got.Object, "spec", "storage", "storageClass")
	if err != nil || !found || class != "fast-ssd" {
		t.Fatalf("storageClass=%q found=%v (must passthrough, not hardcode)", class, found)
	}

	owners := got.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Name != nom.Name {
		t.Fatalf("ownerRefs=%v", owners)
	}
}

func assertOwnedClusterBootstrapAndRoles(t *testing.T, got *unstructured.Unstructured) {
	t.Helper()
	dbName, found, err := unstructured.NestedString(got.Object, "spec", "bootstrap", "initdb", "database")
	if err != nil || !found || dbName != cnpgAppDatabaseName {
		t.Fatalf("bootstrap.initdb.database=%q found=%v err=%v want %q", dbName, found, err, cnpgAppDatabaseName)
	}
	owner, found, err := unstructured.NestedString(got.Object, "spec", "bootstrap", "initdb", "owner")
	if err != nil || !found || owner != cnpgAppOwnerName {
		t.Fatalf("bootstrap.initdb.owner=%q found=%v err=%v want %q", owner, found, err, cnpgAppOwnerName)
	}
	img, found, err := unstructured.NestedString(got.Object, "spec", "imageName")
	if err != nil || !found || img != cnpgDefaultPostGISImage {
		t.Fatalf("imageName=%q found=%v err=%v want %q", img, found, err, cnpgDefaultPostGISImage)
	}
	postInit, found, err := unstructured.NestedStringSlice(got.Object, "spec", "bootstrap", "initdb", "postInitTemplateSQL")
	if err != nil {
		t.Fatalf("postInitTemplateSQL lookup: %v", err)
	}
	if found && len(postInit) > 0 {
		t.Fatalf("postInitTemplateSQL=%v want unset (extensions via Database CR)", postInit)
	}

	roles, found, err := unstructured.NestedSlice(got.Object, "spec", "managed", "roles")
	if err != nil || !found || len(roles) != 1 {
		t.Fatalf("managed.roles=%v found=%v err=%v", roles, found, err)
	}
	role, ok := roles[0].(map[string]interface{})
	if !ok {
		t.Fatalf("managed.roles[0] type %T", roles[0])
	}
	if role["name"] != cnpgNominatimWebRole || role["ensure"] != "present" || role["login"] != false {
		t.Fatalf("managed.roles[0]=%v want name=%s ensure=present login=false", role, cnpgNominatimWebRole)
	}
}

func assertOwnedClusterCreate(t *testing.T, c client.Client, nom *nominatimv1alpha1.Nominatim, wantName string) {
	t.Helper()
	assertOwnedClusterStatus(t, nom, wantName)

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: wantName, Namespace: "default"}, got); err != nil {
		t.Fatalf("get created cluster: %v", err)
	}
	assertOwnedClusterStorageAndOwner(t, got, nom)
	assertOwnedClusterBootstrapAndRoles(t, got)
}

func assertOwnedDatabaseCR(t *testing.T, c client.Client, nom *nominatimv1alpha1.Nominatim, wantName string) {
	t.Helper()
	dbCR := &unstructured.Unstructured{}
	dbCR.SetGroupVersionKind(CNPGDatabaseGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: OwnedCNPGDatabaseName(nom), Namespace: "default"}, dbCR); err != nil {
		t.Fatalf("get owned Database CR: %v", err)
	}
	exts, found, err := unstructured.NestedSlice(dbCR.Object, "spec", "extensions")
	if err != nil || !found || len(exts) != len(cnpgNominatimExtensions) {
		t.Fatalf("Database.spec.extensions=%v found=%v err=%v", exts, found, err)
	}
	for i, want := range cnpgNominatimExtensions {
		ext, ok := exts[i].(map[string]interface{})
		if !ok || ext["name"] != want || ext["ensure"] != "present" {
			t.Fatalf("extensions[%d]=%v want name=%s ensure=present", i, exts[i], want)
		}
	}
	gotDBName, _, _ := unstructured.NestedString(dbCR.Object, "spec", "name")
	gotOwner, _, _ := unstructured.NestedString(dbCR.Object, "spec", "owner")
	clusterRef, _, _ := unstructured.NestedString(dbCR.Object, "spec", "cluster", "name")
	if gotDBName != cnpgAppDatabaseName || gotOwner != cnpgAppOwnerName || clusterRef != wantName {
		t.Fatalf("Database spec name=%q owner=%q cluster=%q", gotDBName, gotOwner, clusterRef)
	}
	reclaim, _, _ := unstructured.NestedString(dbCR.Object, "spec", "databaseReclaimPolicy")
	if reclaim != cnpgDatabaseReclaimDelete {
		t.Fatalf("databaseReclaimPolicy=%q want %q", reclaim, cnpgDatabaseReclaimDelete)
	}
}

func TestReconcileDatabase_Cluster_CreateOwned(t *testing.T) {
	scheme := testScheme(t)
	nom := ownedClusterNominatim("owned", "fast-ssd", 2)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileDatabase(context.Background(), nom); err != nil {
		t.Fatalf("reconcileDatabase: %v", err)
	}

	wantName := OwnedCNPGClusterName(nom)
	assertOwnedClusterCreate(t, c, nom, wantName)
	assertOwnedDatabaseCR(t, c, nom, wantName)

	if err := r.reconcileDatabase(context.Background(), nom); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
}

func TestReconcileDatabase_Cluster_PreservesExpandedManagedRoles(t *testing.T) {
	scheme := testScheme(t)
	nom := ownedClusterNominatim("owned-roles", "fast-ssd", 2)
	effects := &recordingCNPGEffects{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme, CNPGEffects: effects}

	if err := r.reconcileDatabase(context.Background(), nom); err != nil {
		t.Fatalf("reconcileDatabase: %v", err)
	}
	wantName := OwnedCNPGClusterName(nom)

	// Simulate CNPG expanding managed.roles defaults; reconciler must not replace them.
	expanded := &unstructured.Unstructured{}
	expanded.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: wantName, Namespace: "default"}, expanded); err != nil {
		t.Fatalf("get cluster for expand: %v", err)
	}
	if err := unstructured.SetNestedSlice(expanded.Object, []interface{}{
		map[string]interface{}{
			"name":            cnpgNominatimWebRole,
			"ensure":          "present",
			"login":           false,
			"inherit":         true,
			"connectionLimit": int64(-1),
			"comment":         "Nominatim DATABASE_WEBUSER (read-only grants target)",
		},
	}, "spec", "managed", "roles"); err != nil {
		t.Fatalf("expand roles: %v", err)
	}
	if err := c.Update(context.Background(), expanded); err != nil {
		t.Fatalf("update expanded roles: %v", err)
	}
	if err := r.reconcileDatabase(context.Background(), nom); err != nil {
		t.Fatalf("reconcile after CNPG role expand: %v", err)
	}
	gotAfter := &unstructured.Unstructured{}
	gotAfter.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: wantName, Namespace: "default"}, gotAfter); err != nil {
		t.Fatalf("get after expand reconcile: %v", err)
	}
	rolesAfter, _, _ := unstructured.NestedSlice(gotAfter.Object, "spec", "managed", "roles")
	roleAfter, _ := rolesAfter[0].(map[string]interface{})
	if roleAfter["connectionLimit"] != int64(-1) || roleAfter["inherit"] != true {
		t.Fatalf("managed.roles churned away CNPG defaults: %v", roleAfter)
	}

	if err := r.SetBackupPaused(context.Background(), nom, false); err != nil {
		t.Fatalf("SetBackupPaused resume: %v", err)
	}
	if effects.resumeCalls != 1 {
		t.Fatalf("expected ResumeBackups, got %d", effects.resumeCalls)
	}
}

func TestReconcileDatabase_Cluster_InstanceTune(t *testing.T) {
	scheme := testScheme(t)
	nom := ownedClusterNominatim("tune", "fast-ssd", 1)
	affinityJSON := []byte(`{"nodeSelector":{"node-role.kubernetes.io/postgres":""},"enablePodAntiAffinity":true,"topologyKey":"kubernetes.io/hostname"}`)
	nom.Spec.Database.Cluster.Resources = &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
	}
	nom.Spec.Database.Cluster.Affinity = &runtime.RawExtension{Raw: affinityJSON}
	nom.Spec.Database.Cluster.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{
		MaxSkew:           1,
		TopologyKey:       "topology.kubernetes.io/zone",
		WhenUnsatisfiable: corev1.DoNotSchedule,
		LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "pg"}},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileDatabase(context.Background(), nom); err != nil {
		t.Fatalf("reconcileDatabase: %v", err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: OwnedCNPGClusterName(nom), Namespace: "default"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	mem, found, err := unstructured.NestedString(got.Object, "spec", "resources", "requests", "memory")
	if err != nil || !found || mem != "1Gi" {
		t.Fatalf("resources.memory=%q found=%v err=%v", mem, found, err)
	}
	ns, found, err := unstructured.NestedStringMap(got.Object, "spec", "affinity", "nodeSelector")
	if err != nil || !found || ns["node-role.kubernetes.io/postgres"] != "" {
		t.Fatalf("affinity.nodeSelector=%v found=%v err=%v", ns, found, err)
	}
	tsc, found, err := unstructured.NestedSlice(got.Object, "spec", "topologySpreadConstraints")
	if err != nil || !found || len(tsc) != 1 {
		t.Fatalf("topologySpreadConstraints=%v found=%v err=%v", tsc, found, err)
	}
	img, _, _ := unstructured.NestedString(got.Object, "spec", "imageName")
	if img != cnpgDefaultPostGISImage {
		t.Fatalf("imageName seal broken: %q", img)
	}
}

func TestApplyOwnedCNPGClusterTune_InvalidAffinity(t *testing.T) {
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{}}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	err := applyOwnedCNPGClusterTune(cluster, &nominatimv1alpha1.DatabaseClusterCreate{
		Affinity: &runtime.RawExtension{Raw: []byte(`{`)},
	})
	if err == nil {
		t.Fatal("expected affinity decode error")
	}
}

func TestReconcileDatabase_Cluster_NoHardcodedStorageClass(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("nostorageclass")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		Cluster: &nominatimv1alpha1.DatabaseClusterCreate{
			Storage: &nominatimv1alpha1.VolumeClaimTemplate{
				Spec: corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("10Gi"),
						},
					},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileDatabase(context.Background(), nom); err != nil {
		t.Fatalf("reconcileDatabase: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(context.Background(), client.ObjectKey{Name: OwnedCNPGClusterName(nom), Namespace: "default"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	_, found, _ := unstructured.NestedString(got.Object, "spec", "storage", "storageClass")
	if found {
		t.Fatal("storageClass must be omitted when not specified in VolumeClaimTemplate")
	}
}

func TestMapCNPGClusterToNominatim_OwnerAndClusterRef(t *testing.T) {
	scheme := testScheme(t)
	nomOwned := baseNominatim("owner-nom")
	nomRef := baseNominatim("ref-nom")
	nomRef.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef: &nominatimv1alpha1.DatabaseClusterRef{Name: "shared-pg"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nomOwned, nomRef).Build()

	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("shared-pg")
	cluster.SetNamespace("default")
	cluster.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: nominatimv1alpha1.GroupVersion.String(),
		Kind:       "Nominatim",
		Name:       "owner-nom",
		UID:        nomOwned.UID,
	}})

	reqs := mapCNPGClusterToNominatim(c)(context.Background(), cluster)
	names := map[string]bool{}
	for _, r := range reqs {
		names[r.Name] = true
	}
	if !names["owner-nom"] || !names["ref-nom"] {
		t.Fatalf("expected owner-nom and ref-nom, got %v", reqs)
	}
}

func TestApplyPostgresProfile_UnknownWhich(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("prof")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef: &nominatimv1alpha1.DatabaseClusterRef{Name: "pg"},
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode:        nominatimv1alpha1.DatabaseModeClusterAttached,
		ClusterName: "pg",
	}
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("pg")
	cluster.SetNamespace("default")

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, cluster).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme, CNPGEffects: &recordingCNPGEffects{}}

	if err := r.ApplyPostgresProfile(context.Background(), nom, "nope"); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestReconcileDatabase_NoMode(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("nomode")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileDatabase(context.Background(), nom); err == nil {
		t.Fatal("expected error when no database mode set")
	}
}

func TestCNPGStorage_EmptyTemplate(t *testing.T) {
	empty := ""
	_, err := cnpgStorageFromVolumeClaimTemplate(&nominatimv1alpha1.VolumeClaimTemplate{
		Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &empty},
	})
	if err == nil {
		t.Fatal("expected error for empty storage template")
	}
}

func TestCNPGStorage_ClassOnly(t *testing.T) {
	sc := "ceph"
	out, err := cnpgStorageFromVolumeClaimTemplate(&nominatimv1alpha1.VolumeClaimTemplate{
		Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &sc},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["storageClass"] != "ceph" {
		t.Fatalf("got %#v", out)
	}
}

func TestReconcileDatabase_Cluster_DefaultsNoStorage(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("nostore")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		Cluster: &nominatimv1alpha1.DatabaseClusterCreate{},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileDatabase(context.Background(), nom); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: OwnedCNPGClusterName(nom), Namespace: "default"}, got); err != nil {
		t.Fatal(err)
	}
	inst, found, _ := unstructured.NestedInt64(got.Object, "spec", "instances")
	if !found || inst != 1 {
		t.Fatalf("default instances=%v found=%v", inst, found)
	}
}

func TestReconcileDatabase_Cluster_EmptyStorageErrors(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("badstore")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		Cluster: &nominatimv1alpha1.DatabaseClusterCreate{
			Storage: &nominatimv1alpha1.VolumeClaimTemplate{},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileDatabase(context.Background(), nom); err == nil {
		t.Fatal("expected storage error")
	}
}

func TestSetBackupPaused_ErrorsAndDefaults(t *testing.T) {
	scheme := testScheme(t)
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("pg")
	cluster.SetNamespace("default")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme} // nil CNPGEffects → default stubs

	nom := baseNominatim("bp")
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode: nominatimv1alpha1.DatabaseModeClusterAttached,
	}
	if err := r.SetBackupPaused(context.Background(), nom, true); err == nil {
		t.Fatal("expected missing cluster name error")
	}

	nom.Status.Database.ClusterName = "missing"
	if err := r.SetBackupPaused(context.Background(), nom, true); err == nil {
		t.Fatal("expected get cluster error")
	}

	nom.Status.Database.ClusterName = "pg"
	if err := r.SetBackupPaused(context.Background(), nom, true); err != nil {
		t.Fatalf("default PauseBackups: %v", err)
	}
	if err := r.SetBackupPaused(context.Background(), nom, false); err != nil {
		t.Fatalf("default ResumeBackups: %v", err)
	}

	// Mode ConnectionSecret without Degraded still no-ops.
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{Mode: nominatimv1alpha1.DatabaseModeConnectionSecret}
	effects := &recordingCNPGEffects{}
	r.CNPGEffects = effects
	if err := r.SetBackupPaused(context.Background(), nom, true); err != nil {
		t.Fatal(err)
	}
	if effects.pauseCalls != 0 {
		t.Fatal("ConnectionSecret mode must not pause")
	}
}

func TestApplyPostgresProfile_Branches(t *testing.T) {
	scheme := testScheme(t)
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("pg")
	cluster.SetNamespace("default")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	effects := &recordingCNPGEffects{}
	r := &NominatimReconciler{Client: c, Scheme: scheme, CNPGEffects: effects}

	nom := baseNominatim("ap")
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{Mode: nominatimv1alpha1.DatabaseModeConnectionSecret}
	if err := r.ApplyPostgresProfile(context.Background(), nom, "import"); err != nil {
		t.Fatal(err)
	}
	if effects.profileCalls != 0 {
		t.Fatal("degraded/secret mode must skip profiles")
	}

	nom.Spec.Database.PostgresProfiles = &nominatimv1alpha1.PostgresProfiles{
		Import: map[string]string{"work_mem": "64MB"},
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode: nominatimv1alpha1.DatabaseModeClusterAttached,
	}
	if err := r.ApplyPostgresProfile(context.Background(), nom, "import"); err == nil {
		t.Fatal("expected missing cluster name")
	}
	nom.Status.Database.ClusterName = "missing"
	if err := r.ApplyPostgresProfile(context.Background(), nom, "import"); err == nil {
		t.Fatal("expected get error")
	}
	nom.Status.Database.ClusterName = "pg"
	if err := r.ApplyPostgresProfile(context.Background(), nom, "import"); err != nil {
		t.Fatal(err)
	}
	if effects.profileCalls != 1 || effects.lastParams["work_mem"] != "64MB" {
		t.Fatalf("import profile not applied: %#v", effects)
	}
	// empty runtime still clears import-only managed keys (work_mem)
	if err := r.ApplyPostgresProfile(context.Background(), nom, "runtime"); err != nil {
		t.Fatal(err)
	}
	if effects.profileCalls != 2 {
		t.Fatalf("empty runtime should still clear import-only keys, got %d calls", effects.profileCalls)
	}
	// nil profiles + import
	nom.Spec.Database.PostgresProfiles = nil
	if err := r.ApplyPostgresProfile(context.Background(), nom, "import"); err != nil {
		t.Fatal(err)
	}
	if effects.profileCalls != 2 {
		t.Fatalf("nil profiles must be a no-op, got %d", effects.profileCalls)
	}
}

func TestMapCNPGClusterToNominatim_EdgeCases(t *testing.T) {
	if reqs := mapCNPGClusterToNominatim(nil)(context.Background(), &corev1.Pod{}); len(reqs) != 0 {
		t.Fatalf("non-unstructured: %v", reqs)
	}
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("x")
	cluster.SetNamespace("default")
	cluster.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: nominatimv1alpha1.GroupVersion.String(),
		Kind:       "Nominatim",
		Name:       "from-owner",
		UID:        "uid",
	}})
	reqs := mapCNPGClusterToNominatim(nil)(context.Background(), cluster)
	if len(reqs) != 1 || reqs[0].Name != "from-owner" {
		t.Fatalf("nil client owner mapping: %v", reqs)
	}

	// Same Nominatim via ownerRef and clusterRef → dedupe in add().
	scheme := testScheme(t)
	nom := baseNominatim("same")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ClusterRef: &nominatimv1alpha1.DatabaseClusterRef{Name: "same-pg"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	cluster.SetName("same-pg")
	cluster.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: nominatimv1alpha1.GroupVersion.String(),
		Kind:       "Nominatim",
		Name:       "same",
		UID:        nom.UID,
	}})
	reqs = mapCNPGClusterToNominatim(c)(context.Background(), cluster)
	if len(reqs) != 1 {
		t.Fatalf("expected deduped request, got %v", reqs)
	}
}

type stubClient struct {
	client.Client
	failUpdate bool
	failList   bool
	failStatus bool
}

func (s stubClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if s.failUpdate {
		return fmt.Errorf("update failed")
	}
	return s.Client.Update(ctx, obj, opts...)
}

func (s stubClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if s.failList {
		return fmt.Errorf("list failed")
	}
	return s.Client.List(ctx, list, opts...)
}

func (s stubClient) Status() client.StatusWriter {
	if s.failStatus {
		return stubStatus{err: fmt.Errorf("status failed")}
	}
	return s.Client.Status()
}

type stubStatus struct{ err error }

func (s stubStatus) Create(context.Context, client.Object, client.Object, ...client.SubResourceCreateOption) error {
	return s.err
}
func (s stubStatus) Update(context.Context, client.Object, ...client.SubResourceUpdateOption) error {
	return s.err
}
func (s stubStatus) Patch(context.Context, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
	return s.err
}

func TestReconcile_ErrorPaths(t *testing.T) {
	scheme := testScheme(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
	}
	nom := baseNominatim("errpaths")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{Name: "s"},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(nom).WithObjects(nom, secret).Build()

	r := &NominatimReconciler{Client: base, Scheme: scheme}
	// NotFound
	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "missing", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("IgnoreNotFound: %v", err)
	}

	// Finalizer update failure
	r.Client = stubClient{Client: base, failUpdate: true}
	_, err = r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace},
	})
	if err == nil {
		t.Fatal("expected finalizer update error")
	}

	// Add finalizer successfully then fail status
	r.Client = base
	_, err = r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace},
	})
	if err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	r.Client = stubClient{Client: base, failStatus: true}
	_, err = r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace},
	})
	if err == nil {
		t.Fatal("expected status error")
	}
}

func TestReconcileDelete_NoFinalizerAndUpdateFail(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("delfail")
	nom.Finalizers = nil
	now := metav1.Now()
	nom.DeletionTimestamp = &now
	r := &NominatimReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme}
	res, err := r.reconcileDelete(context.Background(), nom)
	if err != nil || !res.IsZero() {
		t.Fatalf("no finalizer should be no-op: %v %#v", err, res)
	}

	nom2 := baseNominatim("delfail2")
	controllerutil.AddFinalizer(nom2, nominatimv1alpha1.NominatimFinalizer)
	nom2.DeletionTimestamp = &now
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom2).Build()
	r.Client = stubClient{Client: base, failUpdate: true}
	_, err = r.reconcileDelete(context.Background(), nom2)
	if err == nil {
		t.Fatal("expected update error removing finalizer")
	}
}

func TestDefaultCNPGEffects_ApplyParameters(t *testing.T) {
	scheme := testScheme(t)
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("pg")
	cluster.SetNamespace("default")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme} // default effects
	nom := baseNominatim("defeff")
	nom.Spec.Database.PostgresProfiles = &nominatimv1alpha1.PostgresProfiles{
		Runtime: map[string]string{"a": "b"},
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode:        nominatimv1alpha1.DatabaseModeClusterAttached,
		ClusterName: "pg",
	}
	if err := r.ApplyPostgresProfile(context.Background(), nom, "runtime"); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileDatabase_Cluster_BadSpecAndBadOwner(t *testing.T) {
	scheme := testScheme(t)
	nom := baseNominatim("bads")
	nom.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		Cluster: &nominatimv1alpha1.DatabaseClusterCreate{},
	}
	// Pre-create cluster with non-map spec so SetNestedField fails inside mutate.
	existing := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": CNPGClusterGVK.GroupVersion().String(),
		"kind":       CNPGClusterGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      OwnedCNPGClusterName(nom),
			"namespace": "default",
		},
		"spec": "not-a-map",
	}}
	existing.SetGroupVersionKind(CNPGClusterGVK)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom, existing).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileDatabase(context.Background(), nom); err == nil {
		t.Fatal("expected SetNestedField failure")
	}

	// Scheme without Nominatim → SetControllerReference fails (cluster create path).
	emptyScheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(emptyScheme)
	nom2 := baseNominatim("noowner")
	nom2.Spec.Database = nominatimv1alpha1.DatabaseSpec{
		Cluster: &nominatimv1alpha1.DatabaseClusterCreate{},
	}
	c2 := fake.NewClientBuilder().WithScheme(emptyScheme).Build()
	r2 := &NominatimReconciler{Client: c2, Scheme: emptyScheme}
	if err := r2.reconcileDatabase(context.Background(), nom2); err == nil {
		t.Fatal("expected SetControllerReference failure")
	}
}

func TestMapCNPGClusterToNominatim_ListError(t *testing.T) {
	scheme := testScheme(t)
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	c := stubClient{Client: base, failList: true}
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName("pg")
	cluster.SetNamespace("default")
	reqs := mapCNPGClusterToNominatim(c)(context.Background(), cluster)
	if len(reqs) != 0 {
		t.Fatalf("list error should return owner-only reqs, got %v", reqs)
	}
}
