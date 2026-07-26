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
| `Reimport` | `scripts/reimport.sh` | Clear markers + Bootstrap (`NOMINATIM_REIMPORT_CONFIRM=1`) |
| `Update` | `scripts/update.sh` | Geofabrik diffs via `pyosmium-get-changes` |
| `CatchUp` | `scripts/catch-up.sh` | Update loop until idle |

These are thin phases invoked by `NominatimOperation` Jobs. Orchestration (mutex, scale API, pause backups, empty DB for Reimport) stays in the operator — not in bash.

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
