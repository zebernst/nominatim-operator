# Container images

Own images for Nominatim on Kubernetes — **not** based on `mediagis/nominatim`.

| Image | Dockerfile | Registry | Plane |
|-------|------------|----------|-------|
| Operator (kubebuilder manager) | `operator.Dockerfile` | `ghcr.io/zebernst/nominatim-operator` | Control |
| API (gunicorn + nominatim-api) | `api.Dockerfile` | `ghcr.io/zebernst/nominatim-api` | Serving |
| Worker (nominatim CLI + Operation phases) | `worker.Dockerfile` | `ghcr.io/zebernst/nominatim-worker` | Data / write |

Root operator architecture (planes, status SoT, replica/volume notes): see the repository [README](../README.md).

## Planes and volumes

| Plane | Image / process | Mounts | Talks to Kubernetes API? |
|-------|-----------------|--------|---------------------------|
| **Control** | operator | none of the instance project/flatnode | yes (reconcile) |
| **Serving** | API (+ optional UI) | emptyDir workdir only | no |
| **Data / write** | worker Operation Jobs | project (+ optional flatnode), staging | no |
| **Observation** | short-lived sequence probe Job (worker image, operator-owned) | project **read-only** | ConfigMaps only (dedicated SA) |

| Volume | Access mode (typical) | Serving API | Worker Jobs | Sequence probe |
|--------|----------------------|-------------|-------------|----------------|
| project PVC | RWO | no | read-write | read-only |
| flatnode PVC | RWO | no | read-write when `spec.flatnode` set | no |
| staging PVC | RWO (per Operation) | no | yes | no |
| API workdir | emptyDir | yes | no | no |

**Multi-replica API** (`spec.api.replicas > 1`) is supported for serving because the API is stateless against Postgres. It does **not** require RWX project or flatnode volumes — those stay single-writer for Jobs. Shared RWO flatnode across API replicas is never the design and is not mounted on the serving plane.

## Base / packaging

API and worker use **Ubuntu 24.04** and install Nominatim from **PyPI** (`nominatim-db` / `nominatim-api`), matching the [upstream Ubuntu 24 install docs](https://nominatim.org/release-docs/latest/admin/Install-on-Ubuntu-24/). Worker also installs `osm2pgsql` from apt and `pyosmium` for Geofabrik diffs.

Override version at build time:

```bash
docker build -f api.Dockerfile --build-arg NOMINATIM_VERSION=5.3.2 -t ghcr.io/zebernst/nominatim-api:dev .
```

## Local builds

```bash
# Operator
docker build -t ghcr.io/zebernst/nominatim-operator:dev -f operator.Dockerfile .

# API
docker build -t ghcr.io/zebernst/nominatim-api:dev -f api.Dockerfile .

# Worker
docker build -t ghcr.io/zebernst/nominatim-worker:dev -f worker.Dockerfile .
```

CI (`.github/workflows/release.yaml`) builds and pushes all three to GHCR on pushes to `main` and on version tags.

## External Postgres

API and worker **do not** run PostgreSQL. They expect an external database (typically CNPG) via:

| Variable | Purpose |
|----------|---------|
| `NOMINATIM_DATABASE_DSN` | Nominatim connection string (required) |
| `PGHOST` / `PGDATABASE` / `PGUSER` / `PGPASSWORD` | libpq / `pg_isready` / `psql` (required) |

Do not bake password-bearing DSNs into the project PVC `.env`; prefer process env / Secrets.

## Worker Operation phases

The worker entrypoint dispatches on `OPERATION_TYPE` (or the first CLI arg):

| Type | Script | Role |
|------|--------|------|
| `Bootstrap` | `scripts/bootstrap.sh` | Fresh or resumed `nominatim import` |
| `AddRegions` | `scripts/add-regions.sh` | `nominatim add-data` for new Geofabrik regions |
| `Rebuild` | `scripts/rebuild.sh` | Clear markers + Bootstrap (`NOMINATIM_REBUILD_CONFIRM=1`). Operator drops/recreates the owned CNPG Database CR first so extensions are reinstalled on an empty DB. |
| `Update` | `scripts/update.sh` | Geofabrik diffs via `pyosmium-get-changes` |
| `CatchUp` | `scripts/catch-up.sh` | Update loop until idle |
| `Refresh` | `scripts/refresh.sh` | `nominatim refresh` admin tasks (default: `--postcodes --word-counts --functions --importance`; override with `NOMINATIM_REFRESH_TASKS`) |
| `Migrate` | `scripts/migrate.sh` | `nominatim admin --migrate` after rolling the worker image to a newer `nominatim-db` (then roll API). Stop Update/CatchUp first — Migrate is write-heavy. Prefer `suspendDuringOperations: WriteHeavy` (or All) while migrating; upstream advises not serving during upgrades. |
| `Freeze` | `scripts/freeze.sh` | `nominatim freeze` — drop tables kept only for OSM diffs (same as import `--no-updates`). Serving continues; Update/AddRegions/aux imports that need update structures will fail afterward. Do not Freeze before TIGER/aux data you still plan to load. |

These are thin phases invoked by `NominatimOperation` Jobs. Orchestration (mutex, scale API, pause backups, empty DB for Rebuild) stays in the operator — not in bash.

**Write-plane mutex:** the operator serializes conflicting Operations via parent `status.activeOperationRefs` (retry-on-conflict CAS), not Kubernetes Leases. A peer that is already `Running` or has a `JobRef` causes terminal `Conflict`. Two fresh write-heavy peers in a creation race requeue (lexicographically smaller name wins) instead of both failing.

`Refresh` is not write-heavy for the Operation mutex (it still conflicts with an active Bootstrap / AddRegions / Rebuild / Migrate / Freeze peer). Do not run it in parallel with Update / CatchUp / AddRegions — upstream Nominatim forbids parallel refresh with add-data / replication-style updates. Staging PVC is still created for Job shape consistency even though Refresh does not download extracts.

`Migrate` and `Freeze` are write-heavy (mutex with Update/CatchUp and other write-heavy peers; default `pauseBackupsDuringOperations: WriteHeavy` applies).

### Import-complete / ready-to-serve (Kubernetes source of truth)

| Concern | Source of truth | Not SoT |
|---------|-----------------|--------|
| Which regions are imported | `status.regions` (synced from Succeeded Bootstrap / AddRegions / Rebuild) | `imported-regions.txt` on the project PVC |
| Bootstrap done (regions mode) | `status.regions` non-empty **or** a peer Bootstrap Operation `Succeeded` | `import-finished` on the project PVC |
| API/UI may exist | `servingWorkloadsAllowed`: no desired regions, or `status.regions` populated | PVC markers |
| Day-2 Jobs (AddRegions / Update / CatchUp) | Operator `BootstrapIncomplete` gate via `bootstrapComplete` | Worker file checks alone |
| Day-2 admin (`Refresh`) | Worker `require_bootstrap_ready` (no region gate; Refresh does not need `NOMINATIM_REGIONS`) | API boot path |
| Schema / serve-only (`Migrate` / `Freeze`) | Worker `require_bootstrap_ready`; write-heavy mutex (stop updates first) | Image bumps for Migrate are outside the Job |

Project PVC files remain **worker-local resume bookmarks** (Bootstrap still writes `import-finished`; Rebuild clears markers before re-bootstrap). Workers call `require_bootstrap_ready`, which heals a missing marker when the Nominatim schema is already ready and only fails when both the marker and schema are absent — last-resort, not cluster coordination.

**PBF-only** parents (`Spec.Regions` empty) skip the operator Bootstrap gate; the worker schema/marker belt is then the only guard before AddRegions/Update.

### Worker script tests

Resume / region parsing helpers and phase-scoped `prepare_db` / `prepare_import` in `scripts/common.sh` have bats coverage under `scripts/test/`:

```bash
# Requires bats-core + shellcheck on PATH (brew install bats-core shellcheck).
make test-worker-shell
make shellcheck-worker
```

CI runs both in the **Worker shell** job. Stubs under `scripts/test/stubs/` fake `psql` / `pg_isready` so `detect_continue_at` can be exercised without Postgres.

### Sequence state / update lag

After a Succeeded Bootstrap / AddRegions / Rebuild / Update / CatchUp Operation, the NominatimInstance reconciler creates a short-lived **sequence probe** Job (operator-owned) that mounts the project PVC read-only, runs `scripts/report-sequence.sh`, and merge-patches ConfigMap `{name}-sequence`. The reconciler copies `report.json` into `status.regions[].sequenceState` (`sequenceNumber@timestamp`) and `aux-data.json` into `status.auxData` (Wikipedia importance / postcode file presence). Worker Operation scripts do **not** talk to the Kubernetes API.

### Auxiliary data (Wikipedia importance, postcodes)

`spec.auxData` toggles optional downloads from [nominatim.org/data](https://nominatim.org/data/) into the operation staging PVC during Bootstrap (and Refresh when enabled):

| Field | Staging / project file | Upstream URL |
|-------|------------------------|--------------|
| `wikimediaImportance` | `wikimedia-importance.csv.gz` | `…/wikimedia-importance.csv.gz` |
| `secondaryImportance` | `secondary_importance.sql.gz` | `…/wikimedia-secondary-importance.sql.gz` |
| `usPostcodes` | `us_postcodes.csv.gz` | `…/us_postcodes.csv.gz` |

The worker downloads into staging (resume-friendly), then **copies** files onto the project PVC so import and the sequence probe see durable files (not broken staging symlinks). `Refresh` appends `--wiki-data` / `--secondary-importance` when those files are present for post-import backfill.

This is per-region **pyosmium / Geofabrik** cursor state — not Nominatim `NOMINATIM_REPLICATION_*` lag.

### Bootstrap multi-region: one import, multiple `--osm-file`

When `NOMINATIM_REGIONS` lists more than one Geofabrik path (e.g. `europe/monaco,europe/andorra`),
`bootstrap.sh` downloads **every** extract and passes multiple `--osm-file` flags to a single
`nominatim import` — matching [upstream Advanced Installations](https://nominatim.org/release-docs/latest/admin/Advanced-Installations/).
Do **not** use `add-data` during Bootstrap; it is far slower than initial import. Day-2 growth
of the coverage set is `AddRegions` only.

The CI import e2e (`make test-e2e-import`) bootstraps monaco+andorra and probes both countries
with `countrycodes=` so a regression that only imported `regions[0]` while marking every Spec
region `Imported` fails the search assertions even if `status.regions` looks complete.

### AddRegions Spec contract

`NOMINATIM_REGIONS` (derived by the operator from `op.Spec.Regions`, falling back to `parent.Spec.Regions` when the Operation sets none) is the **exact** import set for an AddRegions Job: `add-regions.sh` imports every region listed there that is not already recorded in `imported-regions.txt` (local bookmark; cluster SoT is `status.regions`), then re-indexes once if anything changed. There is no per-run cap or "only this region" filter in the worker — chunking (e.g. one missing region per drift-driven Operation) is entirely the operator's responsibility. A manual AddRegions with multiple regions in its Spec imports all of them in one Job.

`add-regions.sh` / `update.sh` call `require_bootstrap_ready` as a last-resort belt; the operator's Bootstrap-done gate is authoritative in regions mode (see above).

### Bootstrap gate: PBF-only vs regions mode

See **Import-complete / ready-to-serve** above. Regions-mode parents use `status.regions` / Succeeded Bootstrap; PBF-only parents rely on the worker belt.

### Postgres readiness: CNPG gate is primary, Job wait is last-mile

Before creating any `NominatimOperation` Job, the operator checks CNPG Cluster/Database readiness (`cnpgClusterReadyForJobs`) and requeues rather than creating a Job until the owned or referenced CNPG Cluster is `Ready` (and, for owned Databases, applied). This is the **primary** readiness gate — by the time a Job starts, Postgres is expected to already be accepting connections.

`wait_for_postgres` in `scripts/common.sh` is a **last-mile** check only: it covers the brief gap between Job scheduling and Postgres accepting connections (e.g. pod startup ordering), not a substitute for the operator's gate. It defaults to 15 attempts at a 2s sleep (~30s total) and can be tuned via `NOMINATIM_PG_WAIT_ATTEMPTS` if a deployment's last-mile gap is larger than the default covers.

```bash
docker run --rm \
  -e OPERATION_TYPE=Bootstrap \
  -e NOMINATIM_DATABASE_DSN=postgresql://… \
  -e PGHOST=… -e PGDATABASE=nominatim -e PGUSER=… -e PGPASSWORD=… \
  -e PBF_URL=https://download.geofabrik.de/europe/monaco-latest.osm.pbf \
  -v nominatim-project:/nominatim \
  -v nominatim-staging:/import-staging \
  ghcr.io/zebernst/nominatim-worker:dev
```

## API serving

The API is a **read-only serving plane**: it talks to Postgres via Secrets / env
(`NOMINATIM_DATABASE_DSN`, `PG*`, plus `spec.nominatim` → `NOMINATIM_*`). It does
**not** mount the project or flatnode PVCs (those are for worker Jobs / osm2pgsql
on the data plane). The Deployment mounts an ephemeral emptyDir at `/nominatim`
only as gunicorn's working directory. Import-complete is gated by the operator
(`status.regions`); the entrypoint refuses to start if `public.placex` is missing.

Admin refresh / migrate / freeze run as `NominatimOperation` types when
implemented — never as side effects of API container start. Scale API pods with
`spec.api.replicas` freely relative to project/flatnode access modes (see
**Planes and volumes** above).

Default Deployment probes use **GET `/status`** (startup, readiness, and liveness).
`spec.api.podSpec` may override them. `spec.api.gunicornWorkers` sets
`GUNICORN_WORKERS`; when unset the entrypoint prefers cgroup CPU quota over
`nproc` (nominatim-5et.14). Runtime knobs under `spec.nominatim.api` map to
`NOMINATIM_API_POOL_SIZE`, `NOMINATIM_QUERY_TIMEOUT`, `NOMINATIM_REQUEST_TIMEOUT`,
`NOMINATIM_DEFAULT_LANGUAGE`, and `NOMINATIM_CORS_NOACCESSCONTROL`.

```bash
docker run --rm -p 8080:8080 \
  -e NOMINATIM_DATABASE_DSN=postgresql://… \
  -e PGHOST=… -e PGDATABASE=nominatim -e PGUSER=… -e PGPASSWORD=… \
  ghcr.io/zebernst/nominatim-api:dev
```
