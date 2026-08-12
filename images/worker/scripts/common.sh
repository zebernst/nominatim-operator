#!/usr/bin/env bash
# Shared helpers for worker Operation phases. Keep thin: wait, link, run nominatim CLI.
# shellcheck shell=bash

PROJECT_DIR="${PROJECT_DIR:-/nominatim}"
STAGING_DIR="${IMPORT_STAGING:-/import-staging}"
IMPORT_FINISHED="${IMPORT_FINISHED:-${PROJECT_DIR}/import-finished}"
IMPORTED_LIST="${IMPORTED_LIST:-${PROJECT_DIR}/imported-regions.txt}"
ENV_DEFAULTS="${ENV_DEFAULTS:-/opt/nominatim/env.defaults}"
DOWNURL="${NOMINATIM_DOWNURL:-https://download.geofabrik.de}"
AUX_DATA_BASE_URL="${NOMINATIM_AUX_DATA_URL:-https://nominatim.org/data}"
CURL_USER_AGENT="${USER_AGENT:-nominatim-worker}"
THREADS="${THREADS:-$(nproc)}"
# Ubuntu packages pyosmium helpers under /usr/lib/python3-pyosmium (on PATH in the image).
PYOSMIUM_GET_CHANGES="${PYOSMIUM_GET_CHANGES:-pyosmium-get-changes}"

# Logs go to stderr so command substitutions (e.g. osmfile="$(ensure_osm_file)") stay clean.
log() { echo "[nominatim-worker] $*" >&2; }

die() { echo "[nominatim-worker] ERROR: $*" >&2; exit 1; }

truthy() {
  # Portable lowercase (macOS /bin/bash 3.2 lacks ${var,,}; worker image is bash 5).
  case "$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')" in
    true | 1 | yes | on) return 0 ;;
    *) return 1 ;;
  esac
}

require_db_env() {
  : "${NOMINATIM_DATABASE_DSN:?NOMINATIM_DATABASE_DSN is required}"
  : "${PGHOST:?PGHOST is required}"
  : "${PGDATABASE:?PGDATABASE is required}"
  : "${PGUSER:?PGUSER is required}"
  : "${PGPASSWORD:?PGPASSWORD is required}"
  export NOMINATIM_DATABASE_DSN PGHOST PGDATABASE PGUSER PGPASSWORD
}

# wait_for_postgres is a last-mile readiness check only: the operator's
# cnpgClusterReadyForJobs gate already blocks Job creation until the CNPG
# Cluster/Database is ready, so this loop only needs to cover the brief gap
# between Job scheduling and Postgres accepting connections. Default is 15
# attempts * 2s sleep = ~30s; override via NOMINATIM_PG_WAIT_ATTEMPTS.
wait_for_postgres() {
  log "Waiting for PostgreSQL at ${PGHOST}"
  local i attempts="${NOMINATIM_PG_WAIT_ATTEMPTS:-15}"
  for i in $(seq 1 "${attempts}"); do
    if pg_isready -h "${PGHOST}" -d "${PGDATABASE}" -U "${PGUSER}" -q; then
      return 0
    fi
    sleep 2
  done
  die "PostgreSQL at ${PGHOST} did not become ready"
}

link_staging_name() {
  local name="$1"
  local link="${PROJECT_DIR}/${name}"
  local target="${STAGING_DIR}/${name}"

  if [ -e "${link}" ] && [ ! -L "${link}" ]; then
    log "Removing stale project artifact: ${link}"
    rm -f "${link}"
  fi
  ln -sfn "${target}" "${link}"
}

download_aux_file() {
  local staging_name="$1"
  local url="$2"
  local target="${STAGING_DIR}/${staging_name}"

  if [ -f "${target}" ] && [ -s "${target}" ]; then
    log "Using existing aux file: ${staging_name}"
    return 0
  fi
  log "Downloading aux data ${staging_name} from ${url}"
  curl -L -A "${CURL_USER_AGENT}" --fail-with-body -C - --create-dirs \
    --connect-timeout 30 --max-time 7200 \
    -o "${target}" "${url}"
}

# Copy a staged aux file onto the project PVC as a regular file (not a symlink to
# staging). Nominatim import reads from PROJECT_DIR; the sequence probe mounts only
# the project volume, so status.auxData requires durable project copies.
materialize_aux_file_to_project() {
  local name="$1"
  local src="${STAGING_DIR}/${name}"
  local dst="${PROJECT_DIR}/${name}"

  if [ ! -f "${src}" ] || [ ! -s "${src}" ]; then
    return 0
  fi
  mkdir -p "${PROJECT_DIR}"
  if [ -L "${dst}" ] || [ ! -f "${dst}" ] || [ ! -s "${dst}" ]; then
    log "Materializing aux file into project: ${name}"
    rm -f "${dst}"
    cp -f "${src}" "${dst}"
  fi
}

# ensure_aux_data_downloads fetches enabled auxiliary datasets into IMPORT_STAGING
# (resume-friendly), then materializes them onto PROJECT_DIR. Operator sets
# NOMINATIM_AUX_* from spec.auxData; unset/false skips download.
ensure_aux_data_downloads() {
  mkdir -p "${STAGING_DIR}" "${PROJECT_DIR}"
  if truthy "${NOMINATIM_AUX_WIKIMEDIA_IMPORTANCE:-false}"; then
    download_aux_file "wikimedia-importance.csv.gz" \
      "${AUX_DATA_BASE_URL}/wikimedia-importance.csv.gz"
    materialize_aux_file_to_project "wikimedia-importance.csv.gz"
  fi
  if truthy "${NOMINATIM_AUX_SECONDARY_IMPORTANCE:-false}"; then
    download_aux_file "secondary_importance.sql.gz" \
      "${AUX_DATA_BASE_URL}/wikimedia-secondary-importance.sql.gz"
    materialize_aux_file_to_project "secondary_importance.sql.gz"
  fi
  if truthy "${NOMINATIM_AUX_US_POSTCODES:-false}"; then
    download_aux_file "us_postcodes.csv.gz" \
      "${AUX_DATA_BASE_URL}/us_postcodes.csv.gz"
    materialize_aux_file_to_project "us_postcodes.csv.gz"
  fi
}

link_staging() {
  mkdir -p "${STAGING_DIR}" "${PROJECT_DIR}"
  link_staging_name "data.osm.pbf"
}

seed_project_env() {
  local env_file="${PROJECT_DIR}/.env"
  local import_style="${IMPORT_STYLE:-extratags}"
  # Prefer explicit WEBUSER; otherwise Nominatim's default grants target (www-data),
  # which owned CNPG Clusters declare via spec.managed.roles.
  local webuser="${NOMINATIM_DATABASE_WEBUSER:-www-data}"

  if [ ! -f "${env_file}" ]; then
    log "Seeding ${env_file} from ${ENV_DEFAULTS}"
    cp "${ENV_DEFAULTS}" "${env_file}"
  fi

  if grep -qE '^NOMINATIM_IMPORT_STYLE=' "${env_file}"; then
    # -i.bak is portable across GNU sed (Ubuntu image) and BSD sed (macOS bats).
    sed -i.bak "s|^NOMINATIM_IMPORT_STYLE=.*|NOMINATIM_IMPORT_STYLE=${import_style}|" "${env_file}"
    rm -f "${env_file}.bak"
  else
    printf 'NOMINATIM_IMPORT_STYLE=%s\n' "${import_style}" >> "${env_file}"
  fi

  if grep -qE '^NOMINATIM_DATABASE_WEBUSER=' "${env_file}"; then
    sed -i.bak "s|^NOMINATIM_DATABASE_WEBUSER=.*|NOMINATIM_DATABASE_WEBUSER=${webuser}|" "${env_file}"
    rm -f "${env_file}.bak"
  else
    printf 'NOMINATIM_DATABASE_WEBUSER=%s\n' "${webuser}" >> "${env_file}"
  fi

  if [ -n "${NOMINATIM_FLATNODE_FILE:-}" ]; then
    if grep -qE '^NOMINATIM_FLATNODE_FILE=' "${env_file}"; then
      sed -i.bak "s|^NOMINATIM_FLATNODE_FILE=.*|NOMINATIM_FLATNODE_FILE=${NOMINATIM_FLATNODE_FILE}|" "${env_file}"
      rm -f "${env_file}.bak"
    else
      printf 'NOMINATIM_FLATNODE_FILE=%s\n' "${NOMINATIM_FLATNODE_FILE}" >> "${env_file}"
    fi
  fi

  if [ -n "${NOMINATIM_REPLICATION_URL:-}" ]; then
    if grep -qE '^NOMINATIM_REPLICATION_URL=' "${env_file}"; then
      sed -i.bak "s|^NOMINATIM_REPLICATION_URL=.*|NOMINATIM_REPLICATION_URL=${NOMINATIM_REPLICATION_URL}|" "${env_file}"
      rm -f "${env_file}.bak"
    else
      printf 'NOMINATIM_REPLICATION_URL=%s\n' "${NOMINATIM_REPLICATION_URL}" >> "${env_file}"
    fi
  fi
}

# Prefer gosu when root; otherwise run directly (already nominatim).
run_nominatim() {
  if [ "$(id -u)" -eq 0 ]; then
    gosu nominatim env \
      NOMINATIM_DATABASE_DSN="${NOMINATIM_DATABASE_DSN}" \
      NOMINATIM_DATABASE_WEBUSER="${NOMINATIM_DATABASE_WEBUSER:-www-data}" \
      PGHOST="${PGHOST}" \
      PGDATABASE="${PGDATABASE}" \
      PGUSER="${PGUSER}" \
      PGPASSWORD="${PGPASSWORD}" \
      nominatim "$@"
  else
    NOMINATIM_DATABASE_WEBUSER="${NOMINATIM_DATABASE_WEBUSER:-www-data}" \
      nominatim "$@"
  fi
}

fix_project_ownership() {
  if [ "$(id -u)" -ne 0 ]; then
    return 0
  fi
  local flatnode_file="${NOMINATIM_FLATNODE_FILE:-}"
  local flatnode_dir=""
  if [ -n "${flatnode_file}" ]; then
    flatnode_dir="$(dirname "${flatnode_file}")"
  fi
  chown nominatim:nominatim "${PROJECT_DIR}" 2>/dev/null || true
  if [ -n "${flatnode_dir}" ] && [ -d "${flatnode_dir}" ]; then
    find "${PROJECT_DIR}" -mindepth 1 \
      \( -path "${flatnode_dir}" -o -path "${flatnode_dir}/*" \) -prune -o \
      \( -type l ! -exec test -e {} \; \) -prune -o \
      -print0 \
      | xargs -0 -r chown nominatim:nominatim
  else
    find "${PROJECT_DIR}" -mindepth 1 \
      \( -type l ! -exec test -e {} \; \) -prune -o \
      -print0 \
      | xargs -0 -r chown nominatim:nominatim
  fi
}

psql_scalar() {
  psql -h "${PGHOST}" -d "${PGDATABASE}" -U "${PGUSER}" -Atqc "$1"
}

psql_true() {
  case "$1" in
    t | true) return 0 ;;
    *) return 1 ;;
  esac
}

# import_schema_ready is true when placex exists and Nominatim finished setup
# (database_version property). Used to reject stale import-finished markers.
import_schema_ready() {
  local has_placex has_version
  has_placex="$(psql_scalar "SELECT EXISTS (
       SELECT 1
       FROM pg_catalog.pg_class c
       JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
       WHERE n.nspname = 'public' AND c.relname = 'placex' AND c.relkind = 'r'
     )" 2>/dev/null || echo f)"
  if ! psql_true "${has_placex}"; then
    return 1
  fi
  has_version="$(psql_scalar \
    "SELECT EXISTS (SELECT 1 FROM nominatim_properties WHERE property = 'database_version')" 2>/dev/null || echo f)"
  psql_true "${has_version}"
}

# Detect nominatim import --continue stage against external DB (from resume-import knowledge).
# Prints: done|missing-db|import-from-file|load-data|indexing|db-postprocess
#
# The worker never runs createdb / CREATE DATABASE. The database (and Nominatim extensions)
# must already exist via CNPG bootstrap.initdb / Database CR / admin. An empty existing DB
# starts at import-from-file (skips nominatim's setup_database_skeleton).
detect_continue_at() {
  if [ -n "${NOMINATIM_CONTINUE_AT:-}" ]; then
    case "${NOMINATIM_CONTINUE_AT}" in
      import-from-file | load-data | indexing | db-postprocess)
        echo "${NOMINATIM_CONTINUE_AT}"
        return 0
        ;;
      *)
        die "Invalid NOMINATIM_CONTINUE_AT='${NOMINATIM_CONTINUE_AT}'"
        ;;
    esac
  fi

  # Never trust import-finished alone — a leftover marker on the project PVC must not
  # report "done" when the database was wiped / never imported.
  if [ -f "${IMPORT_FINISHED}" ] && import_schema_ready; then
    echo "done"
    return 0
  fi

  local db_exists
  db_exists="$(psql -h "${PGHOST}" -d postgres -U "${PGUSER}" -Atqc \
    "SELECT 1 FROM pg_database WHERE datname = '${PGDATABASE}'" 2>/dev/null || true)"
  if [ "${db_exists}" != "1" ]; then
    echo "missing-db"
    return 0
  fi

  local has_place has_placex_rel placex_loaded indexing_started has_pending has_version

  has_place="$(psql_scalar "SELECT COALESCE((SELECT true FROM place LIMIT 1), false)" 2>/dev/null || echo f)"
  if ! psql_true "${has_place}"; then
    echo "import-from-file"
    return 0
  fi

  has_placex_rel="$(psql_scalar "SELECT to_regclass('public.placex') IS NOT NULL" 2>/dev/null || echo f)"
  if ! psql_true "${has_placex_rel}"; then
    echo "load-data"
    return 0
  fi

  placex_loaded="$(psql_scalar "SELECT EXISTS (SELECT 1 FROM placex LIMIT 1)" 2>/dev/null || echo f)"
  if ! psql_true "${placex_loaded}"; then
    echo "load-data"
    return 0
  fi

  has_version="$(psql_scalar \
    "SELECT EXISTS (SELECT 1 FROM nominatim_properties WHERE property = 'database_version')" 2>/dev/null || echo f)"
  if psql_true "${has_version}"; then
    echo "done"
    return 0
  fi

  indexing_started="$(psql_scalar \
    "SELECT EXISTS (SELECT 1 FROM placex WHERE indexed_status = 0 LIMIT 1)" 2>/dev/null || echo f)"
  if ! psql_true "${indexing_started}"; then
    echo "load-data"
    return 0
  fi

  has_pending="$(psql_scalar \
    "SELECT EXISTS (SELECT 1 FROM placex WHERE indexed_status > 0 LIMIT 1)" 2>/dev/null || echo f)"
  if psql_true "${has_pending}"; then
    echo "indexing"
    return 0
  fi

  echo "db-postprocess"
}

ensure_osm_file() {
  local osmfile="${PBF_PATH:-${STAGING_DIR}/data.osm.pbf}"

  if [ -n "${PBF_PATH:-}" ] && [ -f "${PBF_PATH}" ] && [ -s "${PBF_PATH}" ]; then
    echo "${PBF_PATH}"
    return 0
  fi

  mkdir -p "${STAGING_DIR}"
  if [ -f "${osmfile}" ] && [ -s "${osmfile}" ]; then
    echo "${osmfile}"
    return 0
  fi

  if [ -z "${PBF_URL:-}" ]; then
    die "OSM extract missing at ${osmfile} and PBF_URL is unset"
  fi

  log "Downloading OSM extract from ${PBF_URL}"
  curl -L -A "${CURL_USER_AGENT}" --fail-with-body -C - --create-dirs \
    --connect-timeout 30 --max-time 21600 \
    -o "${osmfile}" "${PBF_URL}"
  echo "${osmfile}"
}

seed_region_state() {
  local region="$1"
  local state_dir="${PROJECT_DIR}/update/${region}"
  mkdir -p "${state_dir}"
  curl -fsSL -A "${CURL_USER_AGENT}" \
    "${DOWNURL}/${region}-updates/state.txt" > "${state_dir}/sequence.state"
}

parse_regions() {
  # Populates bash array DESIRED_REGIONS from NOMINATIM_REGIONS (comma/space separated).
  DESIRED_REGIONS=()
  local raw="${NOMINATIM_REGIONS:-}"
  if [ -z "${raw}" ]; then
    return 0
  fi
  local region
  while IFS= read -r region; do
    [ -n "${region}" ] || continue
    DESIRED_REGIONS+=("${region}")
  done < <(echo "${raw}" | tr ',' ' ' | tr ' ' '\n' | sed '/^[[:space:]]*$/d')
}

region_already_imported() {
  local region="$1"
  [ -f "${IMPORTED_LIST}" ] && grep -qxF "${region}" "${IMPORTED_LIST}"
}

# Download a Geofabrik extract and nominatim add-data it. Appends to IMPORTED_LIST and
# seeds update/<region>/sequence.state. Used by AddRegions only — never for Bootstrap.
# (Initial multi-region load must use nominatim import with multiple --osm-file flags.)
import_geofabrik_region() {
  local region="$1"
  local import_file
  import_file="${STAGING_DIR}/$(echo "${region}" | tr '/' '-')-latest.osm.pbf"
  log "Downloading and add-data importing region ${region}"
  curl -L -C - -A "${CURL_USER_AGENT}" --fail-with-body \
    "${DOWNURL}/${region}-latest.osm.pbf" -o "${import_file}"
  run_nominatim add-data --project-dir "${PROJECT_DIR}" --file "${import_file}"
  seed_region_state "${region}" || log "Warning: could not seed update state for ${region}"
  echo "${region}" >> "${IMPORTED_LIST}"
  rm -f "${import_file}"
}

region_staging_pbf() {
  local region="$1"
  echo "${STAGING_DIR}/$(echo "${region}" | tr '/' '-')-latest.osm.pbf"
}

# Populate OSM_FILES with local PBF paths for the Bootstrap/Reimport import command.
# When NOMINATIM_REGIONS is set, download every Geofabrik extract (resume-friendly).
# Otherwise fall back to ensure_osm_file (PBF_URL / PBF_PATH / data.osm.pbf).
ensure_import_osm_files() {
  OSM_FILES=()
  parse_regions
  if [ "${#DESIRED_REGIONS[@]}" -eq 0 ]; then
    OSM_FILES+=("$(ensure_osm_file)")
    return 0
  fi

  mkdir -p "${STAGING_DIR}"
  local region path url
  local i=0
  for region in "${DESIRED_REGIONS[@]}"; do
    path="$(region_staging_pbf "${region}")"
    if [ -f "${path}" ] && [ -s "${path}" ]; then
      log "Using existing extract for ${region}: ${path}"
    else
      url="${DOWNURL}/${region}-latest.osm.pbf"
      # Controller sets PBF_URL from regions[0]; prefer it when present.
      if [ "${i}" -eq 0 ] && [ -n "${PBF_URL:-}" ]; then
        url="${PBF_URL}"
      fi
      log "Downloading OSM extract for ${region} from ${url}"
      curl -L -A "${CURL_USER_AGENT}" --fail-with-body -C - --create-dirs \
        --connect-timeout 30 --max-time 21600 \
        -o "${path}" "${url}"
    fi
    OSM_FILES+=("${path}")
    i=$((i + 1))
  done
}

# Build OSM_FILE_ARGS as: --osm-file path1 --osm-file path2 ...
build_osm_file_args() {
  OSM_FILE_ARGS=()
  local f
  if [ "${#OSM_FILES[@]}" -eq 0 ]; then
    die "OSM_FILES is empty; call ensure_import_osm_files first"
  fi
  for f in "${OSM_FILES[@]}"; do
    OSM_FILE_ARGS+=(--osm-file "${f}")
  done
}

# After a successful multi-file (or single-file) import, record every desired region and
# seed Geofabrik update state. Does not call add-data.
record_imported_regions_from_spec() {
  parse_regions
  if [ "${#DESIRED_REGIONS[@]}" -eq 0 ]; then
    return 0
  fi
  : > "${IMPORTED_LIST}"
  local region
  for region in "${DESIRED_REGIONS[@]}"; do
    echo "${region}" >> "${IMPORTED_LIST}"
    seed_region_state "${region}" || log "Warning: could not seed update state for ${region}"
  done
}

# Resume/no-op guard: schema ready must already list every desired region. If an older
# Bootstrap only imported regions[0], refuse to paper over with add-data — operator should
# Reimport with a multi --osm-file Bootstrap.
assert_imported_list_complete() {
  parse_regions
  if [ "${#DESIRED_REGIONS[@]}" -eq 0 ]; then
    return 0
  fi
  local region missing=()
  for region in "${DESIRED_REGIONS[@]}"; do
    if ! region_already_imported "${region}"; then
      missing+=("${region}")
    fi
  done
  if [ "${#missing[@]}" -gt 0 ]; then
    die "Nominatim schema is ready but imported-regions.txt is missing: ${missing[*]}. Create a Reimport Operation so Bootstrap can load all regions via nominatim import --osm-file … (add-data is not used for initial multi-region load)."
  fi
}

# require_bootstrap_ready is a last-resort worker belt. Cluster source of truth for
# Bootstrap-done is Nominatim status.regions (and/or a Succeeded Bootstrap Operation);
# the operator refuses AddRegions/Update/CatchUp Jobs until that is true in regions mode.
# Locally we prefer the import-finished bookmark, heal it when the schema is ready, and
# only die when both the marker and placex/database_version are missing.
require_bootstrap_ready() {
  if [ -f "${IMPORT_FINISHED}" ]; then
    return 0
  fi
  if import_schema_ready; then
    log "Warning: ${IMPORT_FINISHED} missing but Nominatim schema is ready; healing marker (CR status.regions is Bootstrap-done source of truth)"
    touch "${IMPORT_FINISHED}"
    return 0
  fi
  die "Import not finished (${IMPORT_FINISHED} missing and Nominatim schema incomplete); run Bootstrap first (operator gate uses status.regions)"
}

prepare_worker() {
  require_db_env
  mkdir -p "${STAGING_DIR}" "${PROJECT_DIR}" "${PROJECT_DIR}/update"
  ensure_aux_data_downloads
  link_staging
  seed_project_env
  wait_for_postgres
  fix_project_ownership
}
