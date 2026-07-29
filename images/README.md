# Container images

Own images for Nominatim on Kubernetes — **not** based on `mediagis/nominatim`.

| Image | Dockerfile | Registry |
|-------|------------|----------|
| Operator (kubebuilder manager) | `Dockerfile` | `ghcr.io/zebernst/nominatim-operator` |
| API (gunicorn + nominatim-api) | `Dockerfile.api` | `ghcr.io/zebernst/nominatim-api` |
| Worker (nominatim CLI + Operation phases) | `Dockerfile.worker` | `ghcr.io/zebernst/nominatim-worker` |

## Base / packaging

API and worker use **Ubuntu 24.04** and install Nominatim from **PyPI** (`nominatim-db` / `nominatim-api`), matching the [upstream Ubuntu 24 install docs](https://nominatim.org/release-docs/latest/admin/Install-on-Ubuntu-24/). Worker also installs `osm2pgsql` from apt and `pyosmium` for Geofabrik diffs.

Override version at build time:

```bash
docker build -f Dockerfile.api --build-arg NOMINATIM_VERSION=5.3.2 -t ghcr.io/zebernst/nominatim-api:dev .
```

## Local builds

```bash
# Operator
docker build -t ghcr.io/zebernst/nominatim-operator:dev -f Dockerfile .

# API
docker build -t ghcr.io/zebernst/nominatim-api:dev -f Dockerfile.api .

# Worker
docker build -t ghcr.io/zebernst/nominatim-worker:dev -f Dockerfile.worker .
```

CI (`.github/workflows/images.yml`) builds and pushes all three to GHCR on pushes to `main` and on version tags.

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
| `Reimport` | `scripts/reimport.sh` | Clear markers + Bootstrap (`NOMINATIM_REIMPORT_CONFIRM=1`). Operator drops/recreates the owned CNPG Database CR first so extensions are reinstalled on an empty DB. |
| `Update` | `scripts/update.sh` | Geofabrik diffs via `pyosmium-get-changes` |
| `CatchUp` | `scripts/catch-up.sh` | Update loop until idle |

These are thin phases invoked by `NominatimOperation` Jobs. Orchestration (mutex, scale API, pause backups, empty DB for Reimport) stays in the operator — not in bash.

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

`NOMINATIM_REGIONS` (derived by the operator from `op.Spec.Regions`, falling back to `parent.Spec.Regions` when the Operation sets none) is the **exact** import set for an AddRegions Job: `add-regions.sh` imports every region listed there that is not already recorded in `imported-regions.txt`, then re-indexes once if anything changed. There is no per-run cap or "only this region" filter in the worker — chunking (e.g. one missing region per drift-driven Operation) is entirely the operator's responsibility. A manual AddRegions with multiple regions in its Spec imports all of them in one Job.

`add-regions.sh` still requires `IMPORT_FINISHED` (Bootstrap must have completed) and a non-empty `NOMINATIM_REGIONS` as bash-level belts, even though the operator also enforces these preconditions before creating the Job.

### Bootstrap gate: PBF-only vs regions mode

The operator only requires Bootstrap-done before creating AddRegions / Update / CatchUp Jobs when the parent `Nominatim` is in **regions mode** (`Spec.Regions` non-empty). In that mode, Bootstrap-done means `Status.Regions` is non-empty **or** a peer Bootstrap Operation for the same parent has already `Succeeded` (covers the brief window before status is persisted). **PBF-only** parents (`Spec.Regions` empty) skip this operator-side gate entirely; the worker's own `IMPORT_FINISHED` belt in `add-regions.sh` / `update.sh` remains the only guard against running before a Bootstrap has completed.

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

```bash
docker run --rm -p 8080:8080 \
  -e NOMINATIM_DATABASE_DSN=postgresql://… \
  -e PGHOST=… -e PGDATABASE=nominatim -e PGUSER=… -e PGPASSWORD=… \
  -v nominatim-project:/nominatim \
  ghcr.io/zebernst/nominatim-api:dev
```
