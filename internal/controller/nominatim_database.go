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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

// CNPGClusterGVK is the CloudNativePG Cluster resource (owned/watched directly; not CNPG-I).
var CNPGClusterGVK = schema.GroupVersionKind{
	Group:   "postgresql.cnpg.io",
	Version: "v1",
	Kind:    "Cluster",
}

// CNPGDatabaseGVK is the CloudNativePG Database resource used for declarative extensions.
var CNPGDatabaseGVK = schema.GroupVersionKind{
	Group:   "postgresql.cnpg.io",
	Version: "v1",
	Kind:    "Database",
}

// CNPGEffects, defaultCNPGEffects, and each reconciler's cnpgEffects() accessor live in
// cnpg_effects.go, since both NominatimReconciler and NominatimOperationReconciler use them.

// OwnedCNPGClusterName is the default name for a Cluster created from spec.database.cluster.
func OwnedCNPGClusterName(nom *nominatimv1alpha1.Nominatim) string {
	return nom.Name + "-pg"
}

// OwnedCNPGDatabaseName is the owned Database CR that declares Nominatim extensions on the
// application database created by Cluster bootstrap.initdb.
func OwnedCNPGDatabaseName(nom *nominatimv1alpha1.Nominatim) string {
	return OwnedCNPGClusterName(nom) + "-nominatim"
}

// CNPGAppSecretName is the conventional CNPG application-user Secret for a Cluster.
func CNPGAppSecretName(clusterName string) string {
	return clusterName + "-app"
}

// Default CNPG application database/owner created via bootstrap.initdb.
// Nominatim connects via the Cluster's {name}-app Secret (dbname/user match these).
const (
	cnpgAppDatabaseName = "nominatim"
	cnpgAppOwnerName    = "nominatim"
	// Official CNPG PostGIS operand from postgis-containers (bake pipeline).
	// Prefer the new multi-arch rolling tag (amd64+arm64); legacy :17 is amd64-only.
	// "standard" = PostGIS without Barman Cloud (enough for Nominatim; backups optional).
	// See https://github.com/cloudnative-pg/postgis-containers
	cnpgDefaultPostGISImage = "ghcr.io/cloudnative-pg/postgis:17-3-standard-trixie"
	// cnpgNominatimWebRole is Nominatim's default DATABASE_WEBUSER (grants.sql target).
	cnpgNominatimWebRole = "www-data"
)

// cnpgNominatimExtensions are installed via an owned CNPG Database CR (spec.extensions),
// not imperative postInitTemplateSQL.
var cnpgNominatimExtensions = []string{
	"hstore",
	"postgis",
	"postgis_raster",
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
	ref := nom.Spec.Database.ClusterRef
	clusterName := ref.Name
	cluster, err := r.getCNPGCluster(ctx, nom.Namespace, clusterName)
	if err != nil {
		return fmt.Errorf("clusterRef %q: %w", clusterName, err)
	}

	secretName := CNPGAppSecretName(clusterName)
	if ref.ConnectionSecretRef != nil && ref.ConnectionSecretRef.Name != "" {
		secretName = ref.ConnectionSecretRef.Name
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: nom.Namespace}, secret); err != nil {
			return fmt.Errorf("clusterRef.connectionSecretRef %q: %w", secretName, err)
		}
	}

	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode:                 nominatimv1alpha1.DatabaseModeClusterAttached,
		ClusterName:          cluster.GetName(),
		ConnectionSecretName: secretName,
		Degraded:             false,
	}
	return nil
}

func (r *NominatimReconciler) reconcileDatabaseClusterCreate(ctx context.Context, nom *nominatimv1alpha1.Nominatim) error {
	create := nom.Spec.Database.Cluster
	clusterName := OwnedCNPGClusterName(nom)

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
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
				// Set fields individually — replacing the whole storage map would wipe
				// CNPG-managed keys and cause update churn.
				for key, val := range storage {
					if err := unstructured.SetNestedField(cluster.Object, val, "spec", "storage", key); err != nil {
						return err
					}
				}
			}
			if err := unstructured.SetNestedField(cluster.Object, cnpgDefaultPostGISImage, "spec", "imageName"); err != nil {
				return err
			}
			// bootstrap.initdb is only meaningful on first create; rewriting it on every
			// reconcile fights CNPG's expanded defaults (encoding/locale/…) and causes
			// perpetual resourceVersion conflicts that block Database CR ownership.
			if cluster.GetResourceVersion() == "" {
				if err := unstructured.SetNestedField(cluster.Object, cnpgAppDatabaseName, "spec", "bootstrap", "initdb", "database"); err != nil {
					return err
				}
				if err := unstructured.SetNestedField(cluster.Object, cnpgAppOwnerName, "spec", "bootstrap", "initdb", "owner"); err != nil {
					return err
				}
			}
			// Nominatim grants.sql targets role www-data — declare via CNPG managed.roles.
			// Merge only: never replace CNPG-expanded role objects once www-data is present.
			if err := ensureCNPGManagedWebRole(cluster); err != nil {
				return err
			}
			return nil
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("reconcile owned CNPG Cluster %q: %w", clusterName, err)
	}

	if err := r.reconcileOwnedCNPGDatabase(ctx, nom, clusterName); err != nil {
		return err
	}

	nom.Status.Database = nominatimv1alpha1.DatabaseStatus{
		Mode:                 nominatimv1alpha1.DatabaseModeClusterManaged,
		ClusterName:          clusterName,
		ConnectionSecretName: CNPGAppSecretName(clusterName),
		Degraded:             false,
	}
	return nil
}

// ensureCNPGManagedWebRole appends www-data when missing; leaves existing role entries untouched
// so CNPG-added fields (inherit, connectionLimit, …) do not churn CreateOrUpdate.
func ensureCNPGManagedWebRole(cluster *unstructured.Unstructured) error {
	roles, _, err := unstructured.NestedSlice(cluster.Object, "spec", "managed", "roles")
	if err != nil {
		return err
	}
	for _, raw := range roles {
		role, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if role["name"] == cnpgNominatimWebRole {
			return nil
		}
	}
	roles = append(roles, map[string]interface{}{
		"name":    cnpgNominatimWebRole,
		"ensure":  "present",
		"login":   false,
		"comment": "Nominatim DATABASE_WEBUSER (read-only grants target)",
	})
	return unstructured.SetNestedSlice(cluster.Object, roles, "spec", "managed", "roles")
}

// reconcileOwnedCNPGDatabase owns a CNPG Database CR that declaratively installs Nominatim
// extensions (hstore/postgis/…) on the initdb application database.
func (r *NominatimReconciler) reconcileOwnedCNPGDatabase(ctx context.Context, nom *nominatimv1alpha1.Nominatim, clusterName string) error {
	return ensureOwnedCNPGDatabase(ctx, r.Client, r.Scheme, nom, clusterName)
}

// ensureOwnedCNPGDatabase CreateOrUpdates the owned CNPG Database CR for nom.
// Used by the Nominatim reconciler and by Reimport database reset.
func ensureOwnedCNPGDatabase(ctx context.Context, c client.Client, scheme *runtime.Scheme, nom *nominatimv1alpha1.Nominatim, clusterName string) error {
	dbName := OwnedCNPGDatabaseName(nom)

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		db := &unstructured.Unstructured{}
		db.SetGroupVersionKind(CNPGDatabaseGVK)
		db.SetName(dbName)
		db.SetNamespace(nom.Namespace)

		_, err := controllerutil.CreateOrUpdate(ctx, c, db, func() error {
			if err := controllerutil.SetControllerReference(nom, db, scheme); err != nil {
				return err
			}
			return applyOwnedCNPGDatabaseSpec(db, clusterName)
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("reconcile owned CNPG Database %q: %w", dbName, err)
	}
	return nil
}

// applyOwnedCNPGDatabaseSpec sets the desired Database CR fields (name/owner/cluster/extensions/reclaim).
func applyOwnedCNPGDatabaseSpec(db *unstructured.Unstructured, clusterName string) error {
	exts := make([]interface{}, 0, len(cnpgNominatimExtensions))
	for _, name := range cnpgNominatimExtensions {
		exts = append(exts, map[string]interface{}{
			"name":   name,
			"ensure": "present",
		})
	}
	if err := unstructured.SetNestedField(db.Object, cnpgAppDatabaseName, "spec", "name"); err != nil {
		return err
	}
	if err := unstructured.SetNestedField(db.Object, cnpgAppOwnerName, "spec", "owner"); err != nil {
		return err
	}
	if err := unstructured.SetNestedField(db.Object, clusterName, "spec", "cluster", "name"); err != nil {
		return err
	}
	if err := unstructured.SetNestedSlice(db.Object, exts, "spec", "extensions"); err != nil {
		return err
	}
	// delete reclaim so Reimport can drop+recreate the application database via the CR.
	if err := unstructured.SetNestedField(db.Object, "delete", "spec", "databaseReclaimPolicy"); err != nil {
		return err
	}
	if err := unstructured.SetNestedField(db.Object, "present", "spec", "ensure"); err != nil {
		return err
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
	return getCNPGCluster(ctx, r.Client, namespace, name)
}

// getCNPGCluster is the shared lookup used by both the Nominatim and NominatimOperation
// reconcilers (the latter acts on the parent's Cluster from cnpg_effects.go call sites).
func getCNPGCluster(ctx context.Context, c client.Client, namespace, name string) (*unstructured.Unstructured, error) {
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, cluster); err != nil {
		return nil, err
	}
	return cluster, nil
}

// SetBackupPaused pauses or resumes continuous (Barman) backup on the managed/attached CNPG Cluster.
// No-op in degraded connectionSecretRef mode — never attempts Barman pause there.
func (r *NominatimReconciler) SetBackupPaused(ctx context.Context, nom *nominatimv1alpha1.Nominatim, paused bool) error {
	return setBackupPaused(ctx, r.Client, r.cnpgEffects(), nom, paused)
}

// setBackupPaused is the shared implementation behind NominatimReconciler.SetBackupPaused,
// reused by the NominatimOperation reconciler (nominatimoperation_effects.go) so both act
// through the exact same degraded-mode guard and CNPGEffects call.
func setBackupPaused(ctx context.Context, c client.Client, effects CNPGEffects, nom *nominatimv1alpha1.Nominatim, paused bool) error {
	if nom.Status.Database.Degraded || nom.Status.Database.Mode == nominatimv1alpha1.DatabaseModeConnectionSecret {
		return nil
	}
	clusterName := nom.Status.Database.ClusterName
	if clusterName == "" {
		return fmt.Errorf("cannot pause backups: no CNPG cluster in status")
	}
	cluster, err := getCNPGCluster(ctx, c, nom.Namespace, clusterName)
	if err != nil {
		return err
	}
	if paused {
		return effects.PauseBackups(ctx, cluster)
	}
	return effects.ResumeBackups(ctx, cluster)
}

// ApplyPostgresProfile applies import or runtime postgresProfiles to the CNPG Cluster.
// No-op in degraded connectionSecretRef mode. Intended for Operations to call later.
func (r *NominatimReconciler) ApplyPostgresProfile(ctx context.Context, nom *nominatimv1alpha1.Nominatim, which string) error {
	return applyPostgresProfile(ctx, r.Client, r.cnpgEffects(), nom, which)
}

// applyPostgresProfile is the shared implementation behind NominatimReconciler.ApplyPostgresProfile,
// reused by the NominatimOperation reconciler (nominatimoperation_effects.go).
//
// Profile-managed keys are the union of import ∪ runtime. When switching profiles, keys that
// belong to that union but are absent from the target profile are removed so import-only knobs
// (e.g. work_mem) do not leak into "runtime" forever. Keys outside the union are left alone.
func applyPostgresProfile(ctx context.Context, c client.Client, effects CNPGEffects, nom *nominatimv1alpha1.Nominatim, which string) error {
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
	removeKeys := profileKeysToRemove(profiles, params)
	if len(params) == 0 && len(removeKeys) == 0 {
		return nil
	}
	clusterName := nom.Status.Database.ClusterName
	if clusterName == "" {
		return fmt.Errorf("cannot apply postgres profile: no CNPG cluster in status")
	}
	cluster, err := getCNPGCluster(ctx, c, nom.Namespace, clusterName)
	if err != nil {
		return err
	}
	return effects.ApplyParameters(ctx, cluster, params, removeKeys)
}

// profileKeysToRemove returns profile-managed keys (import ∪ runtime) that are not present in
// the target profile map, so ApplyParameters can delete them when switching profiles.
func profileKeysToRemove(profiles *nominatimv1alpha1.PostgresProfiles, target map[string]string) []string {
	if profiles == nil {
		return nil
	}
	managed := map[string]struct{}{}
	for k := range profiles.Import {
		managed[k] = struct{}{}
	}
	for k := range profiles.Runtime {
		managed[k] = struct{}{}
	}
	var remove []string
	for k := range managed {
		if _, ok := target[k]; !ok {
			remove = append(remove, k)
		}
	}
	return remove
}
