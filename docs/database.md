# Database and backup

Nominatim needs Postgres (almost always [CloudNativePG](https://cloudnative-pg.io/)). How you attach that database decides whether you get continuous backup, parameter profiles, and backup pause around Operations.

## Choose an attach mode

| Mode | When to use |
|------|-------------|
| `spec.database.clusterRef` | **Recommended for production** — you own the CNPG Cluster (backup, certs, bootstrap) |
| `spec.database.cluster` | Quick owned Cluster with a small knobs surface (instances, storage, resources, affinity). **Does not** expose CNPG backup |
| `spec.database.connectionSecretRef` | Escape hatch to arbitrary Postgres (degraded: no Cluster manage, profiles, or backup pause) |

Owned-cluster backup knobs on `NominatimInstance` are **intentionally deferred**. If you need backup/restore today, author a CNPG Cluster with backup configured and attach it with `clusterRef`.

## Example: backup-enabled Cluster + clusterRef

Hand-author a CNPG Cluster (backup stanza is illustrative — match your CNPG version and object store):

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: nominatim-pg
  namespace: nominatim
spec:
  instances: 2
  imageName: ghcr.io/cloudnative-pg/postgresql:16
  bootstrap:
    initdb:
      database: nominatim
      owner: nominatim
      # PostGIS / extensions: follow CNPG + Nominatim needs for your image
  storage:
    size: 100Gi
  backup:
    barmanObjectStore:
      destinationPath: s3://my-bucket/nominatim-pg
      # wal / s3Credentials / … per CNPG docs
    retentionPolicy: "30d"
```

Attach the instance:

```yaml
apiVersion: nominatim.zebernst.dev/v1alpha1
kind: NominatimInstance
metadata:
  name: nominatim
  namespace: nominatim
spec:
  project:
    volume:
      claimName: nominatim-project
  regions:
    - europe/monaco
  database:
    clusterRef:
      name: nominatim-pg
      # connectionSecretRef:
      #   name: nominatim-pg-app   # default if omitted
  api:
    replicas: 1
```

Credentials default to the Cluster’s application Secret (`{cluster}-app`). Override with `clusterRef.connectionSecretRef` when needed.

When the instance is CNPG-attached, `pauseBackupsDuringOperations` (default `WriteHeavy`) can pause continuous backup around Bootstrap / AddRegions / Rebuild / Migrate / Freeze so backups do not race those Jobs.

## Restore

Restore is a CNPG concern: recover or bootstrap a Cluster from backup using CNPG’s documented recovery flows, then point `clusterRef` at the restored Cluster (or keep the same name after in-place recovery). The operator does not invent a separate restore CR.

After restore, confirm `NominatimInstance` status shows a healthy database attachment and that search still works before re-enabling scheduled Updates.

## Freeze before logical dumps

If you take a logical dump (`pg_dump` / similar) outside CNPG continuous backup, [upstream Nominatim](https://nominatim.org/release-docs/latest/) recommends freezing (or otherwise stopping updates) so the dump is consistent — dynamic update tables and in-flight replication make live dumps risky.

In this operator, run a `Freeze` Operation when you intentionally want a serve-only database with update structures dropped. That is stronger than “pause updates”: after Freeze, Update / AddRegions that need those structures will fail. See [Operations](operations.md).

For a dump while still planning to update later, stop Update/CatchUp (disable `spec.updates` or wait for idle), avoid write-heavy Operations, then dump — prefer CNPG physical backup when you can.

## Related

- [Configuration](configuration.md) — attach mode field reference
- [Concepts](concepts.md) — degraded mode summary
- Sample Instance comments in [`config/samples/nominatim_v1alpha1_nominatiminstance.yaml`](../config/samples/nominatim_v1alpha1_nominatiminstance.yaml)
