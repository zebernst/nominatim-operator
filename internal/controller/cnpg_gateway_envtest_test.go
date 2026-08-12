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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// The operator writes CNPG Cluster/Database and Gateway API HTTPRoute as unstructured, so
// nothing in its own type system checks those payloads. These specs run the same reconcile
// functions against envtest with the upstream schemas vendored in test/crds/, which makes
// the API server reject wrong field names, types, and enum values.
//
// envtest runs no CNPG or Gateway controller: nothing here reconciles the objects further,
// so status stays empty and no Postgres or Gateway is programmed.

// persistNominatim creates nom in envtest so the NominatimInstance CRD validates the spec and the
// API server assigns the real UID that ends up in the owner references under test.
func persistNominatim(nom *nominatimv1alpha1.NominatimInstance) *nominatimv1alpha1.NominatimInstance {
	GinkgoHelper()
	nom.UID = ""
	nom.ResourceVersion = ""
	Expect(k8sClient.Create(ctx, nom)).To(Succeed())
	return nom
}

func getUnstructured(gvk schema.GroupVersionKind, name, namespace string) *unstructured.Unstructured {
	GinkgoHelper()
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(gvk)
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, got)).To(Succeed())
	return got
}

var _ = Describe("owned CNPG objects against the vendored CNPG schema", func() {
	var reconciler *NominatimInstanceReconciler

	BeforeEach(func() {
		reconciler = &NominatimInstanceReconciler{Client: k8sClient, Scheme: scheme.Scheme}
	})

	It("creates a Cluster and Database the API server accepts, without churning them", func() {
		nom := persistNominatim(ownedClusterNominatim("envtest-owned", 2))
		Expect(reconciler.reconcileDatabase(ctx, nom)).To(Succeed())

		clusterName := OwnedCNPGClusterName(nom)
		assertOwnedClusterCreate(GinkgoT(), k8sClient, nom, clusterName)
		assertOwnedDatabaseCR(GinkgoT(), k8sClient, nom, clusterName)

		By("re-reconciling without rewriting either object")
		clusterRV := getUnstructured(CNPGClusterGVK, clusterName, nom.Namespace).GetResourceVersion()
		dbRV := getUnstructured(CNPGDatabaseGVK, OwnedCNPGDatabaseName(nom), nom.Namespace).GetResourceVersion()

		Expect(reconciler.reconcileDatabase(ctx, nom)).To(Succeed())

		Expect(getUnstructured(CNPGClusterGVK, clusterName, nom.Namespace).GetResourceVersion()).
			To(Equal(clusterRV), "second reconcile rewrote the CNPG Cluster; CNPG-expanded defaults are churning")
		Expect(getUnstructured(CNPGDatabaseGVK, OwnedCNPGDatabaseName(nom), nom.Namespace).GetResourceVersion()).
			To(Equal(dbRV), "second reconcile rewrote the CNPG Database")
	})

	It("keeps CNPG-expanded managed.roles fields on re-reconcile", func() {
		nom := persistNominatim(ownedClusterNominatim("envtest-roles", 2))
		Expect(reconciler.reconcileDatabase(ctx, nom)).To(Succeed())
		clusterName := OwnedCNPGClusterName(nom)

		By("simulating CNPG expanding the www-data role with its own defaults")
		expanded := getUnstructured(CNPGClusterGVK, clusterName, nom.Namespace)
		Expect(unstructured.SetNestedSlice(expanded.Object, []interface{}{
			map[string]interface{}{
				"name":            cnpgNominatimWebRole,
				"ensure":          "present",
				"login":           false,
				"inherit":         true,
				"connectionLimit": int64(-1),
				"comment":         "Nominatim DATABASE_WEBUSER (read-only grants target)",
			},
		}, "spec", "managed", "roles")).To(Succeed())
		Expect(k8sClient.Update(ctx, expanded)).To(Succeed())

		Expect(reconciler.reconcileDatabase(ctx, nom)).To(Succeed())

		roles, found, err := unstructured.NestedSlice(
			getUnstructured(CNPGClusterGVK, clusterName, nom.Namespace).Object, "spec", "managed", "roles")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(roles).To(HaveLen(1))
		Expect(roles[0]).To(HaveKeyWithValue("connectionLimit", int64(-1)))
		Expect(roles[0]).To(HaveKeyWithValue("inherit", true))
	})

	It("writes instance-tune fields the CNPG schema recognises", func() {
		nom := ownedClusterNominatim("envtest-tune", 1)
		nom.Spec.Database.Cluster.Resources = &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
		}
		nom.Spec.Database.Cluster.Affinity = &runtime.RawExtension{
			Raw: []byte(`{"nodeSelector":{"node-role.kubernetes.io/postgres":""},` +
				`"enablePodAntiAffinity":true,"topologyKey":"kubernetes.io/hostname"}`),
		}
		nom.Spec.Database.Cluster.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{
			MaxSkew:           1,
			TopologyKey:       "topology.kubernetes.io/zone",
			WhenUnsatisfiable: corev1.DoNotSchedule,
			LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "pg"}},
		}}
		persistNominatim(nom)

		Expect(reconciler.reconcileDatabase(ctx, nom)).To(Succeed())

		// Every field below survived CRD pruning, which is the point: a typo or a wrong
		// type here would have been silently dropped (or rejected) by the API server.
		got := getUnstructured(CNPGClusterGVK, OwnedCNPGClusterName(nom), nom.Namespace)
		mem, found, err := unstructured.NestedString(got.Object, "spec", "resources", "requests", "memory")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(mem).To(Equal("1Gi"))

		nodeSelector, found, err := unstructured.NestedStringMap(got.Object, "spec", "affinity", "nodeSelector")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(nodeSelector).To(HaveKeyWithValue("node-role.kubernetes.io/postgres", ""))

		antiAffinity, found, err := unstructured.NestedBool(got.Object, "spec", "affinity", "enablePodAntiAffinity")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(antiAffinity).To(BeTrue())

		constraints, found, err := unstructured.NestedSlice(got.Object, "spec", "topologySpreadConstraints")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(constraints).To(HaveLen(1))

		image, _, _ := unstructured.NestedString(got.Object, "spec", "imageName")
		Expect(image).To(Equal(cnpgDefaultPostGISImage), "operator-owned imageName must not be overridable")
	})

	It("resumes backups through the CNPG Cluster it just created", func() {
		nom := persistNominatim(ownedClusterNominatim("envtest-resume", 1))
		effects := &recordingCNPGEffects{}

		Expect(reconciler.reconcileDatabase(ctx, nom)).To(Succeed())
		Expect(setBackupPaused(ctx, k8sClient, effects, nom, false)).To(Succeed())

		Expect(effects.resumeCalls).To(Equal(1))
		Expect(effects.lastCluster).To(Equal(OwnedCNPGClusterName(nom)))
	})

	It("rejects a Cluster that violates the CNPG schema", func() {
		cluster := &unstructured.Unstructured{}
		cluster.SetGroupVersionKind(CNPGClusterGVK)
		cluster.SetName("envtest-schema-guard")
		cluster.SetNamespace("default")
		Expect(unstructured.SetNestedField(cluster.Object, int64(0), "spec", "instances")).To(Succeed())

		err := k8sClient.Create(ctx, cluster)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(),
			"CNPG requires spec.instances >= 1; got %v (is test/crds still loaded?)", err)
	})

	It("rejects a Database whose databaseReclaimPolicy is not a CNPG enum value", func() {
		db := &unstructured.Unstructured{}
		db.SetGroupVersionKind(CNPGDatabaseGVK)
		db.SetName("envtest-reclaim-guard")
		db.SetNamespace("default")
		Expect(applyOwnedCNPGDatabaseSpec(db, "envtest-reclaim-guard-pg")).To(Succeed())
		// The operator writes lowercase "delete"; prove the enum is actually enforced.
		Expect(unstructured.SetNestedField(db.Object, "Delete", "spec", "databaseReclaimPolicy")).To(Succeed())

		err := k8sClient.Create(ctx, db)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(),
			"databaseReclaimPolicy enum should reject \"Delete\"; got %v", err)
	})
})

var _ = Describe("owned HTTPRoutes against the vendored Gateway API schema", func() {
	var reconciler *NominatimInstanceReconciler

	BeforeEach(func() {
		reconciler = &NominatimInstanceReconciler{Client: k8sClient, Scheme: scheme.Scheme}
	})

	It("creates the API HTTPRoute alongside the Deployment and Service", func() {
		group := "gateway.networking.k8s.io"
		kind := "Gateway"
		nom := nominatimWithConnectionSecret("envtest-api-route")
		nom.Spec.API = &nominatimv1alpha1.APISpec{
			Route: &nominatimv1alpha1.RouteSpec{
				ParentRefs: []nominatimv1alpha1.ParentReference{
					{Name: "my-gateway", Group: &group, Kind: &kind},
				},
				Hostnames: []string{"nominatim.example.com"},
			},
		}
		persistNominatim(nom)
		nom.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: testConnectionSecretName}

		Expect(reconciler.reconcileAPI(ctx, nom)).To(Succeed())

		route := getUnstructured(HTTPRouteGVK, APIName(nom), nom.Namespace)

		hostnames, found, err := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(hostnames).To(ConsistOf("nominatim.example.com"))

		parentRefs, found, err := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(parentRefs).To(HaveLen(1))
		Expect(parentRefs[0]).To(SatisfyAll(
			HaveKeyWithValue("name", "my-gateway"),
			HaveKeyWithValue("group", group),
			HaveKeyWithValue("kind", kind),
		))

		rules, found, err := unstructured.NestedSlice(route.Object, "spec", "rules")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(rules).To(HaveLen(1))
		backendRefs, ok := rules[0].(map[string]interface{})["backendRefs"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(backendRefs).To(HaveLen(1))
		Expect(backendRefs[0]).To(SatisfyAll(
			HaveKeyWithValue("name", APIName(nom)),
			HaveKeyWithValue("port", int64(workloadServicePort)),
		))

		owners := route.GetOwnerReferences()
		Expect(owners).To(HaveLen(1))
		Expect(owners[0].Name).To(Equal(nom.Name))
		Expect(owners[0].UID).To(Equal(nom.UID))
	})

	It("passes parentRef namespace and sectionName through to a valid HTTPRoute", func() {
		nom := persistNominatim(nominatimWithConnectionSecret("envtest-route-parent"))
		gatewayNS := "gw-ns"
		section := "https"
		route := &nominatimv1alpha1.RouteSpec{
			ParentRefs: []nominatimv1alpha1.ParentReference{
				{Name: "gw", Namespace: &gatewayNS, SectionName: &section},
			},
		}

		Expect(reconciler.reconcileHTTPRoute(ctx, nom, "envtest-full-route", "svc", route, ComponentAPI)).To(Succeed())

		parentRefs, found, err := unstructured.NestedSlice(
			getUnstructured(HTTPRouteGVK, "envtest-full-route", nom.Namespace).Object, "spec", "parentRefs")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(parentRefs).To(HaveLen(1))
		Expect(parentRefs[0]).To(SatisfyAll(
			HaveKeyWithValue("namespace", gatewayNS),
			HaveKeyWithValue("sectionName", section),
		))
	})

	It("drops hostnames again when the route spec stops listing them", func() {
		nom := persistNominatim(nominatimWithConnectionSecret("envtest-route-hostnames"))
		withHost := &nominatimv1alpha1.RouteSpec{
			ParentRefs: []nominatimv1alpha1.ParentReference{{Name: "gw"}},
			Hostnames:  []string{"first.example.com"},
		}
		Expect(reconciler.reconcileHTTPRoute(ctx, nom, "envtest-hostnames", "svc", withHost, ComponentAPI)).To(Succeed())

		withoutHost := &nominatimv1alpha1.RouteSpec{
			ParentRefs: []nominatimv1alpha1.ParentReference{{Name: "gw"}},
		}
		Expect(reconciler.reconcileHTTPRoute(ctx, nom, "envtest-hostnames", "svc", withoutHost, ComponentAPI)).To(Succeed())

		_, found, err := unstructured.NestedSlice(
			getUnstructured(HTTPRouteGVK, "envtest-hostnames", nom.Namespace).Object, "spec", "hostnames")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("rejects an HTTPRoute hostname the Gateway API schema disallows", func() {
		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(HTTPRouteGVK)
		route.SetName("envtest-hostname-guard")
		route.SetNamespace("default")
		Expect(unstructured.SetNestedStringSlice(route.Object, []string{"not a hostname"}, "spec", "hostnames")).To(Succeed())

		err := k8sClient.Create(ctx, route)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(),
			"Gateway API should reject a malformed hostname; got %v (is test/crds still loaded?)", err)
	})
})

var _ = Describe("optional GVK discovery with the vendored CRDs installed", func() {
	It("finds HTTPRoute, CNPG Cluster, and CNPG Database through the live RESTMapper", func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: k8sClient.Scheme()})
		Expect(err).NotTo(HaveOccurred())

		for _, gvk := range []schema.GroupVersionKind{HTTPRouteGVK, CNPGClusterGVK, CNPGDatabaseGVK} {
			available, err := gvkAvailableFromMapper(mgr.GetRESTMapper(), gvk)
			Expect(err).NotTo(HaveOccurred())
			Expect(available).To(BeTrue(), "%s should be discoverable from test/crds", gvk)
		}

		r := &NominatimInstanceReconciler{
			Client:         mgr.GetClient(),
			Scheme:         mgr.GetScheme(),
			ControllerName: "nominatim-live-mapper",
		}
		Expect(r.SetupWithManager(mgr)).To(Succeed())
	})
})
