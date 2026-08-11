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
	"testing"

	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

func mustBuildOperationJob(t *testing.T, op *nominatimv1alpha1.NominatimOperation, parent *nominatimv1alpha1.Nominatim, stagingClaim, image string, pullPolicy corev1.PullPolicy) *batchv1.Job { //nolint:unparam // mirrors buildOperationJob signature for call-site clarity
	t.Helper()
	job, err := buildOperationJob(op, parent, stagingClaim, image, pullPolicy)
	if err != nil {
		t.Fatalf("buildOperationJob: %v", err)
	}
	return job
}

func TestIsWriteHeavyOperation(t *testing.T) {
	g := NewWithT(t)
	g.Expect(isWriteHeavyOperation(nominatimv1alpha1.NominatimOperationBootstrap)).To(BeTrue())
	g.Expect(isWriteHeavyOperation(nominatimv1alpha1.NominatimOperationAddRegions)).To(BeTrue())
	g.Expect(isWriteHeavyOperation(nominatimv1alpha1.NominatimOperationReimport)).To(BeTrue())
	g.Expect(isWriteHeavyOperation(nominatimv1alpha1.NominatimOperationUpdate)).To(BeFalse())
	g.Expect(isWriteHeavyOperation(nominatimv1alpha1.NominatimOperationCatchUp)).To(BeFalse())
	g.Expect(isWriteHeavyOperation(nominatimv1alpha1.NominatimOperationRefresh)).To(BeFalse())
}

func TestIsActiveOperationPhase(t *testing.T) {
	g := NewWithT(t)
	g.Expect(isActiveOperationPhase("")).To(BeTrue())
	g.Expect(isActiveOperationPhase(nominatimv1alpha1.NominatimOperationPhasePending)).To(BeTrue())
	g.Expect(isActiveOperationPhase(nominatimv1alpha1.NominatimOperationPhaseRunning)).To(BeTrue())
	g.Expect(isActiveOperationPhase(nominatimv1alpha1.NominatimOperationPhaseSucceeded)).To(BeFalse())
	g.Expect(isActiveOperationPhase(nominatimv1alpha1.NominatimOperationPhaseFailed)).To(BeFalse())
}

func TestFindConflictingOperation(t *testing.T) {
	g := NewWithT(t)

	bootstrap := nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-1"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseRunning},
	}
	update := nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "update-1"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhasePending},
	}
	otherNom := nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "other"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "other-nom"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseRunning},
	}
	done := nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "done"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}

	// Write-heavy vs write-heavy.
	conflict := findConflictingOperation(&nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-2"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationReimport,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
		},
	}, []nominatimv1alpha1.NominatimOperation{bootstrap, otherNom, done})
	g.Expect(conflict).NotTo(BeNil())
	g.Expect(conflict.Name).To(Equal("bootstrap-1"))

	// Update conflicts with active write-heavy.
	conflict = findConflictingOperation(&update, []nominatimv1alpha1.NominatimOperation{bootstrap})
	g.Expect(conflict).NotTo(BeNil())
	g.Expect(conflict.Name).To(Equal("bootstrap-1"))

	// Write-heavy conflicts with active Update.
	conflict = findConflictingOperation(&bootstrap, []nominatimv1alpha1.NominatimOperation{update})
	g.Expect(conflict).NotTo(BeNil())
	g.Expect(conflict.Name).To(Equal("update-1"))

	// Two Updates with no write-heavy are allowed.
	update2 := update
	update2.Name = "update-2"
	conflict = findConflictingOperation(&update2, []nominatimv1alpha1.NominatimOperation{update, done, otherNom})
	g.Expect(conflict).To(BeNil())
}

func TestResolveStagingSpec(t *testing.T) {
	g := NewWithT(t)
	sc := "fast"

	opOnly := &nominatimv1alpha1.NominatimOperation{
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Staging: &nominatimv1alpha1.StagingSpec{Size: "100Gi", StorageClassName: &sc},
		},
	}
	parent := &nominatimv1alpha1.Nominatim{
		Spec: nominatimv1alpha1.NominatimSpec{
			Staging: &nominatimv1alpha1.StagingSpec{Size: "20Gi"},
		},
	}
	resolved := resolveStagingSpec(opOnly, parent)
	g.Expect(resolved.Size).To(Equal("100Gi"))
	g.Expect(resolved.StorageClassName).NotTo(BeNil())
	g.Expect(*resolved.StorageClassName).To(Equal("fast"))

	// Parent defaults when Operation.staging unset.
	resolved = resolveStagingSpec(&nominatimv1alpha1.NominatimOperation{}, parent)
	g.Expect(resolved.Size).To(Equal("20Gi"))

	// Built-in default when neither sets size.
	resolved = resolveStagingSpec(&nominatimv1alpha1.NominatimOperation{}, &nominatimv1alpha1.Nominatim{})
	g.Expect(resolved.Size).To(Equal(defaultStagingSize))
}

func TestVolumeClaimName(t *testing.T) {
	g := NewWithT(t)
	g.Expect(volumeClaimName(nominatimv1alpha1.VolumeSource{ClaimName: "proj"}, "fallback")).To(Equal("proj"))
	g.Expect(volumeClaimName(nominatimv1alpha1.VolumeSource{
		VolumeClaimTemplate: &nominatimv1alpha1.VolumeClaimTemplate{
			Metadata: metav1.ObjectMeta{Name: "from-template"},
		},
	}, "fallback")).To(Equal("from-template"))
	g.Expect(volumeClaimName(nominatimv1alpha1.VolumeSource{}, "fallback")).To(Equal("fallback"))
}

func TestWorkerImageForOperation(t *testing.T) {
	g := NewWithT(t)
	defaultImage := resolveImage(nil, defaultWorkerRepository)
	g.Expect(workerImageForOperation(&nominatimv1alpha1.NominatimOperation{}, nil)).To(Equal(defaultImage))
	g.Expect(workerImageForOperation(&nominatimv1alpha1.NominatimOperation{
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Image: &nominatimv1alpha1.ImageSpec{Repository: "example.com/worker", Tag: "v1"},
		},
	}, nil)).To(Equal("example.com/worker:v1"))
	g.Expect(workerImageForOperation(&nominatimv1alpha1.NominatimOperation{}, &nominatimv1alpha1.Nominatim{
		Spec: nominatimv1alpha1.NominatimSpec{
			Worker: &nominatimv1alpha1.WorkerSpec{
				Image: &nominatimv1alpha1.ImageSpec{Repository: "example.com/from-parent", Tag: "e2e"},
			},
		},
	})).To(Equal("example.com/from-parent:e2e"))
	// Operation.spec.image wins over Nominatim.spec.worker.image.
	g.Expect(workerImageForOperation(&nominatimv1alpha1.NominatimOperation{
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Image: &nominatimv1alpha1.ImageSpec{Repository: "example.com/op", Tag: "v1"},
		},
	}, &nominatimv1alpha1.Nominatim{
		Spec: nominatimv1alpha1.NominatimSpec{
			Worker: &nominatimv1alpha1.WorkerSpec{
				Image: &nominatimv1alpha1.ImageSpec{Repository: "example.com/parent", Tag: "v1"},
			},
		},
	})).To(Equal("example.com/op:v1"))
}

func TestPhaseFromJob(t *testing.T) {
	g := NewWithT(t)
	g.Expect(phaseFromJob(1, 0, 0)).To(Equal(nominatimv1alpha1.NominatimOperationPhaseSucceeded))
	g.Expect(phaseFromJob(0, 1, 0)).To(Equal(nominatimv1alpha1.NominatimOperationPhaseFailed))
	g.Expect(phaseFromJob(0, 0, 1)).To(Equal(nominatimv1alpha1.NominatimOperationPhaseRunning))
	g.Expect(phaseFromJob(0, 0, 0)).To(Equal(nominatimv1alpha1.NominatimOperationPhasePending))
}

func TestBuildStagingPVC(t *testing.T) {
	g := NewWithT(t)
	sc := "ssd"
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "op1", Namespace: "default"},
	}
	pvc := buildStagingPVC(op, nominatimv1alpha1.StagingSpec{Size: "50Gi", StorageClassName: &sc})
	g.Expect(pvc.Name).To(Equal("op1-staging"))
	g.Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteOnce))
	g.Expect(pvc.Spec.Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("50Gi")))
	g.Expect(pvc.Spec.StorageClassName).NotTo(BeNil())
	g.Expect(*pvc.Spec.StorageClassName).To(Equal("ssd"))
	// Must be a real PVC request — never emptyDir.
	g.Expect(pvc.Spec.VolumeMode).To(BeNil())
}

func TestBuildOperationJob(t *testing.T) {
	g := NewWithT(t)
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-1", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "mynom"},
			Regions:      []string{"europe/monaco"},
		},
	}
	parent := &nominatimv1alpha1.Nominatim{
		ObjectMeta: metav1.ObjectMeta{Name: "mynom", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimSpec{
			Project: nominatimv1alpha1.ProjectSpec{
				Volume: nominatimv1alpha1.VolumeSource{ClaimName: "project-pvc"},
			},
			Flatnode: &nominatimv1alpha1.FlatnodeSpec{
				Volume: nominatimv1alpha1.VolumeSource{ClaimName: "flatnode-pvc"},
			},
		},
	}
	job := mustBuildOperationJob(t, op, parent, "staging-pvc", resolveImage(nil, defaultWorkerRepository), "")
	g.Expect(job.Name).To(Equal("boot-1"))
	g.Expect(job.Spec.Template.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyNever))
	g.Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
	c := job.Spec.Template.Spec.Containers[0]
	g.Expect(c.Image).To(Equal(resolveImage(nil, defaultWorkerRepository)))
	g.Expect(envValue(c.Env, "OPERATION_TYPE")).To(Equal("Bootstrap"))
	g.Expect(envValue(c.Env, "NOMINATIM_REGIONS")).To(Equal("europe/monaco"))
	g.Expect(envValue(c.Env, "NOMINATIM_FLATNODE_FILE")).To(Equal(flatnodeFilePath))
	g.Expect(job.Spec.Template.Spec.Volumes).To(HaveLen(3))
	g.Expect(c.VolumeMounts).To(HaveLen(3))

	// Fall back to parent regions when Operation.regions is empty.
	opNoRegions := op.DeepCopy()
	opNoRegions.Spec.Regions = nil
	parent.Spec.Regions = []string{"africa/morocco"}
	job = mustBuildOperationJob(t, opNoRegions, parent, "staging-pvc", resolveImage(nil, defaultWorkerRepository), "")
	g.Expect(envValue(job.Spec.Template.Spec.Containers[0].Env, "NOMINATIM_REGIONS")).To(Equal("africa/morocco"))
}

func TestBuildOperationJob_ReimportSetsConfirmEnv(t *testing.T) {
	g := NewWithT(t)
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "reimport-1", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationReimport,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "mynom"},
			Regions:      []string{"europe/monaco"},
		},
	}
	parent := &nominatimv1alpha1.Nominatim{
		ObjectMeta: metav1.ObjectMeta{Name: "mynom", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimSpec{
			Project: nominatimv1alpha1.ProjectSpec{
				Volume: nominatimv1alpha1.VolumeSource{ClaimName: "project-pvc"},
			},
		},
		Status: nominatimv1alpha1.NominatimStatus{
			Database: nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: "pg-secret"},
		},
	}
	job := mustBuildOperationJob(t, op, parent, "staging-pvc", resolveImage(nil, defaultWorkerRepository), "")
	c := job.Spec.Template.Spec.Containers[0]
	g.Expect(envValue(c.Env, "NOMINATIM_REIMPORT_CONFIRM")).To(Equal("1"))
	g.Expect(envValue(c.Env, "OPERATION_TYPE")).To(Equal("Reimport"))
}

func TestBuildOperationJob_IncludesDBEnvAndPBFURLForBootstrap(t *testing.T) {
	g := NewWithT(t)
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-1", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "mynom"},
			Regions:      []string{"europe/monaco", "africa/morocco"},
		},
	}
	parent := &nominatimv1alpha1.Nominatim{
		ObjectMeta: metav1.ObjectMeta{Name: "mynom", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimSpec{
			Project: nominatimv1alpha1.ProjectSpec{
				Volume: nominatimv1alpha1.VolumeSource{ClaimName: "project-pvc"},
			},
		},
		Status: nominatimv1alpha1.NominatimStatus{
			Database: nominatimv1alpha1.DatabaseStatus{ConnectionSecretName: "pg-secret"},
		},
	}
	job := mustBuildOperationJob(t, op, parent, "staging-pvc", resolveImage(nil, defaultWorkerRepository), "")
	c := job.Spec.Template.Spec.Containers[0]

	// Worker downloads every NOMINATIM_REGIONS extract and runs nominatim import with
	// multiple --osm-file flags (PBF_URL remains regions[0] for the first URL).
	g.Expect(envValue(c.Env, "NOMINATIM_REGIONS")).To(Equal("europe/monaco,africa/morocco"))
	g.Expect(envValue(c.Env, "PBF_URL")).To(Equal("https://download.geofabrik.de/europe/monaco-latest.osm.pbf"))

	dsn := findEnvVar(c.Env, "NOMINATIM_DATABASE_DSN")
	g.Expect(dsn).NotTo(BeNil())
	g.Expect(dsn.ValueFrom).NotTo(BeNil())
	g.Expect(dsn.ValueFrom.SecretKeyRef).NotTo(BeNil())
	g.Expect(dsn.ValueFrom.SecretKeyRef.Name).To(Equal("pg-secret"))

	for _, name := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD"} {
		e := findEnvVar(c.Env, name)
		g.Expect(e).NotTo(BeNil(), "missing env %s", name)
		g.Expect(e.ValueFrom).NotTo(BeNil())
		g.Expect(e.ValueFrom.SecretKeyRef.Name).To(Equal("pg-secret"))
	}
}

func TestBuildOperationJob_NoPBFURLForNonWriteHeavyOperation(t *testing.T) {
	g := NewWithT(t)
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "update-1", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationUpdate,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "mynom"},
			Regions:      []string{"europe/monaco"},
		},
	}
	parent := &nominatimv1alpha1.Nominatim{
		ObjectMeta: metav1.ObjectMeta{Name: "mynom", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimSpec{
			Project: nominatimv1alpha1.ProjectSpec{
				Volume: nominatimv1alpha1.VolumeSource{ClaimName: "project-pvc"},
			},
		},
	}
	job := mustBuildOperationJob(t, op, parent, "staging-pvc", resolveImage(nil, defaultWorkerRepository), "")
	c := job.Spec.Template.Spec.Containers[0]
	g.Expect(findEnvVar(c.Env, "PBF_URL")).To(BeNil())
}

func TestBuildOperationJob_NoDBEnvWhenConnectionSecretNameEmpty(t *testing.T) {
	g := NewWithT(t)
	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "boot-nosecret", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "mynom"},
		},
	}
	parent := &nominatimv1alpha1.Nominatim{
		ObjectMeta: metav1.ObjectMeta{Name: "mynom", Namespace: "ns"},
		Spec: nominatimv1alpha1.NominatimSpec{
			Project: nominatimv1alpha1.ProjectSpec{
				Volume: nominatimv1alpha1.VolumeSource{ClaimName: "project-pvc"},
			},
		},
	}
	job := mustBuildOperationJob(t, op, parent, "staging-pvc", resolveImage(nil, defaultWorkerRepository), "")
	c := job.Spec.Template.Spec.Containers[0]
	g.Expect(findEnvVar(c.Env, "NOMINATIM_DATABASE_DSN")).To(BeNil())
}

func TestPBFURLForRegion(t *testing.T) {
	g := NewWithT(t)
	g.Expect(pbfURLForRegion("europe/monaco")).To(Equal("https://download.geofabrik.de/europe/monaco-latest.osm.pbf"))
}

func TestIsTerminalOperationPhase(t *testing.T) {
	g := NewWithT(t)
	g.Expect(isTerminalOperationPhase(nominatimv1alpha1.NominatimOperationPhaseSucceeded)).To(BeTrue())
	g.Expect(isTerminalOperationPhase(nominatimv1alpha1.NominatimOperationPhaseFailed)).To(BeTrue())
	g.Expect(isTerminalOperationPhase(nominatimv1alpha1.NominatimOperationPhaseRunning)).To(BeFalse())
}

func TestEffectiveRegions(t *testing.T) {
	g := NewWithT(t)

	// Operation.spec.regions wins when set.
	op := &nominatimv1alpha1.NominatimOperation{
		Spec: nominatimv1alpha1.NominatimOperationSpec{Regions: []string{"europe/monaco"}},
	}
	parent := &nominatimv1alpha1.Nominatim{
		Spec: nominatimv1alpha1.NominatimSpec{Regions: []string{"africa/morocco"}},
	}
	g.Expect(effectiveRegions(op, parent)).To(Equal([]string{"europe/monaco"}))

	// Falls back to parent.spec.regions when Operation.spec.regions is empty.
	g.Expect(effectiveRegions(&nominatimv1alpha1.NominatimOperation{}, parent)).To(Equal([]string{"africa/morocco"}))

	// Empty on both sides.
	g.Expect(effectiveRegions(&nominatimv1alpha1.NominatimOperation{}, &nominatimv1alpha1.Nominatim{})).To(BeEmpty())
}

func TestBootstrapComplete(t *testing.T) {
	g := NewWithT(t)

	// status.regions already populated → complete regardless of peers.
	parentWithStatus := &nominatimv1alpha1.Nominatim{
		ObjectMeta: metav1.ObjectMeta{Name: "nom"},
		Status: nominatimv1alpha1.NominatimStatus{
			Regions: []nominatimv1alpha1.RegionStatus{{Name: "europe/monaco"}},
		},
	}
	g.Expect(bootstrapComplete(parentWithStatus, nil)).To(BeTrue())

	// Empty status, no peers → incomplete.
	parentEmpty := &nominatimv1alpha1.Nominatim{ObjectMeta: metav1.ObjectMeta{Name: "nom"}}
	g.Expect(bootstrapComplete(parentEmpty, nil)).To(BeFalse())

	// Empty status, Succeeded Bootstrap peer targeting this Nominatim → complete.
	succeededBootstrap := nominatimv1alpha1.NominatimOperation{
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: "nom"},
		},
		Status: nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseSucceeded},
	}
	g.Expect(bootstrapComplete(parentEmpty, []nominatimv1alpha1.NominatimOperation{succeededBootstrap})).To(BeTrue())

	// Running (not Succeeded) Bootstrap peer → still incomplete.
	runningBootstrap := succeededBootstrap
	runningBootstrap.Status.Phase = nominatimv1alpha1.NominatimOperationPhaseRunning
	g.Expect(bootstrapComplete(parentEmpty, []nominatimv1alpha1.NominatimOperation{runningBootstrap})).To(BeFalse())

	// Succeeded Bootstrap peer targeting a different Nominatim → still incomplete.
	otherBootstrap := succeededBootstrap
	otherBootstrap.Spec.NominatimRef.Name = "other-nom"
	g.Expect(bootstrapComplete(parentEmpty, []nominatimv1alpha1.NominatimOperation{otherBootstrap})).To(BeFalse())

	// Succeeded non-Bootstrap peer → still incomplete.
	succeededUpdate := succeededBootstrap
	succeededUpdate.Spec.Type = nominatimv1alpha1.NominatimOperationUpdate
	g.Expect(bootstrapComplete(parentEmpty, []nominatimv1alpha1.NominatimOperation{succeededUpdate})).To(BeFalse())
}

func TestRequiresRegionGate(t *testing.T) {
	g := NewWithT(t)
	g.Expect(requiresRegionGate(nominatimv1alpha1.NominatimOperationAddRegions)).To(BeTrue())
	g.Expect(requiresRegionGate(nominatimv1alpha1.NominatimOperationUpdate)).To(BeTrue())
	g.Expect(requiresRegionGate(nominatimv1alpha1.NominatimOperationCatchUp)).To(BeTrue())
	g.Expect(requiresRegionGate(nominatimv1alpha1.NominatimOperationBootstrap)).To(BeFalse())
	g.Expect(requiresRegionGate(nominatimv1alpha1.NominatimOperationReimport)).To(BeFalse())
	g.Expect(requiresRegionGate(nominatimv1alpha1.NominatimOperationRefresh)).To(BeFalse())
}

func TestConflictMessage(t *testing.T) {
	g := NewWithT(t)
	peer := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "peer"},
		Spec:       nominatimv1alpha1.NominatimOperationSpec{Type: nominatimv1alpha1.NominatimOperationBootstrap},
		Status:     nominatimv1alpha1.NominatimOperationStatus{Phase: nominatimv1alpha1.NominatimOperationPhaseRunning},
	}
	g.Expect(conflictMessage(peer)).To(ContainSubstring("Conflict"))
	g.Expect(conflictMessage(peer)).To(ContainSubstring("peer"))
}

func envValue(env []corev1.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

// findEnvVar returns a pointer to the named EnvVar (including ValueFrom-sourced vars,
// unlike envValue which only reads the literal Value field), or nil if absent.
func findEnvVar(env []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range env {
		if env[i].Name == name {
			return &env[i]
		}
	}
	return nil
}

func TestIsOperationTypeImplemented(t *testing.T) {
	g := NewWithT(t)
	g.Expect(isOperationTypeImplemented(nominatimv1alpha1.NominatimOperationBootstrap)).To(BeTrue())
	g.Expect(isOperationTypeImplemented(nominatimv1alpha1.NominatimOperationAddRegions)).To(BeTrue())
	g.Expect(isOperationTypeImplemented(nominatimv1alpha1.NominatimOperationReimport)).To(BeTrue())
	g.Expect(isOperationTypeImplemented(nominatimv1alpha1.NominatimOperationUpdate)).To(BeTrue())
	g.Expect(isOperationTypeImplemented(nominatimv1alpha1.NominatimOperationCatchUp)).To(BeTrue())
	g.Expect(isOperationTypeImplemented(nominatimv1alpha1.NominatimOperationRefresh)).To(BeTrue())
	g.Expect(isOperationTypeImplemented(nominatimv1alpha1.NominatimOperationMigrate)).To(BeFalse())
	g.Expect(isOperationTypeImplemented(nominatimv1alpha1.NominatimOperationFreeze)).To(BeFalse())
	g.Expect(isOperationTypeImplemented(nominatimv1alpha1.NominatimOperationType("Nope"))).To(BeFalse())
}
