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

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// workerReporterSAName is the per-Nominatim ServiceAccount used by Operation Jobs
// to PATCH sequence-report annotations onto their NominatimOperation.
func workerReporterSAName(nom *nominatimv1alpha1.Nominatim) string {
	return nom.Name + "-worker"
}

// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch

// ensureWorkerReporterRBAC creates the ServiceAccount, Role, and RoleBinding that
// let worker Jobs annotate their NominatimOperation with Geofabrik sequence state.
func (r *NominatimReconciler) ensureWorkerReporterRBAC(ctx context.Context, nom *nominatimv1alpha1.Nominatim) error {
	saName := workerReporterSAName(nom)
	key := types.NamespacedName{Name: saName, Namespace: nom.Namespace}

	sa := &corev1.ServiceAccount{}
	if err := r.Get(ctx, key, sa); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get worker ServiceAccount: %w", err)
		}
		sa = &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: nom.Namespace},
		}
		if err := controllerutil.SetControllerReference(nom, sa, r.Scheme); err != nil {
			return fmt.Errorf("ownerref worker ServiceAccount: %w", err)
		}
		if err := r.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create worker ServiceAccount: %w", err)
		}
	}

	desiredRules := []rbacv1.PolicyRule{{
		APIGroups: []string{nominatimv1alpha1.GroupVersion.Group},
		Resources: []string{"nominatimoperations"},
		Verbs:     []string{"get", "patch"},
	}}
	role := &rbacv1.Role{}
	if err := r.Get(ctx, key, role); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get worker Role: %w", err)
		}
		role = &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: nom.Namespace},
			Rules:      desiredRules,
		}
		if err := controllerutil.SetControllerReference(nom, role, r.Scheme); err != nil {
			return fmt.Errorf("ownerref worker Role: %w", err)
		}
		if err := r.Create(ctx, role); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create worker Role: %w", err)
		}
	} else {
		role.Rules = desiredRules
		if err := r.Update(ctx, role); err != nil {
			return fmt.Errorf("update worker Role: %w", err)
		}
	}

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
	rb := &rbacv1.RoleBinding{}
	if err := r.Get(ctx, key, rb); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get worker RoleBinding: %w", err)
		}
		rb = &desiredRB
		if err := controllerutil.SetControllerReference(nom, rb, r.Scheme); err != nil {
			return fmt.Errorf("ownerref worker RoleBinding: %w", err)
		}
		if err := r.Create(ctx, rb); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create worker RoleBinding: %w", err)
		}
		return nil
	}
	rb.RoleRef = desiredRB.RoleRef
	rb.Subjects = desiredRB.Subjects
	if err := r.Update(ctx, rb); err != nil {
		return fmt.Errorf("update worker RoleBinding: %w", err)
	}
	return nil
}
