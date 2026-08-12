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
	// cnpgDatabaseReclaimDelete drops the Postgres database when the Database CR is deleted.
	cnpgDatabaseReclaimDelete = "delete"
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
			if err := applyOwnedCNPGClusterTune(cluster, create); err != nil {
				return err
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

// applyOwnedCNPGClusterTune writes instance-tune fields (resources, affinity,
// topologySpreadConstraints) from DatabaseClusterCreate onto the Cluster unstructured.
// Bootstrap / imageName / managed.roles are not accepted from the CR — they stay operator-owned.
func applyOwnedCNPGClusterTune(cluster *unstructured.Unstructured, create *nominatimv1alpha1.DatabaseClusterCreate) error {
	if create == nil {
		return nil
	}
	if create.Resources != nil {
		raw, err := json.Marshal(create.Resources)
		if err != nil {
			return fmt.Errorf("marshal cluster.resources: %w", err)
		}
		var asMap map[string]interface{}
		if err := json.Unmarshal(raw, &asMap); err != nil {
			return fmt.Errorf("decode cluster.resources: %w", err)
		}
		if err := unstructured.SetNestedMap(cluster.Object, asMap, "spec", "resources"); err != nil {
			return err
		}
	}
	if create.Affinity != nil && len(create.Affinity.Raw) > 0 {
		var affinity map[string]interface{}
		if err := json.Unmarshal(create.Affinity.Raw, &affinity); err != nil {
			return fmt.Errorf("decode cluster.affinity: %w", err)
		}
		if err := unstructured.SetNestedMap(cluster.Object, affinity, "spec", "affinity"); err != nil {
			return err
		}
	}
	if len(create.TopologySpreadConstraints) > 0 {
		raw, err := json.Marshal(create.TopologySpreadConstraints)
		if err != nil {
			return fmt.Errorf("marshal cluster.topologySpreadConstraints: %w", err)
		}
		var asSlice []interface{}
		if err := json.Unmarshal(raw, &asSlice); err != nil {
			return fmt.Errorf("decode cluster.topologySpreadConstraints: %w", err)
		}
		if err := unstructured.SetNestedSlice(cluster.Object, asSlice, "spec", "topologySpreadConstraints"); err != nil {
			return err
		}
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
// While a Reimport Operation is mid drop/recreate (reset=pending), skip CreateOrUpdate so
// we do not fight the Operation's Delete or recreate under the pre-Reimport UID.
func (r *NominatimReconciler) reconcileOwnedCNPGDatabase(ctx context.Context, nom *nominatimv1alpha1.Nominatim, clusterName string) error {
	pending, err := hasPendingReimportDatabaseReset(ctx, r.Client, nom)
	if err != nil {
		return err
	}
	if pending {
		return nil
	}
	return ensureOwnedCNPGDatabase(ctx, r.Client, r.Scheme, nom, clusterName)
}

// hasPendingReimportDatabaseReset is true when an active Reimport Operation against nom
// has recorded the drop/recreate handshake as pending (not yet done).
func hasPendingReimportDatabaseReset(ctx context.Context, c client.Client, nom *nominatimv1alpha1.Nominatim) (bool, error) {
	list := &nominatimv1alpha1.NominatimOperationList{}
	if err := c.List(ctx, list, client.InNamespace(nom.Namespace)); err != nil {
		return false, err
	}
	for i := range list.Items {
		op := &list.Items[i]
		if op.Spec.NominatimRef.Name != nom.Name {
			continue
		}
		if op.Spec.Type != nominatimv1alpha1.NominatimOperationReimport {
			continue
		}
		if !isActiveOperationPhase(op.Status.Phase) {
			continue
		}
		if op.Annotations[annotationReimportDBReset] == reimportDBResetPending {
			return true, nil
		}
	}
	return false, nil
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
	if err := unstructured.SetNestedField(db.Object, cnpgDatabaseReclaimDelete, "spec", "databaseReclaimPolicy"); err != nil {
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

// getCNPGCluster loads a CNPG Cluster for attach/create (Nominatim) and Operation side-effects.
func getCNPGCluster(ctx context.Context, c client.Client, namespace, name string) (*unstructured.Unstructured, error) {
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(CNPGClusterGVK)
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, cluster); err != nil {
		return nil, err
	}
	return cluster, nil
}
