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

## API and UI

- `spec.api.replicas` — may be >1 (stateless serving).
- `spec.api.suspendDuringOperations` — whether to scale the API down during day-2 write work (`Never` keeps it up for AddRegions/Update; Rebuild always quiesces the API).
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

## CNPG and Gateway prerequisites

Install CloudNativePG CRDs before creating instances that use `database.cluster` or `database.clusterRef`. Install Gateway API CRDs before using routes. The operator chart does not install those third-party CRDs.
