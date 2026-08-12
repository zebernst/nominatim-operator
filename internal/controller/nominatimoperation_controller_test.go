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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

var _ = Describe("NominatimOperation Controller", func() {
	ctx := context.Background()

	var (
		parentName string
		opName     string
		reconciler *NominatimOperationReconciler
	)

	BeforeEach(func() {
		parentName = "nom-" + uniqueSuffix()
		opName = "op-" + uniqueSuffix()
		reconciler = &NominatimOperationReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		parent := minimalNominatim(parentName)
		parent.Spec.Staging = &nominatimv1alpha1.StagingSpec{Size: "30Gi"}
		parent.Spec.Regions = []string{"europe/monaco"}
		Expect(k8sClient.Create(ctx, parent)).To(Succeed())
		parent.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: "pg-secret"}
		parent.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco", Phase: regionPhaseImported}}
		Expect(k8sClient.Status().Update(ctx, parent)).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "pg-secret", Namespace: "default"},
			Data:       map[string][]byte{"uri": []byte("postgresql://n:p@h/db")},
		})).To(Succeed())
	})

	AfterEach(func() {
		cleanupOperation(ctx, opName)
		// Second operation name used by conflict / multi-type tests.
		cleanupOperation(ctx, opName+"-b")
		cleanupOperation(ctx, opName+"-migrate")
		cleanupOperation(ctx, opName+"-freeze")
		cleanupOperation(ctx, opName+"-aaa")
		cleanupOperation(ctx, opName+"-bbb")
		cleanupNominatim(ctx, parentName)
		_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "pg-secret", Namespace: "default"}})
	})

	It("requeues Job creation until parent status.database.connectionSecretName is set", func() {
		parent := &nominatimv1alpha1.Nominatim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: parentName, Namespace: "default"}, parent)).To(Succeed())
		parent.Status.Database = nominatimv1alpha1.DatabaseStatus{}
		Expect(k8sClient.Status().Update(ctx, parent)).To(Succeed())

		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationBootstrap,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		res, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))

		job := &batchv1.Job{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("requeues Job creation until the connection Secret exists", func() {
		Expect(k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "pg-secret", Namespace: "default"}})).To(Succeed())

		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationBootstrap,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		res, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))

		job := &batchv1.Job{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)
		Expect(errors.IsNotFound(err)).To(BeTrue())

		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "pg-secret", Namespace: "default"},
			Data:       map[string][]byte{"uri": []byte("postgresql://n:p@h/db")},
		})).To(Succeed())

		_, err = reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)).To(Succeed())
		Expect(findEnvVar(job.Spec.Template.Spec.Containers[0].Env, "NOMINATIM_DATABASE_DSN")).NotTo(BeNil())
	})

	It("creates staging PVC and Job mounting project/staging volumes", func() {
		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationUpdate,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
				Regions:      []string{"europe/monaco"},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		pvc := &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName + "-staging", Namespace: "default"}, pvc)).To(Succeed())
		Expect(pvc.Spec.Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("30Gi")))
		Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteOnce))
		Expect(metav1.IsControlledBy(pvc, op)).To(BeTrue())

		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)).To(Succeed())
		Expect(metav1.IsControlledBy(job, op)).To(BeTrue())
		c := job.Spec.Template.Spec.Containers[0]
		Expect(c.Image).To(Equal(resolveImage(nil, defaultWorkerRepository)))
		Expect(envValue(c.Env, "OPERATION_TYPE")).To(Equal("Update"))
		Expect(job.Spec.Template.Spec.Volumes).To(HaveLen(2)) // project + staging (no flatnode)

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, op)).To(Succeed())
		Expect(op.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhasePending))
		Expect(op.Status.JobRef).NotTo(BeNil())
		Expect(op.Status.JobRef.Name).To(Equal(opName))
		Expect(op.OwnerReferences).NotTo(BeEmpty())
	})

	It("honors Operation.staging override over parent defaults", func() {
		sc := "fast-sc"
		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationBootstrap,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
				Staging: &nominatimv1alpha1.StagingSpec{
					Size:             "80Gi",
					StorageClassName: &sc,
				},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		pvc := &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName + "-staging", Namespace: "default"}, pvc)).To(Succeed())
		Expect(pvc.Spec.Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("80Gi")))
		Expect(pvc.Spec.StorageClassName).NotTo(BeNil())
		Expect(*pvc.Spec.StorageClassName).To(Equal("fast-sc"))
	})

	It("mounts flatnode when parent.spec.flatnode is set", func() {
		parent := &nominatimv1alpha1.Nominatim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: parentName, Namespace: "default"}, parent)).To(Succeed())
		parent.Spec.Flatnode = &nominatimv1alpha1.FlatnodeSpec{
			Volume: nominatimv1alpha1.VolumeSource{ClaimName: "flatnode-pvc"},
		}
		Expect(k8sClient.Update(ctx, parent)).To(Succeed())

		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationBootstrap,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)).To(Succeed())
		Expect(job.Spec.Template.Spec.Volumes).To(HaveLen(3))
		Expect(envValue(job.Spec.Template.Spec.Containers[0].Env, "NOMINATIM_FLATNODE_FILE")).To(Equal(flatnodeFilePath))
	})

	It("updates status.phase and times from Job", func() {
		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationUpdate,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
				Regions:      []string{"europe/monaco"},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)).To(Succeed())
		now := metav1.Now()
		job.Status.Active = 1
		job.Status.StartTime = &now
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, op)).To(Succeed())
		Expect(op.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhaseRunning))
		Expect(op.Status.StartTime).NotTo(BeNil())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)).To(Succeed())
		job.Status.Active = 0
		job.Status.Succeeded = 1
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, op)).To(Succeed())
		Expect(op.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhaseSucceeded))
		Expect(op.Status.CompletionTime).NotTo(BeNil())

		// Terminal reconcile keeps Job-derived status in sync.
		_, err = reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("marks Operation Failed when Job fails", func() {
		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationUpdate,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
				Regions:      []string{"europe/monaco"},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)).To(Succeed())
		job.Status.Failed = 1
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, op)).To(Succeed())
		Expect(op.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhaseFailed))
		Expect(op.Status.Message).To(ContainSubstring("failed"))
	})

	It("fails with Conflict when another write-heavy Operation is active", func() {
		first := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationBootstrap,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		}
		Expect(k8sClient.Create(ctx, first)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, first)).To(Succeed())
		first.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseRunning
		Expect(k8sClient.Status().Update(ctx, first)).To(Succeed())

		secondName := opName + "-b"
		second := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: secondName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationAddRegions,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
				Regions:      []string{"europe/monaco"},
			},
		}
		Expect(k8sClient.Create(ctx, second)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: secondName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secondName, Namespace: "default"}, second)).To(Succeed())
		Expect(second.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhaseFailed))
		Expect(second.Status.Message).To(ContainSubstring("Conflict"))

		job := &batchv1.Job{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: secondName, Namespace: "default"}, job)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("requeues the lexicographically younger write-heavy Operation in a creation race", func() {
		winnerName := opName + "-aaa"
		loserName := opName + "-bbb"
		Expect(k8sClient.Create(ctx, &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: winnerName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationBootstrap,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: loserName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationBootstrap,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		})).To(Succeed())

		// Simulate the winner claiming the write plane before either Job exists.
		parent := &nominatimv1alpha1.Nominatim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: parentName, Namespace: "default"}, parent)).To(Succeed())
		parent.Status.ActiveOperationRefs = []corev1.ObjectReference{{
			APIVersion: nominatimv1alpha1.GroupVersion.String(),
			Kind:       operationRefKind,
			Namespace:  "default",
			Name:       winnerName,
		}}
		Expect(k8sClient.Status().Update(ctx, parent)).To(Succeed())

		res, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: loserName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))

		loser := &nominatimv1alpha1.NominatimOperation{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: loserName, Namespace: "default"}, loser)).To(Succeed())
		Expect(loser.Status.Phase).NotTo(Equal(nominatimv1alpha1.NominatimOperationPhaseFailed))

		job := &batchv1.Job{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: loserName, Namespace: "default"}, job)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("fails Update with Conflict when a write-heavy Operation is active", func() {
		first := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationReimport,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		}
		Expect(k8sClient.Create(ctx, first)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, first)).To(Succeed())
		first.Status.Phase = nominatimv1alpha1.NominatimOperationPhasePending
		Expect(k8sClient.Status().Update(ctx, first)).To(Succeed())

		secondName := opName + "-b"
		second := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: secondName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationUpdate,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
				Regions:      []string{"europe/monaco"},
			},
		}
		Expect(k8sClient.Create(ctx, second)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: secondName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secondName, Namespace: "default"}, second)).To(Succeed())
		Expect(second.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhaseFailed))
		Expect(second.Status.Message).To(ContainSubstring("Conflict"))
	})

	It("fails when parent Nominatim is missing", func() {
		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationUpdate,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "missing-parent"},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, op)).To(Succeed())
		Expect(op.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhaseFailed))
	})

	It("fails AddRegions with RegionsRequired when no regions are configured (T5)", func() {
		parent := &nominatimv1alpha1.Nominatim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: parentName, Namespace: "default"}, parent)).To(Succeed())
		parent.Spec.Regions = nil
		Expect(k8sClient.Update(ctx, parent)).To(Succeed())
		parent.Status.Regions = []nominatimv1alpha1.RegionStatus{}
		Expect(k8sClient.Status().Update(ctx, parent)).To(Succeed())

		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationAddRegions,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, op)).To(Succeed())
		Expect(op.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhaseFailed))
		Expect(op.Status.Message).To(ContainSubstring("RegionsRequired"))

		job := &batchv1.Job{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)
		Expect(errors.IsNotFound(err)).To(BeTrue())

		pvc := &corev1.PersistentVolumeClaim{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: opName + "-staging", Namespace: "default"}, pvc)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("fails Update with BootstrapIncomplete when regions-mode parent has no Bootstrap history (T6)", func() {
		parent := &nominatimv1alpha1.Nominatim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: parentName, Namespace: "default"}, parent)).To(Succeed())
		parent.Spec.Regions = []string{"europe/monaco"}
		Expect(k8sClient.Update(ctx, parent)).To(Succeed())
		parent.Status.Regions = []nominatimv1alpha1.RegionStatus{}
		Expect(k8sClient.Status().Update(ctx, parent)).To(Succeed())

		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationUpdate,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, op)).To(Succeed())
		Expect(op.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhaseFailed))
		Expect(op.Status.Message).To(ContainSubstring("BootstrapIncomplete"))

		job := &batchv1.Job{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("fails CatchUp with RegionsRequired when no regions are configured (T6b)", func() {
		parent := &nominatimv1alpha1.Nominatim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: parentName, Namespace: "default"}, parent)).To(Succeed())
		parent.Spec.Regions = nil
		Expect(k8sClient.Update(ctx, parent)).To(Succeed())
		parent.Status.Regions = []nominatimv1alpha1.RegionStatus{}
		Expect(k8sClient.Status().Update(ctx, parent)).To(Succeed())

		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationCatchUp,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, op)).To(Succeed())
		Expect(op.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhaseFailed))
		Expect(op.Status.Message).To(ContainSubstring("RegionsRequired"))

		job := &batchv1.Job{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("fails AddRegions with BootstrapIncomplete when regions-mode parent has no Bootstrap history (T6c)", func() {
		parent := &nominatimv1alpha1.Nominatim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: parentName, Namespace: "default"}, parent)).To(Succeed())
		parent.Spec.Regions = []string{"europe/monaco", "africa/morocco"}
		Expect(k8sClient.Update(ctx, parent)).To(Succeed())
		parent.Status.Regions = []nominatimv1alpha1.RegionStatus{}
		Expect(k8sClient.Status().Update(ctx, parent)).To(Succeed())

		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationAddRegions,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
				Regions:      []string{"africa/morocco"},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, op)).To(Succeed())
		Expect(op.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhaseFailed))
		Expect(op.Status.Message).To(ContainSubstring("BootstrapIncomplete"))

		job := &batchv1.Job{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("fails CatchUp with BootstrapIncomplete when regions-mode parent has no Bootstrap history (T6d)", func() {
		parent := &nominatimv1alpha1.Nominatim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: parentName, Namespace: "default"}, parent)).To(Succeed())
		parent.Spec.Regions = []string{"europe/monaco"}
		Expect(k8sClient.Update(ctx, parent)).To(Succeed())
		parent.Status.Regions = []nominatimv1alpha1.RegionStatus{}
		Expect(k8sClient.Status().Update(ctx, parent)).To(Succeed())

		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationCatchUp,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, op)).To(Succeed())
		Expect(op.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhaseFailed))
		Expect(op.Status.Message).To(ContainSubstring("BootstrapIncomplete"))

		job := &batchv1.Job{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("creates a Job for AddRegions when Bootstrap already imported regions (T7)", func() {
		parent := &nominatimv1alpha1.Nominatim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: parentName, Namespace: "default"}, parent)).To(Succeed())
		parent.Spec.Regions = []string{"europe/monaco", "africa/morocco"}
		Expect(k8sClient.Update(ctx, parent)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: parentName, Namespace: "default"}, parent)).To(Succeed())
		parent.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: "pg-secret"}
		parent.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}}
		Expect(k8sClient.Status().Update(ctx, parent)).To(Succeed())

		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationAddRegions,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
				Regions:      []string{"africa/morocco"},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)).To(Succeed())
		Expect(envValue(job.Spec.Template.Spec.Containers[0].Env, "NOMINATIM_REGIONS")).To(Equal("africa/morocco"))

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, op)).To(Succeed())
		Expect(op.Status.Phase).NotTo(Equal(nominatimv1alpha1.NominatimOperationPhaseFailed))
	})

	It("creates a Job for Update falling back to parent.spec.regions (T8)", func() {
		parent := &nominatimv1alpha1.Nominatim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: parentName, Namespace: "default"}, parent)).To(Succeed())
		parent.Spec.Regions = []string{"europe/monaco"}
		Expect(k8sClient.Update(ctx, parent)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: parentName, Namespace: "default"}, parent)).To(Succeed())
		parent.Status.Database = nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: "pg-secret"}
		parent.Status.Regions = []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}}
		Expect(k8sClient.Status().Update(ctx, parent)).To(Succeed())

		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationUpdate,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)).To(Succeed())
		Expect(envValue(job.Spec.Template.Spec.Containers[0].Env, "NOMINATIM_REGIONS")).To(Equal("europe/monaco"))
	})

	It("does not require Bootstrap history for AddRegions on a PBF-only parent (T9)", func() {
		parent := &nominatimv1alpha1.Nominatim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: parentName, Namespace: "default"}, parent)).To(Succeed())
		parent.Spec.Regions = nil
		Expect(k8sClient.Update(ctx, parent)).To(Succeed())
		parent.Status.Regions = []nominatimv1alpha1.RegionStatus{}
		Expect(k8sClient.Status().Update(ctx, parent)).To(Succeed())

		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationAddRegions,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
				Regions:      []string{"europe/monaco"},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, op)).To(Succeed())
		Expect(op.Status.Message).NotTo(ContainSubstring("BootstrapIncomplete"))

		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)).To(Succeed())
	})

	It("passes the Bootstrap-done gate via a Succeeded Bootstrap peer (T9b)", func() {
		parent := &nominatimv1alpha1.Nominatim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: parentName, Namespace: "default"}, parent)).To(Succeed())
		parent.Spec.Regions = []string{"europe/monaco"}
		Expect(k8sClient.Update(ctx, parent)).To(Succeed())

		bootstrapName := opName + "-bootstrap"
		bootstrap := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: bootstrapName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationBootstrap,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		}
		Expect(k8sClient.Create(ctx, bootstrap)).To(Succeed())
		bootstrap.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseSucceeded
		Expect(k8sClient.Status().Update(ctx, bootstrap)).To(Succeed())
		DeferCleanup(func() { cleanupOperation(ctx, bootstrapName) })

		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationUpdate,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, op)).To(Succeed())
		Expect(op.Status.Message).NotTo(ContainSubstring("BootstrapIncomplete"))

		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)).To(Succeed())
	})

	It("is a no-op when the Operation is already deleted", func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "does-not-exist", Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("creates staging PVC and Job for Refresh without requiring regions", func() {
		op := &nominatimv1alpha1.NominatimOperation{
			ObjectMeta: metav1.ObjectMeta{Name: opName, Namespace: "default"},
			Spec: nominatimv1alpha1.NominatimOperationSpec{
				Type:         nominatimv1alpha1.NominatimOperationRefresh,
				NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
			},
		}
		Expect(k8sClient.Create(ctx, op)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: opName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())

		pvc := &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName + "-staging", Namespace: "default"}, pvc)).To(Succeed())
		Expect(metav1.IsControlledBy(pvc, op)).To(BeTrue())

		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, job)).To(Succeed())
		Expect(metav1.IsControlledBy(job, op)).To(BeTrue())
		c := job.Spec.Template.Spec.Containers[0]
		Expect(envValue(c.Env, "OPERATION_TYPE")).To(Equal("Refresh"))
		Expect(findEnvVar(c.Env, "NOMINATIM_REGIONS")).To(BeNil())
		Expect(findEnvVar(c.Env, "PBF_URL")).To(BeNil())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: opName, Namespace: "default"}, op)).To(Succeed())
		Expect(op.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhasePending))
		Expect(op.Status.JobRef).NotTo(BeNil())
	})

	It("creates staging PVC and Job for Migrate and Freeze without requiring regions", func() {
		for _, typ := range []nominatimv1alpha1.NominatimOperationType{
			nominatimv1alpha1.NominatimOperationMigrate,
			nominatimv1alpha1.NominatimOperationFreeze,
		} {
			name := opName + "-" + strings.ToLower(string(typ))
			op := &nominatimv1alpha1.NominatimOperation{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: nominatimv1alpha1.NominatimOperationSpec{
					Type:         typ,
					NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: parentName},
				},
			}
			Expect(k8sClient.Create(ctx, op)).To(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: name, Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-staging", Namespace: "default"}, pvc)).To(Succeed())
			Expect(metav1.IsControlledBy(pvc, op)).To(BeTrue())

			job := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, job)).To(Succeed())
			Expect(metav1.IsControlledBy(job, op)).To(BeTrue())
			c := job.Spec.Template.Spec.Containers[0]
			Expect(envValue(c.Env, "OPERATION_TYPE")).To(Equal(string(typ)))
			Expect(findEnvVar(c.Env, "NOMINATIM_REGIONS")).To(BeNil())
			Expect(findEnvVar(c.Env, "PBF_URL")).To(BeNil())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, op)).To(Succeed())
			Expect(op.Status.Phase).To(Equal(nominatimv1alpha1.NominatimOperationPhasePending))
			Expect(op.Status.JobRef).NotTo(BeNil())

			// Both types are write-heavy; finish this Op before starting the next peer.
			op.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseSucceeded
			Expect(k8sClient.Status().Update(ctx, op)).To(Succeed())
		}
	})

	It("registers with the manager", func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: k8sClient.Scheme(),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect((&NominatimOperationReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr)).To(Succeed())
	})
})

var uniqueCounter int

func uniqueSuffix() string {
	uniqueCounter++
	return fmt.Sprintf("%d", uniqueCounter)
}

func cleanupOperation(ctx context.Context, name string) {
	op := &nominatimv1alpha1.NominatimOperation{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, op)
	if errors.IsNotFound(err) {
		return
	}
	Expect(err).NotTo(HaveOccurred())

	job := &batchv1.Job{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, job); err == nil {
		_ = k8sClient.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name + "-staging", Namespace: "default"}, pvc); err == nil {
		_ = k8sClient.Delete(ctx, pvc)
	}
	controllerutil.RemoveFinalizer(op, nominatimv1alpha1.NominatimOperationFinalizer)
	_ = k8sClient.Update(ctx, op)
	_ = k8sClient.Delete(ctx, op)
	Eventually(func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, &nominatimv1alpha1.NominatimOperation{})
		return errors.IsNotFound(err)
	}).Should(BeTrue())
}

func cleanupNominatim(ctx context.Context, name string) {
	nom := &nominatimv1alpha1.Nominatim{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, nom)
	if errors.IsNotFound(err) {
		return
	}
	Expect(err).NotTo(HaveOccurred())
	controllerutil.RemoveFinalizer(nom, nominatimv1alpha1.NominatimFinalizer)
	_ = k8sClient.Update(ctx, nom)
	_ = k8sClient.Delete(ctx, nom)
	Eventually(func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, &nominatimv1alpha1.Nominatim{})
		return errors.IsNotFound(err)
	}).Should(BeTrue())
}
