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
	"encoding/json"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

const (
	annotationSequenceObserved = "nominatim.zebernst.dev/sequence-observed"
	labelSequenceProbe         = "sequence-probe"
	sequenceReportCMKey        = "report.json"
	sequenceProbeSASuffix      = "-seq-probe"
	sequenceProbeJobSuffix     = "-seq"
)

// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// reconcileSequenceObservation runs operator-owned probe Jobs after Succeeded write
// Operations so status.regions[].sequenceState reflects Geofabrik sequence.state on
// the project PVC — without giving Nominatim worker scripts Kubernetes API access.
func (r *NominatimReconciler) reconcileSequenceObservation(ctx context.Context, nom *nominatimv1alpha1.Nominatim) error {
	if len(nom.Status.Regions) == 0 {
		return nil
	}
	ops, err := r.listOperationsForParent(ctx, nom)
	if err != nil {
		return fmt.Errorf("list operations for sequence observation: %w", err)
	}

	if err := r.ensureSequenceProbeRBAC(ctx, nom); err != nil {
		return err
	}
	if err := r.ensureSequenceReportConfigMap(ctx, nom); err != nil {
		return err
	}

	for i := range ops {
		op := &ops[i]
		if !sequenceProbeOperation(op) {
			continue
		}
		if op.Annotations[annotationSequenceObserved] == "true" {
			continue
		}
		if err := r.observeSequenceForOperation(ctx, nom, op); err != nil {
			return err
		}
	}

	return r.applySequenceReportConfigMap(ctx, nom)
}

func sequenceProbeOperation(op *nominatimv1alpha1.NominatimOperation) bool {
	if op.Status.Phase != nominatimv1alpha1.NominatimOperationPhaseSucceeded {
		return false
	}
	switch op.Spec.Type {
	case nominatimv1alpha1.NominatimOperationBootstrap,
		nominatimv1alpha1.NominatimOperationAddRegions,
		nominatimv1alpha1.NominatimOperationReimport,
		nominatimv1alpha1.NominatimOperationUpdate,
		nominatimv1alpha1.NominatimOperationCatchUp:
		return true
	default:
		return false
	}
}

func sequenceProbeSAName(nom *nominatimv1alpha1.Nominatim) string {
	return nom.Name + sequenceProbeSASuffix
}

func sequenceReportConfigMapName(nom *nominatimv1alpha1.Nominatim) string {
	return nom.Name + "-sequence"
}

func sequenceProbeJobName(op *nominatimv1alpha1.NominatimOperation) string {
	base := op.Name + sequenceProbeJobSuffix
	if len(base) <= 63 {
		return base
	}
	return base[:63]
}

func (r *NominatimReconciler) ensureSequenceProbeRBAC(ctx context.Context, nom *nominatimv1alpha1.Nominatim) error {
	saName := sequenceProbeSAName(nom)
	key := types.NamespacedName{Name: saName, Namespace: nom.Namespace}

	sa := &corev1.ServiceAccount{}
	if err := r.Get(ctx, key, sa); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get sequence probe ServiceAccount: %w", err)
		}
		sa = &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: nom.Namespace}}
		if err := controllerutil.SetControllerReference(nom, sa, r.Scheme); err != nil {
			return fmt.Errorf("ownerref sequence probe ServiceAccount: %w", err)
		}
		if err := r.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create sequence probe ServiceAccount: %w", err)
		}
	}

	desiredRules := []rbacv1.PolicyRule{{
		APIGroups: []string{""},
		Resources: []string{"configmaps"},
		Verbs:     []string{"get", "create", "update", "patch"},
	}}
	role := &rbacv1.Role{}
	if err := r.Get(ctx, key, role); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get sequence probe Role: %w", err)
		}
		role = &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: nom.Namespace},
			Rules:      desiredRules,
		}
		if err := controllerutil.SetControllerReference(nom, role, r.Scheme); err != nil {
			return fmt.Errorf("ownerref sequence probe Role: %w", err)
		}
		if err := r.Create(ctx, role); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create sequence probe Role: %w", err)
		}
	} else {
		role.Rules = desiredRules
		if err := r.Update(ctx, role); err != nil {
			return fmt.Errorf("update sequence probe Role: %w", err)
		}
	}

	rb := &rbacv1.RoleBinding{}
	desiredRB := rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: nom.Namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     saName,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      saName,
			Namespace: nom.Namespace,
		}},
	}
	if err := r.Get(ctx, key, rb); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get sequence probe RoleBinding: %w", err)
		}
		rb = &desiredRB
		if err := controllerutil.SetControllerReference(nom, rb, r.Scheme); err != nil {
			return fmt.Errorf("ownerref sequence probe RoleBinding: %w", err)
		}
		if err := r.Create(ctx, rb); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create sequence probe RoleBinding: %w", err)
		}
		return nil
	}
	rb.RoleRef = desiredRB.RoleRef
	rb.Subjects = desiredRB.Subjects
	if err := r.Update(ctx, rb); err != nil {
		return fmt.Errorf("update sequence probe RoleBinding: %w", err)
	}
	return nil
}

func (r *NominatimReconciler) ensureSequenceReportConfigMap(ctx context.Context, nom *nominatimv1alpha1.Nominatim) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      sequenceReportConfigMapName(nom),
		Namespace: nom.Namespace,
	}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		if err := controllerutil.SetControllerReference(nom, cm, r.Scheme); err != nil {
			return err
		}
		if cm.Labels == nil {
			cm.Labels = map[string]string{}
		}
		cm.Labels["app.kubernetes.io/managed-by"] = "nominatim-operator"
		cm.Labels["nominatim.zebernst.dev/nominatim"] = nom.Name
		cm.Labels["nominatim.zebernst.dev/component"] = "sequence-report"
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("ensure sequence report ConfigMap: %w", err)
	}
	return nil
}

func (r *NominatimReconciler) observeSequenceForOperation(
	ctx context.Context,
	nom *nominatimv1alpha1.Nominatim,
	op *nominatimv1alpha1.NominatimOperation,
) error {
	log := logf.FromContext(ctx)
	jobName := sequenceProbeJobName(op)
	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: nom.Namespace}, job)
	if apierrors.IsNotFound(err) {
		built, berr := r.buildSequenceProbeJob(nom, op)
		if berr != nil {
			return berr
		}
		if err := r.Create(ctx, built); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create sequence probe Job %q: %w", jobName, err)
		}
		log.Info("created sequence probe Job", "job", jobName, "operation", op.Name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("get sequence probe Job %q: %w", jobName, err)
	}

	if job.Status.Succeeded > 0 {
		return r.markSequenceObserved(ctx, op)
	}
	if job.Status.Failed > 0 {
		log.Info("sequence probe Job failed; will not block reconcile", "job", jobName)
		return r.markSequenceObserved(ctx, op)
	}
	return nil
}

func (r *NominatimReconciler) markSequenceObserved(ctx context.Context, op *nominatimv1alpha1.NominatimOperation) error {
	latest := &nominatimv1alpha1.NominatimOperation{}
	if err := r.Get(ctx, types.NamespacedName{Name: op.Name, Namespace: op.Namespace}, latest); err != nil {
		return client.IgnoreNotFound(err)
	}
	if latest.Annotations != nil && latest.Annotations[annotationSequenceObserved] == "true" {
		return nil
	}
	patch := latest.DeepCopy()
	if patch.Annotations == nil {
		patch.Annotations = map[string]string{}
	}
	patch.Annotations[annotationSequenceObserved] = "true"
	return r.Patch(ctx, patch, client.MergeFrom(latest))
}

func (r *NominatimReconciler) buildSequenceProbeJob(
	nom *nominatimv1alpha1.Nominatim,
	op *nominatimv1alpha1.NominatimOperation,
) (*batchv1.Job, error) {
	projectClaim := volumeClaimName(nom.Spec.Project.Volume, ProjectPVCName(nom))
	image := resolveImage(nil, defaultWorkerRepository)
	if nom.Spec.Worker != nil && nom.Spec.Worker.Image != nil {
		image = resolveImage(nom.Spec.Worker.Image, defaultWorkerRepository)
	}
	pullPolicy := corev1.PullIfNotPresent
	if nom.Spec.Worker != nil {
		pullPolicy = resolvePullPolicy(nom.Spec.Worker.Image)
	}

	backoff := int32(1)
	ttl := int32(600)
	automount := true
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sequenceProbeJobName(op),
			Namespace: nom.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":     "nominatim-operator",
				"nominatim.zebernst.dev/nominatim": nom.Name,
				"nominatim.zebernst.dev/operation": op.Name,
				"nominatim.zebernst.dev/component": labelSequenceProbe,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"nominatim.zebernst.dev/component": labelSequenceProbe,
						"nominatim.zebernst.dev/nominatim": nom.Name,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					ServiceAccountName:           sequenceProbeSAName(nom),
					AutomountServiceAccountToken: &automount,
					Containers: []corev1.Container{{
						Name:            "sequence-probe",
						Image:           image,
						ImagePullPolicy: pullPolicy,
						Command:         []string{"/opt/nominatim/scripts/report-sequence.sh"},
						Env: []corev1.EnvVar{
							{Name: "PROJECT_DIR", Value: projectMountPath},
							{Name: "NOMINATIM_NAME", Value: nom.Name},
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      projectVolumeName,
							MountPath: projectMountPath,
							ReadOnly:  true,
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: projectVolumeName,
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: projectClaim,
								ReadOnly:  true,
							},
						},
					}},
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(nom, job, r.Scheme); err != nil {
		return nil, fmt.Errorf("ownerref sequence probe Job: %w", err)
	}
	return job, nil
}

func (r *NominatimReconciler) applySequenceReportConfigMap(ctx context.Context, nom *nominatimv1alpha1.Nominatim) error {
	cm := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: sequenceReportConfigMapName(nom), Namespace: nom.Namespace}, cm)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get sequence report ConfigMap: %w", err)
	}
	raw := cm.Data[sequenceReportCMKey]
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	report, err := parseSequenceReport(raw)
	if err != nil {
		return fmt.Errorf("parse sequence report ConfigMap: %w", err)
	}
	applySequenceReportMap(nom, report, nil)
	return nil
}

// parseSequenceReport decodes region path → sequence identity JSON.
func parseSequenceReport(raw string) (map[string]string, error) {
	out := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// applySequenceReportMap merges report into nom.Status.Regions SequenceState fields.
func applySequenceReportMap(nom *nominatimv1alpha1.Nominatim, report map[string]string, observedAt *metav1.Time) {
	if len(report) == 0 || len(nom.Status.Regions) == 0 {
		return
	}
	now := metav1.Now()
	if observedAt != nil {
		now = *observedAt
	}
	byName := make(map[string]int, len(nom.Status.Regions))
	for i, rs := range nom.Status.Regions {
		byName[rs.Name] = i
	}
	for region, state := range report {
		if state == "" {
			continue
		}
		idx, ok := byName[region]
		if !ok {
			continue
		}
		rs := &nom.Status.Regions[idx]
		if rs.SequenceState == state {
			continue
		}
		rs.SequenceState = state
		t := now
		rs.LastUpdatedTime = &t
	}
}
