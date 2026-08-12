# Container images

Own images for Nominatim on Kubernetes — **not** based on `mediagis/nominatim`.

Operator architecture and day-2 behavior: [docs/concepts.md](../docs/concepts.md), [docs/operations.md](../docs/operations.md).

| Image | Dockerfile | Registry |
|-------|------------|----------|
| Operator | `images/operator/Dockerfile` | `ghcr.io/zebernst/nominatim-operator` |
| API | `images/api/Dockerfile` | `ghcr.io/zebernst/nominatim-api` |
| Worker | `images/worker/Dockerfile` | `ghcr.io/zebernst/nominatim-worker` |
| UI | `images/ui/Dockerfile` | `ghcr.io/zebernst/nominatim-ui` |

## Packaging

API and worker use **Ubuntu 24.04** and install Nominatim from **PyPI** (`nominatim-db` / `nominatim-api`), matching the [upstream Ubuntu 24 install docs](https://nominatim.org/release-docs/latest/admin/Install-on-Ubuntu-24/). Worker also installs `osm2pgsql` and `pyosmium`.

```bash
docker build -f images/api/Dockerfile --build-arg NOMINATIM_VERSION=5.3.2 -t ghcr.io/zebernst/nominatim-api:dev .
docker build -f images/ui/Dockerfile --build-arg NOMINATIM_UI_VERSION=3.12.0 -t ghcr.io/zebernst/nominatim-ui:dev .
```

## Local builds

```bash
docker build -t ghcr.io/zebernst/nominatim-operator:dev -f images/operator/Dockerfile .
docker build -t ghcr.io/zebernst/nominatim-api:dev -f images/api/Dockerfile .
docker build -t ghcr.io/zebernst/nominatim-worker:dev -f images/worker/Dockerfile .
docker build -t ghcr.io/zebernst/nominatim-ui:dev -f images/ui/Dockerfile .
# or: make docker-build-ui
```

CI (`.github/workflows/release.yaml`) builds and pushes all four to GHCR on `main` and on version tags.

## UI

Static HTML/JS from [osm-search/nominatim-ui](https://github.com/osm-search/nominatim-ui/releases). At start, `images/ui/entrypoint.sh` writes `theme/config.theme.js` from `NOMINATIM_API_ENDPOINT` (browser-reachable API base URL; trailing slash added). When unset, defaults to `/` (same-origin proxy).

The operator sets this from the first `spec.api.route.hostnames` entry when present (`https://<hostname>/`). Override via `spec.ui.podSpec` env if needed. Omit `spec.ui` or set `enabled: false` for API/database-only.

## Database env (API and worker)

Neither image runs PostgreSQL. They expect:

| Variable | Purpose |
|----------|---------|
| `NOMINATIM_DATABASE_DSN` | Nominatim connection string (required) |
| `PGHOST` / `PGDATABASE` / `PGUSER` / `PGPASSWORD` | libpq / `pg_isready` / `psql` (required) |

Prefer process env / Secrets — do not bake password-bearing DSNs into the project PVC `.env`.

## Worker Operation types

The worker entrypoint dispatches on `OPERATION_TYPE` (or the first CLI arg). Scripts under `images/worker/scripts/`:

| Type | Script |
|------|--------|
| Bootstrap | `bootstrap.sh` |
| AddRegions | `add-regions.sh` |
| Rebuild | `rebuild.sh` |
| Update | `update.sh` |
| CatchUp | `catch-up.sh` |
| Refresh | `refresh.sh` |
| Migrate | `migrate.sh` |
| Freeze | `freeze.sh` |

When to use each type, concurrency, and status: [docs/operations.md](../docs/operations.md).

### Worker shell tests

```bash
# Requires bats-core + shellcheck (brew install bats-core shellcheck).
make test-worker-shell
make shellcheck-worker
```

Stubs under `images/worker/scripts/test/stubs/` fake `psql` / `pg_isready` without Postgres.

### Manual worker / API runs

```bash
docker run --rm \
  -e OPERATION_TYPE=Bootstrap \
  -e NOMINATIM_DATABASE_DSN=postgresql://… \
  -e PGHOST=… -e PGDATABASE=nominatim -e PGUSER=… -e PGPASSWORD=… \
  -e PBF_URL=https://download.geofabrik.de/europe/monaco-latest.osm.pbf \
  -v nominatim-project:/nominatim \
  -v nominatim-staging:/import-staging \
  ghcr.io/zebernst/nominatim-worker:dev

docker run --rm -p 8080:8080 \
  -e NOMINATIM_DATABASE_DSN=postgresql://… \
  -e PGHOST=… -e PGDATABASE=nominatim -e PGUSER=… -e PGPASSWORD=… \
  ghcr.io/zebernst/nominatim-api:dev
```

The API is read-only serving: env/Secrets only, no project/flatnode mounts, refuses to start if `public.placex` is missing. Default probes: `GET /status`. `spec.api.gunicornWorkers` sets `GUNICORN_WORKERS`; when unset the entrypoint prefers cgroup CPU quota over `nproc`.
