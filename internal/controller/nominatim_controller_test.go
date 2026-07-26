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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

func minimalNominatim(name string) *nominatimv1alpha1.Nominatim {
	return &nominatimv1alpha1.Nominatim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: nominatimv1alpha1.NominatimSpec{
			Project: nominatimv1alpha1.ProjectSpec{
				Volume: nominatimv1alpha1.VolumeSource{
					ClaimName: "nominatim-project",
				},
			},
			Database: nominatimv1alpha1.DatabaseSpec{
				ClusterRef: &nominatimv1alpha1.LocalObjectReference{Name: "nominatim-pg"},
			},
		},
	}
}

var _ = Describe("Nominatim Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		nominatim := &nominatimv1alpha1.Nominatim{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Nominatim")
			err := k8sClient.Get(ctx, typeNamespacedName, nominatim)
			if err != nil && errors.IsNotFound(err) {
				resource := minimalNominatim(resourceName)
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &nominatimv1alpha1.Nominatim{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance Nominatim")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &NominatimReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject a Nominatim without spec.project", func() {
			invalid := &nominatimv1alpha1.Nominatim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "missing-project",
					Namespace: "default",
				},
				Spec: nominatimv1alpha1.NominatimSpec{
					Database: nominatimv1alpha1.DatabaseSpec{
						ClusterRef: &nominatimv1alpha1.LocalObjectReference{Name: "nominatim-pg"},
					},
				},
			}
			err := k8sClient.Create(ctx, invalid)
			Expect(err).To(HaveOccurred())
			Expect(errors.IsInvalid(err) || errors.IsBadRequest(err)).To(BeTrue(),
				"expected Invalid/BadRequest, got: %v", err)
		})

		It("should allow create without optional flatnode", func() {
			resource := minimalNominatim("no-flatnode")
			Expect(resource.Spec.Flatnode).To(BeNil())
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
	})
})
