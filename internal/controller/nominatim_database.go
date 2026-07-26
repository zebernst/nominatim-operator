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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// CNPGClusterGVK is the CloudNativePG Cluster resource (owned/watched directly; not CNPG-I).
var CNPGClusterGVK = schema.GroupVersionKind{
	Group:   "postgresql.cnpg.io",
	Version: "v1",
	Kind:    "Cluster",
}

// CNPGEffects abstracts Barman backup pause and postgres parameter patches for CNPG Clusters.
// Operations (nominatim-vzw) call these hooks; degraded connectionSecretRef mode must never invoke them.
type CNPGEffects interface {
	PauseBackups(ctx context.Context, cluster *unstructured.Unstructured) error
	ResumeBackups(ctx context.Context, cluster *unstructured.Unstructured) error
	ApplyParameters(ctx context.Context, cluster *unstructured.Unstructured, params map[string]string) error
}

// defaultCNPGEffects is a stub that records intent for later Operation wiring (no live Barman patch yet).
type defaultCNPGEffects struct{}

func (defaultCNPGEffects) PauseBackups(_ context.Context, _ *unstructured.Unstructured) error {
	return nil
}

func (defaultCNPGEffects) ResumeBackups(_ context.Context, _ *unstructured.Unstructured) error {
	return nil
}

func (defaultCNPGEffects) ApplyParameters(_ context.Context, _ *unstructured.Unstructured, _ map[string]string) error {
	return nil
}

func (r *NominatimReconciler) cnpgEffects() CNPGEffects {
	if r.CNPGEffects != nil {
		return r.CNPGEffects
	}
	return defaultCNPGEffects{}
}

// OwnedCNPGClusterName is the default name for a Cluster created from spec.database.cluster.
func OwnedCNPGClusterName(nom *nominatimv1alpha1.Nominatim) string {
	return nom.Name + "-pg"
}

// CNPGAppSecretName is the conventional CNPG application-user Secret for a Cluster.
func CNPGAppSecretName(clusterName string) string {
	return clusterName + "-app"
}

// reconcileDatabase branches on exactly one of cluster / clusterRef / connectionSecretRef.
func (r *NominatimReconciler) reconcileDatabase(ctx context.Context, nom *nominatimv1alpha1.Nominatim) error {
	db := nom.Spec.Database
	switch {
	case db.ConnectionSecretRef != nil:
		return r.reconcileDatabaseConnectionSecret(ctx, nom)
	case db.ClusterRef != nil:
		return r.reconcileDatabaseClusterRef(ctx, nom)
	case db.Cluster != nil:
		return r.reconcileDatabaseClusterCreate(ctx, nom)
	default:
		return fmt.Errorf("database requires exactly one of cluster, clusterRef, or connectionSecretRef")
	}
}

func (r *NominatimReconciler) reconcileDatabaseConnectionSecret(ctx context.Context, nom *nominatimv1alpha1.Nominatim) error {
	name := nom.Spec.Database.ConnectionSecretRef.Name
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: nom.Namespace}, secret); err != nil {
		return fmt.Errorf("connectionSecretRef %q: %w", name, err)
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode:                 nominatimv1alpha1.DatabaseModeConnectionSecret,
		ConnectionSecretName: name,
		Degraded:             true,
	}
	return nil
}

func (r *NominatimReconciler) reconcileDatabaseClusterRef(ctx context.Context, nom *nominatimv1alpha1.Nominatim) error {
	clusterName := nom.Spec.Database.ClusterRef.Name
	cluster, err := r.getCNPGCluster(ctx, nom.Namespace, clusterName)
	if err != nil {
		return fmt.Errorf("clusterRef %q: %w", clusterName, err)
	}
	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode:                 nominatimv1alpha1.DatabaseModeClusterAttached,
		ClusterName:          cluster.GetName(),
		ConnectionSecretName: CNPGAppSecretName(clusterName),
		Degraded:             false,
	}
	return nil
}

func (r *NominatimReconciler) reconcileDatabaseClusterCreate(ctx context.Context, nom *nominatimv1alpha1.Nominatim) error {
	create := nom.Spec.Database.Cluster
	clusterName := OwnedCNPGClusterName(nom)

	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	cluster.SetName(clusterName)
	cluster.SetNamespace(nom.Namespace)

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cluster, func() error {
		if err := controllerutil.SetControllerReference(nom, cluster, r.Scheme); err != nil {
			return err
		}
		instances := int64(1)
		if create.Instances != nil {
			instances = int64(*create.Instances)
		}
		if err := unstructured.SetNestedField(cluster.Object, instances, "spec", "instances"); err != nil {
			return err
		}
		if create.Storage != nil {
			storage, err := cnpgStorageFromVolumeClaimTemplate(create.Storage)
			if err != nil {
				return err
			}
			// Parent "spec" map exists after SetNestedField above.
			_ = unstructured.SetNestedMap(cluster.Object, storage, "spec", "storage")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconcile owned CNPG Cluster %q: %w", clusterName, err)
	}

	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode:                 nominatimv1alpha1.DatabaseModeClusterManaged,
		ClusterName:          clusterName,
		ConnectionSecretName: CNPGAppSecretName(clusterName),
		Degraded:             false,
	}
	return nil
}

// cnpgStorageFromVolumeClaimTemplate maps Nominatim storage passthrough into CNPG storage
// without inventing storageClass names.
func cnpgStorageFromVolumeClaimTemplate(vct *nominatimv1alpha1.VolumeClaimTemplate) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	if vct.Spec.StorageClassName != nil && *vct.Spec.StorageClassName != "" {
		out["storageClass"] = *vct.Spec.StorageClassName
	}
	if qty, ok := vct.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		out["size"] = qty.String()
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("database.cluster.storage must set size and/or storageClass via volumeClaimTemplate")
	}
	return out, nil
}

func (r *NominatimReconciler) getCNPGCluster(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, cluster); err != nil {
		return nil, err
	}
	return cluster, nil
}

// SetBackupPaused pauses or resumes continuous (Barman) backup on the managed/attached CNPG Cluster.
// No-op in degraded connectionSecretRef mode — never attempts Barman pause there.
func (r *NominatimReconciler) SetBackupPaused(ctx context.Context, nom *nominatimv1alpha1.Nominatim, paused bool) error {
	if nom.Status.Database.Degraded || nom.Status.Database.Mode == nominatimv1alpha1.DatabaseModeConnectionSecret {
		return nil
	}
	clusterName := nom.Status.Database.ClusterName
	if clusterName == "" {
		return fmt.Errorf("cannot pause backups: no CNPG cluster in status")
	}
	cluster, err := r.getCNPGCluster(ctx, nom.Namespace, clusterName)
	if err != nil {
		return err
	}
	if paused {
		return r.cnpgEffects().PauseBackups(ctx, cluster)
	}
	return r.cnpgEffects().ResumeBackups(ctx, cluster)
}

// ApplyPostgresProfile applies import or runtime postgresProfiles to the CNPG Cluster.
// No-op in degraded connectionSecretRef mode. Intended for Operations to call later.
func (r *NominatimReconciler) ApplyPostgresProfile(ctx context.Context, nom *nominatimv1alpha1.Nominatim, which string) error {
	if nom.Status.Database.Degraded || nom.Status.Database.Mode == nominatimv1alpha1.DatabaseModeConnectionSecret {
		return nil
	}
	var params map[string]string
	profiles := nom.Spec.Database.PostgresProfiles
	switch which {
	case "import":
		if profiles != nil {
			params = profiles.Import
		}
	case "runtime":
		if profiles != nil {
			params = profiles.Runtime
		}
	default:
		return fmt.Errorf("unknown postgres profile %q (want import or runtime)", which)
	}
	if len(params) == 0 {
		return nil
	}
	clusterName := nom.Status.Database.ClusterName
	if clusterName == "" {
		return fmt.Errorf("cannot apply postgres profile: no CNPG cluster in status")
	}
	cluster, err := r.getCNPGCluster(ctx, nom.Namespace, clusterName)
	if err != nil {
		return err
	}
	return r.cnpgEffects().ApplyParameters(ctx, cluster, params)
}
