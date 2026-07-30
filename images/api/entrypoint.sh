#!/usr/bin/env bash
# API entrypoint: wait for external Postgres, serve gunicorn.
# Read-only serving plane (nominatim-5et.35.1): config from process env / Secrets only.
# No project/flatnode PVC, no .env seed, no PVC import markers, no nominatim refresh.
set -euo pipefail

PROJECT_DIR="${PROJECT_DIR:-/nominatim}"

: "${NOMINATIM_DATABASE_DSN:?NOMINATIM_DATABASE_DSN is required}"
: "${PGHOST:?PGHOST is required}"
: "${PGDATABASE:?PGDATABASE is required}"
: "${PGUSER:?PGUSER is required}"
: "${PGPASSWORD:?PGPASSWORD is required}"

export NOMINATIM_DATABASE_DSN PGHOST PGDATABASE PGUSER PGPASSWORD

# Ephemeral workdir (emptyDir); never mutate shared import state.
mkdir -p "${PROJECT_DIR}"

echo "[nominatim-api] Waiting for PostgreSQL at ${PGHOST}"
until pg_isready -h "${PGHOST}" -d "${PGDATABASE}" -U "${PGUSER}" -q; do
  sleep 2
done

echo "[nominatim-api] Verifying nominatim schema on ${PGHOST}/${PGDATABASE}"
has_placex="$(psql -h "${PGHOST}" -d "${PGDATABASE}" -U "${PGUSER}" -Atqc \
  "SELECT EXISTS (
     SELECT 1
     FROM pg_catalog.pg_class c
     JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public' AND c.relname = 'placex' AND c.relkind = 'r'
   );")"
if [ "${has_placex}" != "t" ]; then
  echo "[nominatim-api] ERROR: public.placex missing; refusing to start (Bootstrap incomplete)" >&2
  exit 1
fi

if [ -z "${GUNICORN_WORKERS:-}" ]; then
  GUNICORN_WORKERS="$(nproc)"
fi

echo "[nominatim-api] Starting Gunicorn on :8080 with ${GUNICORN_WORKERS} workers"
cd "${PROJECT_DIR}"
exec gunicorn \
  --bind :8080 \
  --workers "${GUNICORN_WORKERS}" \
  --enable-stdio-inheritance \
  --worker-class uvicorn.workers.UvicornWorker \
  "nominatim_api.server.falcon.server:run_wsgi()"
