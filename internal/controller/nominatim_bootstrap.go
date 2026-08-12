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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

const (
	bootstrapNameSuffix = "-bootstrap"
)

// BootstrapOperationName is the deterministic name for the auto-created Bootstrap
// NominatimOperation for a given NominatimInstance.
func BootstrapOperationName(nom *nominatimv1alpha1.NominatimInstance) string {
	return nom.Name + bootstrapNameSuffix
}

// listOperationsForParent lists NominatimOperations in the same namespace whose
// nominatimRef targets nom.
func (r *NominatimInstanceReconciler) listOperationsForParent(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance) ([]nominatimv1alpha1.NominatimOperation, error) {
	list := &nominatimv1alpha1.NominatimOperationList{}
	if err := r.List(ctx, list, client.InNamespace(nom.Namespace)); err != nil {
		return nil, err
	}
	out := make([]nominatimv1alpha1.NominatimOperation, 0, len(list.Items))
	for _, op := range list.Items {
		if op.Spec.NominatimRef.Name == nom.Name {
			out = append(out, op)
		}
	}
	return out, nil
}

// ensureBootstrapOperation creates the auto-bootstrap Operation when the instance has
// desired regions, no observed regions yet, and no Bootstrap Operation (active or
// otherwise) already targets this parent.
func (r *NominatimInstanceReconciler) ensureBootstrapOperation(ctx context.Context, nom *nominatimv1alpha1.NominatimInstance, peers []nominatimv1alpha1.NominatimOperation) error {
	if len(nom.Spec.Regions) == 0 {
		return nil
	}
	if len(nom.Status.Regions) > 0 {
		return nil
	}

	for i := range peers {
		if peers[i].Spec.Type == nominatimv1alpha1.NominatimOperationBootstrap {
			// A Bootstrap Operation already exists for this parent (active or terminal) —
			// never auto-create a second one; retries are a manual/operator concern.
			return nil
		}
	}

	if nom.Status.Database.ConnectionSecretName == "" {
		return fmt.Errorf("cannot create Bootstrap operation: status.database.connectionSecretName is empty")
	}

	op := &nominatimv1alpha1.NominatimOperation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      BootstrapOperationName(nom),
			Namespace: nom.Namespace,
		},
		Spec: nominatimv1alpha1.NominatimOperationSpec{
			Type:         nominatimv1alpha1.NominatimOperationBootstrap,
			NominatimRef: nominatimv1alpha1.LocalObjectReference{Name: nom.Name},
			Regions:      append([]string(nil), nom.Spec.Regions...),
		},
	}
	if err := controllerutil.SetControllerReference(nom, op, r.Scheme); err != nil {
		return fmt.Errorf("set controller reference on Bootstrap operation: %w", err)
	}
	if err := r.Create(ctx, op); err != nil {
		return fmt.Errorf("create Bootstrap operation: %w", err)
	}
	return nil
}
