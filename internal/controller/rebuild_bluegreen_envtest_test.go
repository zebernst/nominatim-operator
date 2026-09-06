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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// BlueGreen envtest acceptance (nominatim-5et.20.3):
// create pending Cluster (schema-valid) → cutover flips connection secret → GC skips
// while the rollback window is still open.
//
// envtest has no CNPG controller: Ready/applied status and app Secrets are simulated.

var _ = Describe("BlueGreen Rebuild against vendored CNPG schema", func() {
	var (
		instanceR *NominatimInstanceReconciler
		opR       *NominatimOperationReconciler
	)

	BeforeEach(func() {
		instanceR = &NominatimInstanceReconciler{Client: k8sClient, Scheme: scheme.Scheme}
		opR = &NominatimOperationReconciler{Client: k8sClient, Scheme: scheme.Scheme}
	})

	It("provisions a pending Cluster, cuts over the secret, and skips GC inside the rollback window", func() {
		nom := ownedClusterNominatim("envtest-bg", 1)
		nom.Spec.Database.RebuildStrategy = nominatimv1alpha1.RebuildStrategyBlueGreen
		nom.Spec.Project.Volume = nominatimv1alpha1.VolumeSource{
			VolumeClaimTemplate: &nominatimv1alpha1.VolumeClaimTemplate{
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
		}
		nom.Spec.Regions = []string{"europe/monaco"}
		nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}}
		nom = persistNominatim(nom)

		By("reconciling owned (blue) Cluster so status.database is ClusterManaged")
		Expect(instanceR.reconcileDatabase(ctx, nom)).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, nom)).To(Succeed())
		blueName := OwnedCNPGClusterName(nom)
		Expect(nom.Status.Database.ClusterName).To(Equal(blueName))

		By("creating the live (blue) app Secret the API would use")
		blueSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      CNPGAppSecretName(blueName),
				Namespace: nom.Namespace,
			},
			Data: map[string][]byte{"uri": []byte("postgres://blue")},
		}
		Expect(k8sClient.Create(ctx, blueSecret)).To(Succeed())

		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: nom.Name + "-rebuild-bg", Namespace: nom.Namespace},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:                 nominatimv1alpha1.NominatimOperationRebuild,
				NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
				Regions:              []string{"europe/monaco"},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		By("provisioning the pending (green) Cluster + Database + project PVC")
		ready, err := opR.ensureBlueGreenPending(ctx, op, nom)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready).To(BeFalse(), "pending is not Ready until we simulate CNPG status")

		greenName := BlueGreenClusterName(nom)
		green := getUnstructured(CNPGClusterGVK, greenName, nom.Namespace)
		Expect(green.GetOwnerReferences()).NotTo(BeEmpty())
		assertOwnedClusterBootstrapAndRoles(GinkgoT(), green)

		greenDB := getUnstructured(CNPGDatabaseGVK, CNPGDatabaseNameForCluster(greenName), nom.Namespace)
		Expect(greenDB.GetName()).To(Equal(greenName + "-nominatim"))

		projectPVC := &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: BlueGreenProjectPVCName(nom), Namespace: nom.Namespace,
		}, projectPVC)).To(Succeed())

		By("simulating CNPG: green Ready + applied Database + app Secret")
		now := time.Now().UTC().Format(time.RFC3339)
		Expect(unstructured.SetNestedSlice(green.Object, []interface{}{
			map[string]interface{}{
				"type":               "Ready",
				"status":             "True",
				"reason":             "ClusterIsReady",
				"message":            "Cluster is ready",
				"lastTransitionTime": now,
			},
		}, "status", "conditions")).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, green)).To(Succeed())

		Expect(unstructured.SetNestedField(greenDB.Object, true, "status", "applied")).To(Succeed())
		// Database CR status may reject partial updates; fall back to Merge if needed.
		if err := k8sClient.Status().Update(ctx, greenDB); err != nil {
			Expect(k8sClient.Update(ctx, greenDB)).To(Succeed())
		}
		greenSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      CNPGAppSecretName(greenName),
				Namespace: nom.Namespace,
			},
			Data: map[string][]byte{"uri": []byte("postgres://green")},
		}
		Expect(k8sClient.Create(ctx, greenSecret)).To(Succeed())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}, nom)).To(Succeed())
		ready, err = opR.ensureBlueGreenPending(ctx, op, nom)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready).To(BeTrue())

		By("advancing import → ReadyToCutover → Cutover (flips status.database)")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}, nom)).To(Succeed())
		Expect(nom.Status.Rebuild).NotTo(BeNil())
		nom.Status.Rebuild.Phase = nominatimv1alpha1.RebuildPhaseImporting
		Expect(k8sClient.Status().Update(ctx, nom)).To(Succeed())

		op.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseSucceeded
		op.Status.Message = "Job succeeded"
		Expect(k8sClient.Status().Update(ctx, op)).To(Succeed())

		advanceBlueGreenEnvtest := func() {
			GinkgoHelper()
			curOp := &nominatimv1alpha1.NominatimOperation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: op.Name, Namespace: op.Namespace}, curOp)).To(Succeed())
			curNom := &nominatimv1alpha1.NominatimInstance{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}, curNom)).To(Succeed())
			_, err := opR.reconcileBlueGreenAfterImport(ctx, curOp, curNom)
			Expect(err).NotTo(HaveOccurred())
		}

		advanceBlueGreenEnvtest() // Importing → ReadyToCutover
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}, nom)).To(Succeed())
		Expect(nom.Status.Rebuild.Phase).To(Equal(nominatimv1alpha1.RebuildPhaseReadyToCutover))

		advanceBlueGreenEnvtest() // ReadyToCutover → Cutover + secret flip
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}, nom)).To(Succeed())
		Expect(nom.Status.Database.ClusterName).To(Equal(greenName))
		Expect(nom.Status.Database.ConnectionSecretName).To(Equal(CNPGAppSecretName(greenName)))
		Expect(nom.Status.Rebuild.Phase).To(Equal(nominatimv1alpha1.RebuildPhaseCutover))

		advanceBlueGreenEnvtest() // Cutover ready → RollbackWindow
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}, nom)).To(Succeed())
		Expect(nom.Status.Rebuild.Phase).To(Equal(nominatimv1alpha1.RebuildPhaseRollbackWindow))
		Expect(nom.Status.Rebuild.RollbackUntil).NotTo(BeNil())

		By("GC must not delete blue while the rollback window is still open")
		future := metav1.NewTime(time.Now().UTC().Add(time.Hour))
		nom.Status.Rebuild.RollbackUntil = &future
		Expect(k8sClient.Status().Update(ctx, nom)).To(Succeed())
		advanceBlueGreenEnvtest()

		blue := &unstructured.Unstructured{}
		blue.SetGroupVersionKind(CNPGClusterGVK)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: blueName, Namespace: nom.Namespace}, blue)).To(Succeed(),
			"blue Cluster must remain during rollback window")

		By("after the window expires, GC deletes blue")
		past := metav1.NewTime(time.Now().UTC().Add(-time.Minute))
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nom.Name, Namespace: nom.Namespace}, nom)).To(Succeed())
		nom.Status.Rebuild.RollbackUntil = &past
		nom.Status.Rebuild.Phase = nominatimv1alpha1.RebuildPhaseRollbackWindow
		Expect(k8sClient.Status().Update(ctx, nom)).To(Succeed())
		advanceBlueGreenEnvtest()

		err = k8sClient.Get(ctx, types.NamespacedName{Name: blueName, Namespace: nom.Namespace}, blue)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "blue Cluster should be gone after window: %v", err)

		// Green (live) Cluster must still exist.
		_ = getUnstructured(CNPGClusterGVK, greenName, nom.Namespace)
	})

	It("rejects BlueGreen Rebuild when the instance is clusterRef-attached", func() {
		extCluster := &unstructured.Unstructured{}
		extCluster.SetGroupVersionKind(CNPGClusterGVK)
		extCluster.SetName("external-pg")
		extCluster.SetNamespace("default")
		Expect(unstructured.SetNestedField(extCluster.Object, int64(1), "spec", "instances")).To(Succeed())
		Expect(k8sClient.Create(ctx, extCluster)).To(Succeed())

		nom := &nominatimv1alpha1.NominatimInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "envtest-bg-reject", Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimInstanceSpec{
				Project: nominatimv1alpha1.ProjectSpec{
					Volume: nominatimv1alpha1.VolumeSource{ClaimName: "project"},
				},
				Database: nominatimv1alpha1.DatabaseSpec{
					ClusterRef:      &nominatimv1alpha1.DatabaseClusterRef{Name: "external-pg"},
					RebuildStrategy: nominatimv1alpha1.RebuildStrategyBlueGreen,
				},
				Regions: []string{"europe/monaco"},
			},
		}
		nom = persistNominatim(nom)
		nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
			Mode:                 nominatimv1alpha1.DatabaseModeClusterAttached,
			ClusterName:          "external-pg",
			ConnectionSecretName: "external-pg-app",
		}
		nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}}
		Expect(k8sClient.Status().Update(ctx, nom)).To(Succeed())

		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "external-pg-app", Namespace: nom.Namespace},
			Data:       map[string][]byte{"uri": []byte("postgres://ext")},
		})).To(Succeed())

		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: "envtest-bg-reject-op", Namespace: nom.Namespace},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:                 nominatimv1alpha1.NominatimOperationRebuild,
				NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := opR.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: op.Name, Namespace: op.Namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: op.Name, Namespace: op.Namespace}, op)).To(Succeed())
		Expect(op.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhaseFailed))
		Expect(op.Status.Message).To(ContainSubstring(reasonUnsupportedRebuild))
	})
})
