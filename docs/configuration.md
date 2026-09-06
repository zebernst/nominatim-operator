# Configuration

Common `NominatimInstance` settings. Exhaustive schema: CRDs under `config/crd/bases/` or `kubectl explain nominatiminstance.spec`.

API group: **`nominatim.zebernst.dev`**. Donating this project to another org (for example osm-search) would create a breaking change.

## Minimal shape

```yaml
apiVersion: nominatim.zebernst.dev/v1alpha1
kind: NominatimInstance
metadata:
  name: nominatim
spec:
  project:
    volume:
      claimName: nominatim-project
  regions:
    - europe/monaco
  database:
    clusterRef:
      name: nominatim-pg
  api:
    replicas: 1
```

Samples: [`config/samples/`](../config/samples/).

Validate:

```bash
kubectl apply --dry-run=server -f config/samples/nominatim_v1alpha1_nominatiminstance.yaml
kubectl kustomize config/samples | kubectl apply --dry-run=server -f -
```

## Images

Defaults point at `ghcr.io/zebernst/nominatim-{api,worker,ui,operator}`. Override per workload with `spec.api.image`, `spec.worker.image`, `spec.ui.image` (repository/tag/pullPolicy). Build notes: [`images/README.md`](../images/README.md).

## Regions and updates

| Field | Role |
|-------|------|
| `spec.regions` | Desired Geofabrik-style paths |
| `spec.regionChangePolicy` | How new regions are applied (`AddData` default → AddRegions Operations; `Rebuild` forces rebuild policy) |
| `spec.updates.enabled` / `schedule` | Controller-driven Update Operations (cron expression; no CronJob object) |

Removing a region from `spec.regions` does **not** delete database data. Shrinking coverage requires a Rebuild (or putting the region back).

Optional `spec.auxData` toggles Wikipedia importance / postcode downloads during Bootstrap (and Refresh when enabled).

Typed Nominatim settings live under `spec.nominatim` (import style, tokenizer, languages, replication, API runtime knobs). Prefer that over stuffing `NOMINATIM_*` into `podSpec`. Import style / tokenizer seal after Bootstrap — changing them needs Rebuild.

## Database attach modes

Exactly one of `cluster`, `clusterRef`, or `connectionSecretRef`:

| Mode | Spec | Operator behavior |
|------|------|-------------------|
| Owned cluster | `database.cluster` | Creates a basic CNPG Cluster (instances, storage, resources, affinity). Not a full CNPG ClusterSpec — **no** backup/certificate/custom bootstrap surface |
| Attached cluster | `database.clusterRef` | Watches an existing CNPG Cluster; default credentials Secret `{name}-app` (override with `clusterRef.connectionSecretRef`) |
| Degraded | `database.connectionSecretRef` | Any Postgres Secret; **no** Cluster manage, parameter profiles, or backup pause |

Production installs that need CNPG backup should use **`clusterRef`** and author the Cluster yourself — see [Database & backup](database.md).

`pauseBackupsDuringOperations` (default `WriteHeavy`) pauses continuous backup around write-heavy Operations when the instance is CNPG-attached.

`rebuildStrategy` (default `InPlace`) selects how Rebuild works:

| Value | Behavior |
|-------|----------|
| `InPlace` | Drop/recreate the owned application Database on the live Cluster; API quiesced |
| `BlueGreen` | Parallel owned Cluster + project volume, then cut over the API Secret (**`database.cluster` only**). Rejected for `clusterRef` / `connectionSecretRef` — for attached clusters, provision a second Cluster yourself and retarget `clusterRef` |

## API and UI

- `spec.api.replicas` — may be >1 (stateless serving).
- `spec.api.suspendDuringOperations` — whether to scale the API down during day-2 write work (`Never` keeps it up for AddRegions/Update). **InPlace** Rebuild always quiesces the API; **BlueGreen** Rebuild does not force-suspend.
- `spec.api.route` / `spec.ui.route` — Gateway API HTTPRoute parentRefs/hostnames (requires Gateway API CRDs).
- Omit `spec.ui`, or set `spec.ui.enabled: false`, for API/database-only.

Default probes use `GET /status`. Override via `spec.api.podSpec` if needed.

## GitOps

| Object | In Git / Flux? |
|--------|----------------|
| `NominatimInstance` | Yes |
| Operator HelmRelease / chart | Yes |
| `NominatimOperation` | **No** — controller-created finite Jobs; create manually with kubectl only when needed |

The Operation sample spells this out; chart README repeats the Flux warning.

## Deleting an instance

Delete the `NominatimInstance` and wait until it is **gone** before deleting the namespace (or other shared resources).

While the Instance is terminating the operator:

1. Sets `Deleting=True` and refuses new Operation work
2. Deletes child `NominatimOperation`s (and their Jobs)
3. Deletes operator-owned CNPG `Database` then `Cluster` (from `spec.database.cluster`) and waits until they disappear — this is what holds the finalizer on slow storage (for example Rook/RBD)
4. Removes `nominatim.zebernst.dev/finalizer`

`spec.project.volume.claimName` / `spec.flatnode.volume.claimName` PVCs are **not** deleted with the Instance. Clean those up yourself (or leave them for the next install). Attached `clusterRef` Clusters are also left alone.

Deleting the namespace while the Instance finalizer is still waiting on volume detach is safe for correctness, but the namespace stays `Terminating` until those PVCs finish — prefer waiting for the Instance to disappear first.

## CNPG and Gateway prerequisites

Install CloudNativePG CRDs before creating instances that use `database.cluster` or `database.clusterRef`. Install Gateway API CRDs before using routes. The operator chart does not install those third-party CRDs.
