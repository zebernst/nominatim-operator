# Concepts

How this operator models Nominatim on Kubernetes. For agent vocabulary (Avoid/prefer phrasing), see [CONTEXT.md](CONTEXT.md).

## NominatimInstance vs NominatimOperation

**NominatimInstance** is the GitOps desired state: regions, database attachment, API/UI, update schedule. Put this in Git / Flux.

**NominatimOperation** is a finite workflow (Kubernetes Job): bootstrap, add regions, update, rebuild, refresh, migrate, freeze. The instance controller creates most of these. You can also create one with `kubectl` for a one-off.

Do **not** manage `NominatimOperation` with Flux (or similar continuous apply). Operations complete and are garbage-collected; re-applying them from Git fights the controller.

## Serving vs jobs

| Role | What runs | Talks to | Mounts project / flatnode? |
|------|-----------|----------|----------------------------|
| Operator | Manager Deployment | Kubernetes API | No |
| API (+ optional UI) | Deployments | Postgres only | No — read-only serving |
| Worker | Operation Jobs | Postgres + Nominatim CLI | Yes (write path) |

The API never mounts the project or flatnode PVCs. That keeps serving stateless against Postgres, so `spec.api.replicas` may be greater than 1. Project and flatnode stay single-writer for Jobs (typically RWO).

## Regions and status

`spec.regions` lists Geofabrik-style paths you want (for example `europe/monaco`).

`status.regions` is the **cluster source of truth** for what has been imported. Files on the project PVC (`import-finished`, `imported-regions.txt`, update sequence files) are **worker-local resume bookmarks**, not coordination between pods.

After successful write operations, a short operator-owned probe Job reads sequence files and updates `status.regions[].sequenceState` (a Geofabrik/pyosmium cursor like `sequenceNumber@timestamp`). That is not Nominatim’s `NOMINATIM_REPLICATION_*` lag.

## Volumes

| Volume | Typical use |
|--------|-------------|
| Project PVC (`spec.project`) | Worker working directory: markers, local files |
| Flatnode PVC (`spec.flatnode`, optional) | osm2pgsql flatnode store during import/update Jobs |
| Staging PVC | Per-operation downloads (extracts, aux data) |
| API workdir | emptyDir only |

## Postgres attachment

Exactly one of:

- `spec.database.cluster` — operator creates a basic CNPG Cluster (limited surface; no full backup config)
- `spec.database.clusterRef` — attach an existing CNPG Cluster (preferred for production / backup)
- `spec.database.connectionSecretRef` — degraded mode: any Postgres Secret; no Cluster manage, profiles, or backup pause

Details: [Configuration](configuration.md) and [Database & backup](database.md).

## Concurrency (plain language)

Only one write-heavy operation runs at a time per instance (bootstrap, add regions, rebuild, migrate, freeze, and conflicting updates). The operator tracks this on the instance status — not with a Kubernetes Lease. If two write operations race, one waits or fails with `Conflict` rather than corrupting the database.

## Glossary (short)

| Term | Meaning |
|------|---------|
| Bootstrap | First import that builds the Nominatim database |
| AddRegions | Import additional regions without wiping existing data |
| Rebuild | Wipe and rebuild when coverage/config must be resealed |
| Update / CatchUp | Apply OSM diffs; CatchUp runs until idle |
| Freeze | Drop diff-update tables; serving continues, further OSM updates stop |
| Project | Durable Nominatim working directory on a PVC |
| Flatnode | Optional osm2pgsql flatnode volume (jobs only) |

Full agent-oriented vocabulary: [CONTEXT.md](CONTEXT.md).
