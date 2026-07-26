#!/usr/bin/env bash
# API entrypoint: wait for external Postgres, link staging, serve gunicorn.
# Codifies the serving path from homelab start-external.sh (no local Postgres / no mediagis init).
set -euo pipefail

PROJECT_DIR="${PROJECT_DIR:-/nominatim}"
STAGING_DIR="${IMPORT_STAGING:-/import-staging}"
IMPORT_FINISHED="${IMPORT_FINISHED:-${PROJECT_DIR}/import-finished}"
ENV_DEFAULTS="${ENV_DEFAULTS:-/opt/nominatim/env.defaults}"

: "${NOMINATIM_DATABASE_DSN:?NOMINATIM_DATABASE_DSN is required}"
: "${PGHOST:?PGHOST is required}"
: "${PGDATABASE:?PGDATABASE is required}"
: "${PGUSER:?PGUSER is required}"
: "${PGPASSWORD:?PGPASSWORD is required}"

export NOMINATIM_DATABASE_DSN PGHOST PGDATABASE PGUSER PGPASSWORD

mkdir -p "${STAGING_DIR}" "${PROJECT_DIR}"

link_staging() {
  local name="$1"
  local link="${PROJECT_DIR}/${name}"
  local target="${STAGING_DIR}/${name}"

  if [ -e "${link}" ] && [ ! -L "${link}" ]; then
    echo "[nominatim-api] Removing stale project artifact: ${link}"
    rm -f "${link}"
  fi
  ln -sfn "${target}" "${link}"
}

link_staging "data.osm.pbf"
link_staging "wikimedia-importance.csv.gz"
link_staging "us_postcodes.csv.gz"
link_staging "secondary_importance.sql.gz"

# Prefer process env over any durable .env DSN — never write the password-bearing DSN to the PVC.
if [ ! -f "${PROJECT_DIR}/.env" ]; then
  echo "[nominatim-api] Seeding ${PROJECT_DIR}/.env from ${ENV_DEFAULTS}"
  cp "${ENV_DEFAULTS}" "${PROJECT_DIR}/.env"
fi

ENV_FILE="${PROJECT_DIR}/.env"
IMPORT_STYLE="${IMPORT_STYLE:-extratags}"
if grep -qE '^NOMINATIM_IMPORT_STYLE=' "${ENV_FILE}"; then
  sed -i "s|^NOMINATIM_IMPORT_STYLE=.*|NOMINATIM_IMPORT_STYLE=${IMPORT_STYLE}|" "${ENV_FILE}"
fi

if [ -n "${NOMINATIM_FLATNODE_FILE:-}" ]; then
  if grep -qE '^NOMINATIM_FLATNODE_FILE=' "${ENV_FILE}"; then
    sed -i "s|^NOMINATIM_FLATNODE_FILE=.*|NOMINATIM_FLATNODE_FILE=${NOMINATIM_FLATNODE_FILE}|" "${ENV_FILE}"
  else
    printf 'NOMINATIM_FLATNODE_FILE=%s\n' "${NOMINATIM_FLATNODE_FILE}" >> "${ENV_FILE}"
  fi
fi

echo "[nominatim-api] Waiting for PostgreSQL at ${PGHOST}"
until pg_isready -h "${PGHOST}" -d "${PGDATABASE}" -U "${PGUSER}" -q; do
  sleep 2
done

# Fail closed: only seed import-finished when placex exists (imported DB).
if [ ! -f "${IMPORT_FINISHED}" ]; then
  echo "[nominatim-api] Verifying nominatim schema on ${PGHOST}/${PGDATABASE}"
  has_placex="$(psql -h "${PGHOST}" -d "${PGDATABASE}" -U "${PGUSER}" -Atqc \
    "SELECT EXISTS (
       SELECT 1
       FROM pg_catalog.pg_class c
       JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
       WHERE n.nspname = 'public' AND c.relname = 'placex' AND c.relkind = 'r'
     );")"
  if [ "${has_placex}" != "t" ]; then
    echo "[nominatim-api] ERROR: public.placex missing; refusing to seed import-finished" >&2
    exit 1
  fi
  echo "[nominatim-api] Seeding ${IMPORT_FINISHED}"
  touch "${IMPORT_FINISHED}"
fi

if [ "${NOMINATIM_REFRESH_FUNCTIONS:-true}" = "true" ] || [ "${NOMINATIM_REFRESH_FUNCTIONS:-true}" = "1" ]; then
  echo "[nominatim-api] Refreshing SQL functions against external DB"
  nominatim refresh --functions --project-dir "${PROJECT_DIR}"
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
