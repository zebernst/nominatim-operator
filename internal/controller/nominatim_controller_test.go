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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

func minimalNominatim(name string) *nominatimv1alpha1.NominatimInstance {
	return &nominatimv1alpha1.NominatimInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: nominatimv1alpha1.NominatimInstanceSpec{
			Project: nominatimv1alpha1.ProjectSpec{
				Volume: nominatimv1alpha1.VolumeSource{
					ClaimName: "nominatim-project",
				},
			},
			// Degraded secret mode keeps envtest free of CNPG Cluster CRDs.
			Database: nominatimv1alpha1.DatabaseSpec{
				ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{Name: "nominatim-pg-secret"},
			},
		},
	}
}

func ensureConnectionSecret(ctx context.Context, name string) {
	sec := &corev1.Secret{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, sec)
	if errors.IsNotFound(err) {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Data:       map[string][]byte{"uri": []byte("postgres://nominatim")},
		})).To(Succeed())
	}
}

func conditionStatus(nom *nominatimv1alpha1.NominatimInstance, condType string) metav1.ConditionStatus {
	c := meta.FindStatusCondition(nom.Status.Conditions, condType)
	if c == nil {
		return ""
	}
	return c.Status
}

var _ = Describe("NominatimInstance Controller", func() {
	ctx := context.Background()

	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			By("creating connection secret and NominatimInstance resource")
			ensureConnectionSecret(ctx, "nominatim-pg-secret")
			nom := &nominatimv1alpha1.NominatimInstance{}
			err := k8sClient.Get(ctx, typeNamespacedName, nom)
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, minimalNominatim(resourceName))).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &nominatimv1alpha1.NominatimInstance{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if errors.IsNotFound(err) {
				return
			}
			Expect(err).NotTo(HaveOccurred())
			resource.Status.ActiveOperationRefs = nil
			_ = k8sClient.Status().Update(ctx, resource)
			controllerutil.RemoveFinalizer(resource, nominatimv1alpha1.NominatimInstanceFinalizer)
			_ = k8sClient.Update(ctx, resource)
			err = k8sClient.Delete(ctx, resource)
			Expect(client.IgnoreNotFound(err)).NotTo(HaveOccurred())
			Eventually(func() bool {
				err := k8sClient.Get(ctx, typeNamespacedName, &nominatimv1alpha1.NominatimInstance{})
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})

		It("should successfully reconcile the resource", func() {
			controllerReconciler := &NominatimInstanceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			// Finalizer add returns early; second pass updates status.
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject a NominatimInstance without spec.project", func() {
			invalid := &nominatimv1alpha1.NominatimInstance{
				ObjectMeta: metav1.ObjectMeta{Name: "missing-project", Namespace: "default"},
				Spec: nominatimv1alpha1.NominatimInstanceSpec{
					Database: nominatimv1alpha1.DatabaseSpec{
						ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{Name: "nominatim-pg-secret"},
					},
				},
			}
			err := k8sClient.Create(ctx, invalid)
			Expect(err).To(HaveOccurred())
			Expect(errors.IsInvalid(err) || errors.IsBadRequest(err)).To(BeTrue())
		})

		It("should allow create without optional flatnode", func() {
			ensureConnectionSecret(ctx, "nominatim-pg-secret")
			resource := minimalNominatim("no-flatnode")
			Expect(resource.Spec.Flatnode).To(BeNil())
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should set Ready=True when desired and observed regions are empty", func() {
			r := &NominatimInstanceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			nom := &nominatimv1alpha1.NominatimInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, nom)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(nom, nominatimv1alpha1.NominatimInstanceFinalizer)).To(BeTrue())
			Expect(nom.Status.ObservedGeneration).To(Equal(nom.Generation))
			Expect(nom.Status.Database.ConnectionSecretName).To(Equal("nominatim-pg-secret"))
			Expect(nom.Status.Database.Degraded).To(BeTrue())
			Expect(conditionStatus(nom, nominatimv1alpha1.ConditionReady)).To(Equal(metav1.ConditionTrue))
			Expect(conditionStatus(nom, nominatimv1alpha1.ConditionProgressing)).To(Equal(metav1.ConditionFalse))
			Expect(conditionStatus(nom, nominatimv1alpha1.ConditionRegionsDrift)).To(Equal(metav1.ConditionFalse))
		})

		It("should set Progressing when desired regions are missing from status", func() {
			nom := &nominatimv1alpha1.NominatimInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, nom)).To(Succeed())
			nom.Spec.Regions = []string{"north-america/us"}
			Expect(k8sClient.Update(ctx, nom)).To(Succeed())

			r := &NominatimInstanceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, nom)).To(Succeed())
			Expect(conditionStatus(nom, nominatimv1alpha1.ConditionReady)).To(Equal(metav1.ConditionFalse))
			Expect(conditionStatus(nom, nominatimv1alpha1.ConditionProgressing)).To(Equal(metav1.ConditionTrue))
			Expect(conditionStatus(nom, nominatimv1alpha1.ConditionRegionsDrift)).To(Equal(metav1.ConditionTrue))
		})

		It("should mark RegionRemovalUnsupported when status has regions removed from spec", func() {
			nom := &nominatimv1alpha1.NominatimInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, nom)).To(Succeed())
			r := &NominatimInstanceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})

			Expect(k8sClient.Get(ctx, typeNamespacedName, nom)).To(Succeed())
			nom.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/germany"}}
			Expect(k8sClient.Status().Update(ctx, nom)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, nom)).To(Succeed())
			Expect(conditionStatus(nom, nominatimv1alpha1.ConditionRegionRemovalUnsupported)).To(Equal(metav1.ConditionTrue))
			Expect(conditionStatus(nom, nominatimv1alpha1.ConditionReady)).To(Equal(metav1.ConditionFalse))
		})

		It("should update observedGeneration when reconcile-at annotation is nudged", func() {
			r := &NominatimInstanceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})

			nom := &nominatimv1alpha1.NominatimInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, nom)).To(Succeed())
			beforeGen := nom.Status.ObservedGeneration

			if nom.Annotations == nil {
				nom.Annotations = map[string]string{}
			}
			nom.Annotations[nominatimv1alpha1.ReconcileAtAnnotation] = "2026-07-26T00:00:00Z"
			Expect(k8sClient.Update(ctx, nom)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, nom)).To(Succeed())
			Expect(nom.Status.ObservedGeneration).To(BeNumerically(">=", beforeGen))
			Expect(nom.Annotations[nominatimv1alpha1.ReconcileAtAnnotation]).To(Equal("2026-07-26T00:00:00Z"))
			Expect(conditionStatus(nom, nominatimv1alpha1.ConditionReady)).To(Equal(metav1.ConditionTrue))
		})

		It("should error when connection secret is missing during reconcile", func() {
			r := &NominatimInstanceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})

			nom := &nominatimv1alpha1.NominatimInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, nom)).To(Succeed())
			nom.Spec.Database.ConnectionSecretRef = &nominatimv1alpha1.LocalObjectReference{Name: "does-not-exist"}
			Expect(k8sClient.Update(ctx, nom)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())
		})

		It("should remove finalizer on delete when no active operations", func() {
			r := &NominatimInstanceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})

			nom := &nominatimv1alpha1.NominatimInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, nom)).To(Succeed())
			Expect(k8sClient.Delete(ctx, nom)).To(Succeed())
			Expect(k8sClient.Get(ctx, typeNamespacedName, nom)).To(Succeed())
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() bool {
				err := k8sClient.Get(ctx, typeNamespacedName, &nominatimv1alpha1.NominatimInstance{})
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})

		It("should block finalizer removal while active operations remain", func() {
			r := &NominatimInstanceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})

			nom := &nominatimv1alpha1.NominatimInstance{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, nom)).To(Succeed())
			nom.Status.ActiveOperationRefs = []corev1.ObjectReference{{Name: "op-busy"}}
			Expect(k8sClient.Status().Update(ctx, nom)).To(Succeed())
			Expect(k8sClient.Delete(ctx, nom)).To(Succeed())
			Expect(k8sClient.Get(ctx, typeNamespacedName, nom)).To(Succeed())
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())
			Expect(controllerutil.ContainsFinalizer(nom, nominatimv1alpha1.NominatimInstanceFinalizer)).To(BeTrue())
			// Clear so AfterEach can delete.
			Expect(k8sClient.Get(ctx, typeNamespacedName, nom)).To(Succeed())
			nom.Status.ActiveOperationRefs = nil
			Expect(k8sClient.Status().Update(ctx, nom)).To(Succeed())
		})
	})

	Context("SetupWithManager", func() {
		It("registers NominatimInstance controller watches", func() {
			mgr, err := ctrl.NewManager(cfg, ctrl.Options{
				Scheme: k8sClient.Scheme(),
			})
			Expect(err).NotTo(HaveOccurred())
			r := &NominatimInstanceReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}
			Expect(r.SetupWithManager(mgr)).To(Succeed())
		})
	})

	Context("Operation watch mapping", func() {
		It("maps NominatimOperation nominatimInstanceRef to parent reconcile request", func() {
			op := &nominatimv1alpha1.NominatimOperation{
				ObjectMeta: metav1.ObjectMeta{Name: "op1", Namespace: "default"},
				Spec: nominatimv1alpha1.NominatimOperationSpec{
					Type:                 nominatimv1alpha1.NominatimOperationBootstrap,
					NominatimInstanceRef: nominatimv1alpha1.LocalObjectReference{Name: "parent-nom"},
				},
			}
			reqs := mapOperationToNominatimInstance(ctx, op)
			Expect(reqs).To(HaveLen(1))
			Expect(reqs[0].Name).To(Equal("parent-nom"))
			Expect(reqs[0].Namespace).To(Equal("default"))
		})

		It("ignores operations without nominatimInstanceRef", func() {
			Expect(mapOperationToNominatimInstance(ctx, &corev1.Pod{})).To(BeEmpty())
			Expect(mapOperationToNominatimInstance(ctx, &nominatimv1alpha1.NominatimOperation{})).To(BeEmpty())
		})
	})
})
