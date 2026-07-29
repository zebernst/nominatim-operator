/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

    11|Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// Manager-driven reconcile proves SetupWithManager watches actually fire: the API
// Deployment must appear after creating a Nominatim without ever calling Reconcile()
// from the test (nominatim-5et.29).
var _ = Describe("Nominatim manager-driven reconcile", Ordered, func() {
	const (
		nomName    = "mgr-driven"
		secretName = "mgr-driven-pg"
	)

	var (
		mgrCancel context.CancelFunc
		nomKey    = types.NamespacedName{Name: nomName, Namespace: "default"}
	)

	BeforeAll(func() {
		By("starting a manager with the Nominatim reconciler registered")
		mgrCtx, cancel := context.WithCancel(context.Background())
		mgrCancel = cancel

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: k8sClient.Scheme(),
			// Disable the metrics listener so parallel envtest suites do not collide on :8080.
			Metrics: metricsserver.Options{BindAddress: "0"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect((&NominatimReconciler{
			Client:         mgr.GetClient(),
			Scheme:         mgr.GetScheme(),
			ControllerName: "nominatim-manager-driven",
		}).SetupWithManager(mgr)).To(Succeed())

		go func() {
			defer GinkgoRecover()
			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()
		Expect(mgr.GetCache().WaitForCacheSync(mgrCtx)).To(BeTrue())
	})

	AfterAll(func() {
		if mgrCancel != nil {
			mgrCancel()
		}
	})

	AfterEach(func() {
		nom := &nominatimv1alpha1.Nominatim{}
		err := k8sClient.Get(context.Background(), nomKey, nom)
		if apierrors.IsNotFound(err) {
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Delete(context.Background(), nom)).To(Succeed())
		Eventually(func() bool {
			err := k8sClient.Get(context.Background(), nomKey, &nominatimv1alpha1.Nominatim{})
			return apierrors.IsNotFound(err)
		}, 30*time.Second, time.Second).Should(BeTrue())
	})

	It("creates the API Deployment via watches without calling Reconcile()", func() {
		By("creating a connection Secret and a regions-empty Nominatim (smoke/attach mode)")
		Expect(k8sClient.Create(context.Background(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
			Data:       map[string][]byte{"uri": []byte("postgres://nominatim")},
		})).To(Succeed())

		Expect(k8sClient.Create(context.Background(), &nominatimv1alpha1.Nominatim{
			ObjectMeta: metav1.ObjectMeta{Name: nomName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimSpec{
				Project: nominatimv1alpha1.ProjectSpec{
					Volume: nominatimv1alpha1.VolumeSource{ClaimName: "nominatim-project"},
				},
				Database: nominatimv1alpha1.DatabaseSpec{
					ConnectionSecretRef: &nominatimv1alpha1.LocalObjectReference{Name: secretName},
				},
				API: &nominatimv1alpha1.APISpec{Replicas: int32Ptr(1)},
			},
		})).To(Succeed())

		By("waiting for the owned API Deployment (manager watches only — no direct Reconcile)")
		Eventually(func(g Gomega) {
			deploy := &appsv1.Deployment{}
			err := k8sClient.Get(context.Background(),
				types.NamespacedName{Name: nomName + "-api", Namespace: "default"}, deploy)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(deploy.Spec.Replicas).NotTo(BeNil())
			g.Expect(*deploy.Spec.Replicas).To(Equal(int32(1)))
		}, 30*time.Second, 500*time.Millisecond).Should(Succeed())
	})
})

func int32Ptr(v int32) *int32 { return &v }
