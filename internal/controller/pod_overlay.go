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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
)

// operatorReservedEnvExact are env keys the operator always reseals after a podSpec overlay.
// Only operator-owned wiring is listed — do NOT blanket-reserve NOMINATIM_* (user/typed
// Nominatim settings like IMPORT_STYLE / TOKENIZER must remain settable via spec.nominatim
// or podSpec). Connection credentials stay exact-match only (not all PG*).
var operatorReservedEnvExact = map[string]struct{}{
	"OPERATION_TYPE":             {},
	"PROJECT_DIR":                {},
	"IMPORT_STAGING":             {},
	"PBF_URL":                    {},
	"NOMINATIM_DATABASE_DSN":     {},
	"NOMINATIM_FLATNODE_FILE":    {},
	"NOMINATIM_REIMPORT_CONFIRM": {},
	"NOMINATIM_REGIONS":          {},
	"PGHOST":                     {},
	"PGPORT":                     {},
	"PGDATABASE":                 {},
	"PGUSER":                     {},
	"PGPASSWORD":                 {},
}

func isReservedEnvName(name string) bool {
	_, ok := operatorReservedEnvExact[name]
	return ok
}

// decodePodSpecOverlay decodes a RawExtension into a PodSpec. Nil/empty yields nil overlay.
func decodePodSpecOverlay(raw *runtime.RawExtension) (*corev1.PodSpec, error) {
	if raw == nil || len(raw.Raw) == 0 {
		return nil, nil
	}
	var spec corev1.PodSpec
	if err := json.Unmarshal(raw.Raw, &spec); err != nil {
		return nil, fmt.Errorf("decode podSpec overlay: %w", err)
	}
	return &spec, nil
}

// mergePodSpecOverlay merges a user podSpec overlay into an operator-built base PodSpec.
// managedContainer is the operator-owned container name (api/ui/worker). After merge, that
// container's image, pullPolicy, and http port are resealed; reserved env keys from base win.
func mergePodSpecOverlay(
	base corev1.PodSpec,
	overlayRaw *runtime.RawExtension,
	managedContainer string,
	image string,
	pullPolicy corev1.PullPolicy,
) (corev1.PodSpec, error) {
	overlay, err := decodePodSpecOverlay(overlayRaw)
	if err != nil {
		return base, err
	}
	if overlay == nil {
		return base, nil
	}

	out, err := strategicMergePodSpec(base, *overlay)
	if err != nil {
		return base, err
	}

	out.InitContainers = mergeContainersByName(base.InitContainers, overlay.InitContainers, "")
	out.Containers = mergeWorkloadContainers(base.Containers, overlay.Containers, managedContainer)
	out.Volumes = unionVolumes(base.Volumes, overlay.Volumes)

	sealManagedContainer(&out, managedContainer, image, pullPolicy, base.Containers)
	hardenPodSpec(&out, base)
	return out, nil
}

func strategicMergePodSpec(base, overlay corev1.PodSpec) (corev1.PodSpec, error) {
	// Strip container/volume slices — handled with explicit merge rules below.
	baseCopy := base.DeepCopy()
	overlayCopy := overlay.DeepCopy()
	baseCopy.Containers = nil
	baseCopy.InitContainers = nil
	baseCopy.Volumes = nil
	overlayCopy.Containers = nil
	overlayCopy.InitContainers = nil
	overlayCopy.Volumes = nil

	baseBytes, err := json.Marshal(baseCopy)
	if err != nil {
		return base, fmt.Errorf("marshal base podSpec: %w", err)
	}
	overlayBytes, err := json.Marshal(overlayCopy)
	if err != nil {
		return base, fmt.Errorf("marshal overlay podSpec: %w", err)
	}
	patched, err := strategicpatch.StrategicMergePatch(baseBytes, overlayBytes, &corev1.PodSpec{})
	if err != nil {
		return base, fmt.Errorf("strategic merge podSpec: %w", err)
	}
	var out corev1.PodSpec
	if err := json.Unmarshal(patched, &out); err != nil {
		return base, fmt.Errorf("unmarshal merged podSpec: %w", err)
	}
	return out, nil
}

func mergeWorkloadContainers(base, overlay []corev1.Container, managedName string) []corev1.Container {
	baseByName := indexContainers(base)
	overlayByName := indexContainers(overlay)

	out := make([]corev1.Container, 0, len(base)+len(overlay))
	// Preserve base order for managed + any existing sidecars, then append new overlay sidecars.
	seen := map[string]struct{}{}
	for _, c := range base {
		seen[c.Name] = struct{}{}
		if oc, ok := overlayByName[c.Name]; ok {
			out = append(out, mergeOneContainer(c, oc, c.Name == managedName))
			continue
		}
		out = append(out, *c.DeepCopy())
	}
	for _, c := range overlay {
		if _, ok := seen[c.Name]; ok {
			continue
		}
		out = append(out, *c.DeepCopy())
	}
	// Ensure managed container exists even if base was empty (should not happen).
	if _, ok := indexContainers(out)[managedName]; !ok {
		if oc, ok := overlayByName[managedName]; ok {
			out = append([]corev1.Container{*oc.DeepCopy()}, out...)
		} else if bc, ok := baseByName[managedName]; ok {
			out = append([]corev1.Container{*bc.DeepCopy()}, out...)
		}
	}
	return out
}

func mergeContainersByName(base, overlay []corev1.Container, _ string) []corev1.Container {
	if len(overlay) == 0 {
		return append([]corev1.Container(nil), base...)
	}
	out := mergeWorkloadContainers(base, overlay, "")
	return out
}

func mergeOneContainer(base, overlay corev1.Container, managed bool) corev1.Container {
	// Overlay fields win via strategic merge of the container object; env/mounts handled after.
	baseBytes, _ := json.Marshal(base)
	overlayBytes, _ := json.Marshal(overlay)
	patched, err := strategicpatch.StrategicMergePatch(baseBytes, overlayBytes, &corev1.Container{})
	merged := *base.DeepCopy()
	if err == nil {
		_ = json.Unmarshal(patched, &merged)
	} else {
		merged = *overlay.DeepCopy()
		merged.Name = base.Name
	}
	if managed {
		merged.Env = mergeEnvReservedWin(base.Env, overlay.Env)
		merged.VolumeMounts = unionVolumeMounts(base.VolumeMounts, overlay.VolumeMounts)
	}
	return merged
}

func mergeEnvReservedWin(base, overlay []corev1.EnvVar) []corev1.EnvVar {
	baseReserved := map[string]corev1.EnvVar{}
	for _, e := range base {
		if isReservedEnvName(e.Name) {
			baseReserved[e.Name] = e
		}
	}
	out := make([]corev1.EnvVar, 0, len(overlay)+len(baseReserved))
	seen := map[string]struct{}{}
	for _, e := range overlay {
		if isReservedEnvName(e.Name) {
			continue
		}
		out = append(out, e)
		seen[e.Name] = struct{}{}
	}
	// Operator reserved from base always present (and win).
	for _, e := range base {
		if !isReservedEnvName(e.Name) {
			continue
		}
		out = append(out, e)
		seen[e.Name] = struct{}{}
	}
	// Keep non-reserved base env not overridden by overlay.
	for _, e := range base {
		if isReservedEnvName(e.Name) {
			continue
		}
		if _, ok := seen[e.Name]; ok {
			continue
		}
		out = append(out, e)
	}
	return out
}

func unionVolumeMounts(base, overlay []corev1.VolumeMount) []corev1.VolumeMount {
	byName := map[string]corev1.VolumeMount{}
	byPath := map[string]string{} // mountPath -> volume name owning it
	order := make([]string, 0, len(base)+len(overlay))
	for _, m := range base {
		byName[m.Name] = m
		byPath[m.MountPath] = m.Name
		order = append(order, m.Name)
	}
	for _, m := range overlay {
		if _, ok := byName[m.Name]; ok {
			// Operator / base wins on name conflict.
			continue
		}
		if _, ok := byPath[m.MountPath]; ok {
			// Operator mount paths (/nominatim, etc.) must not be shadowed.
			continue
		}
		byName[m.Name] = m
		byPath[m.MountPath] = m.Name
		order = append(order, m.Name)
	}
	out := make([]corev1.VolumeMount, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out
}

func unionVolumes(base, overlay []corev1.Volume) []corev1.Volume {
	byName := map[string]corev1.Volume{}
	order := make([]string, 0, len(base)+len(overlay))
	for _, v := range base {
		byName[v.Name] = v
		order = append(order, v.Name)
	}
	for _, v := range overlay {
		if _, ok := byName[v.Name]; ok {
			continue
		}
		if v.HostPath != nil {
			// Deny hostPath volumes from overlays (confused-deputy / node compromise).
			continue
		}
		byName[v.Name] = v
		order = append(order, v.Name)
	}
	out := make([]corev1.Volume, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out
}

func indexContainers(in []corev1.Container) map[string]corev1.Container {
	out := make(map[string]corev1.Container, len(in))
	for _, c := range in {
		out[c.Name] = c
	}
	return out
}

func sealManagedContainer(spec *corev1.PodSpec, managedName, image string, pullPolicy corev1.PullPolicy, baseContainers []corev1.Container) {
	baseByName := indexContainers(baseContainers)
	for i := range spec.Containers {
		if spec.Containers[i].Name != managedName {
			continue
		}
		spec.Containers[i].Image = image
		spec.Containers[i].ImagePullPolicy = pullPolicy
		if base, ok := baseByName[managedName]; ok {
			// Reseal ports / entrypoint / envFrom / securityContext from the operator base.
			spec.Containers[i].Ports = append([]corev1.ContainerPort(nil), base.Ports...)
			spec.Containers[i].Command = append([]string(nil), base.Command...)
			spec.Containers[i].Args = append([]string(nil), base.Args...)
			spec.Containers[i].EnvFrom = nil
			if base.SecurityContext != nil {
				spec.Containers[i].SecurityContext = base.SecurityContext.DeepCopy()
			} else {
				spec.Containers[i].SecurityContext = nil
			}
			spec.Containers[i].Env = mergeEnvReservedWin(base.Env, spec.Containers[i].Env)
			spec.Containers[i].VolumeMounts = unionVolumeMounts(base.VolumeMounts, spec.Containers[i].VolumeMounts)
		}
		return
	}
}

// hardenPodSpec strips host namespaces / hostPath / privileged capabilities introduced by overlays.
// Scheduling knobs (affinity, nodeSelector, tolerations, topologySpread) are left intact.
func hardenPodSpec(spec *corev1.PodSpec, base corev1.PodSpec) {
	spec.HostNetwork = base.HostNetwork
	spec.HostPID = base.HostPID
	spec.HostIPC = base.HostIPC
	spec.ShareProcessNamespace = base.ShareProcessNamespace
	spec.ServiceAccountName = base.ServiceAccountName
	spec.AutomountServiceAccountToken = base.AutomountServiceAccountToken
	if base.SecurityContext != nil {
		spec.SecurityContext = base.SecurityContext.DeepCopy()
	} else {
		spec.SecurityContext = stripPrivilegedPodSC(spec.SecurityContext)
	}

	for i := range spec.Containers {
		stripPrivilegedContainerSC(spec.Containers[i].SecurityContext)
	}
	for i := range spec.InitContainers {
		stripPrivilegedContainerSC(spec.InitContainers[i].SecurityContext)
	}

	// Drop any hostPath volumes that slipped through (e.g. base had none; overlay renamed).
	cleaned := make([]corev1.Volume, 0, len(spec.Volumes))
	baseVols := map[string]corev1.Volume{}
	for _, v := range base.Volumes {
		baseVols[v.Name] = v
	}
	for _, v := range spec.Volumes {
		if _, isBase := baseVols[v.Name]; isBase {
			cleaned = append(cleaned, v)
			continue
		}
		if v.HostPath != nil {
			continue
		}
		cleaned = append(cleaned, v)
	}
	spec.Volumes = cleaned
}

func stripPrivilegedPodSC(sc *corev1.PodSecurityContext) *corev1.PodSecurityContext {
	if sc == nil {
		return nil
	}
	out := sc.DeepCopy()
	// Keep non-privilege fields (fsGroup, etc.) but never allow privileged sysctls wholesale changes
	// beyond what overlay already set — host namespaces are already resealed.
	return out
}

func stripPrivilegedContainerSC(sc *corev1.SecurityContext) {
	if sc == nil {
		return
	}
	if sc.Privileged != nil && *sc.Privileged {
		f := false
		sc.Privileged = &f
	}
	if sc.AllowPrivilegeEscalation != nil && *sc.AllowPrivilegeEscalation {
		f := false
		sc.AllowPrivilegeEscalation = &f
	}
	if sc.Capabilities != nil {
		sc.Capabilities.Add = nil
	}
}
