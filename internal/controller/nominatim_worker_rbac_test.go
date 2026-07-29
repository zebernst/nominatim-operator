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
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

func TestEnsureWorkerReporterRBAC_CreatesSARoleBinding(t *testing.T) {
	scheme := testScheme(t)
	ctx := context.Background()
	nom := baseNominatim("seq-rbac")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nom).Build()
	r := &NominatimReconciler{Client: c, Scheme: scheme}

	if err := r.ensureWorkerReporterRBAC(ctx, nom); err != nil {
		t.Fatalf("ensureWorkerReporterRBAC: %v", err)
	}

	sa := &corev1.ServiceAccount{}
	if err := c.Get(ctx, types.NamespacedName{Name: "seq-rbac-worker", Namespace: nom.Namespace}, sa); err != nil {
		t.Fatalf("get SA: %v", err)
	}
	role := &rbacv1.Role{}
	if err := c.Get(ctx, types.NamespacedName{Name: "seq-rbac-worker", Namespace: nom.Namespace}, role); err != nil {
		t.Fatalf("get Role: %v", err)
	}
	if len(role.Rules) != 1 || role.Rules[0].Resources[0] != "nominatimoperations" {
		t.Fatalf("role rules=%v", role.Rules)
	}
	rb := &rbacv1.RoleBinding{}
	if err := c.Get(ctx, types.NamespacedName{Name: "seq-rbac-worker", Namespace: nom.Namespace}, rb); err != nil {
		t.Fatalf("get RoleBinding: %v", err)
	}
	if rb.RoleRef.Name != "seq-rbac-worker" || len(rb.Subjects) != 1 || rb.Subjects[0].Name != "seq-rbac-worker" {
		t.Fatalf("binding=%+v", rb)
	}

	// Idempotent second call.
	if err := r.ensureWorkerReporterRBAC(ctx, nom); err != nil {
		t.Fatalf("second ensureWorkerReporterRBAC: %v", err)
	}
}

func TestWorkerReporterSAName(t *testing.T) {
	nom := &nominatimv1alpha1.Nominatim{}
	nom.Name = "demo"
	if got := workerReporterSAName(nom); got != "demo-worker" {
		t.Fatalf("got %q", got)
	}
}
