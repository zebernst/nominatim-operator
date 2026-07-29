#!/usr/bin/env bash
# Shared helpers for worker Operation phases. Keep thin: wait, link, run nominatim CLI.
# shellcheck shell=bash

PROJECT_DIR="${PROJECT_DIR:-/nominatim}"
STAGING_DIR="${IMPORT_STAGING:-/import-staging}"
IMPORT_FINISHED="${IMPORT_FINISHED:-${PROJECT_DIR}/import-finished}"
IMPORTED_LIST="${IMPORTED_LIST:-${PROJECT_DIR}/imported-regions.txt}"
ENV_DEFAULTS="${ENV_DEFAULTS:-/opt/nominatim/env.defaults}"
DOWNURL="${NOMINATIM_DOWNURL:-https://download.geofabrik.de}"
CURL_USER_AGENT="${USER_AGENT:-nominatim-worker}"
THREADS="${THREADS:-$(nproc)}"
# Ubuntu packages pyosmium helpers under /usr/lib/python3-pyosmium (on PATH in the image).
PYOSMIUM_GET_CHANGES="${PYOSMIUM_GET_CHANGES:-pyosmium-get-changes}"

# Logs go to stderr so command substitutions (e.g. osmfile="$(ensure_osm_file)") stay clean.
log() { echo "[nominatim-worker] $*" >&2; }

die() { echo "[nominatim-worker] ERROR: $*" >&2; exit 1; }

truthy() {
  case "${1,,}" in
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

link_staging() {
  mkdir -p "${STAGING_DIR}" "${PROJECT_DIR}"
  link_staging_name "data.osm.pbf"
  link_staging_name "wikimedia-importance.csv.gz"
  link_staging_name "us_postcodes.csv.gz"
  link_staging_name "secondary_importance.sql.gz"
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
    sed -i "s|^NOMINATIM_IMPORT_STYLE=.*|NOMINATIM_IMPORT_STYLE=${import_style}|" "${env_file}"
  else
    printf 'NOMINATIM_IMPORT_STYLE=%s\n' "${import_style}" >> "${env_file}"
  fi

  if grep -qE '^NOMINATIM_DATABASE_WEBUSER=' "${env_file}"; then
    sed -i "s|^NOMINATIM_DATABASE_WEBUSER=.*|NOMINATIM_DATABASE_WEBUSER=${webuser}|" "${env_file}"
  else
    printf 'NOMINATIM_DATABASE_WEBUSER=%s\n' "${webuser}" >> "${env_file}"
  fi

  if [ -n "${NOMINATIM_FLATNODE_FILE:-}" ]; then
    if grep -qE '^NOMINATIM_FLATNODE_FILE=' "${env_file}"; then
      sed -i "s|^NOMINATIM_FLATNODE_FILE=.*|NOMINATIM_FLATNODE_FILE=${NOMINATIM_FLATNODE_FILE}|" "${env_file}"
    else
      printf 'NOMINATIM_FLATNODE_FILE=%s\n' "${NOMINATIM_FLATNODE_FILE}" >> "${env_file}"
    fi
  fi

  if [ -n "${NOMINATIM_REPLICATION_URL:-}" ]; then
    if grep -qE '^NOMINATIM_REPLICATION_URL=' "${env_file}"; then
      sed -i "s|^NOMINATIM_REPLICATION_URL=.*|NOMINATIM_REPLICATION_URL=${NOMINATIM_REPLICATION_URL}|" "${env_file}"
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
  local import_file="${STAGING_DIR}/$(echo "${region}" | tr '/' '-')-latest.osm.pbf"
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

# format_sequence_state reads a Geofabrik state.txt / sequence.state and prints
# "sequenceNumber@timestamp" (timestamp backslashes stripped). Empty if unreadable.
format_sequence_state() {
  local state_file="$1"
  if [ ! -f "${state_file}" ]; then
    return 0
  fi
  local seq ts
  seq="$(grep -E '^sequenceNumber=' "${state_file}" | head -n1 | cut -d= -f2- || true)"
  ts="$(grep -E '^timestamp=' "${state_file}" | head -n1 | cut -d= -f2- | tr -d '\\' || true)"
  if [ -z "${seq}" ]; then
    return 0
  fi
  if [ -n "${ts}" ]; then
    printf '%s@%s\n' "${seq}" "${ts}"
  else
    printf '%s\n' "${seq}"
  fi
}

# report_sequence_states PATCHes nominatim.zebernst.dev/sequence-report onto the
# current NominatimOperation (NOMINATIM_OPERATION_NAME) so the operator can copy
# values into status.regions[].sequenceState. Best-effort: missing SA token or
# API errors are logged and ignored so import/update success is not blocked.
report_sequence_states() {
  local op_name="${NOMINATIM_OPERATION_NAME:-}"
  local token_file="${KUBERNETES_SERVICEACCOUNT_TOKEN:-/var/run/secrets/kubernetes.io/serviceaccount/token}"
  local ca_file="${KUBERNETES_SERVICEACCOUNT_CA:-/var/run/secrets/kubernetes.io/serviceaccount/ca.crt}"
  local ns_file="${KUBERNETES_SERVICEACCOUNT_NAMESPACE:-/var/run/secrets/kubernetes.io/serviceaccount/namespace}"
  local api_host="${KUBERNETES_SERVICE_HOST:-}"
  local api_port="${KUBERNETES_SERVICE_PORT_HTTPS:-${KUBERNETES_SERVICE_PORT:-443}}"

  if [ -z "${op_name}" ]; then
    log "Skipping sequence report: NOMINATIM_OPERATION_NAME unset"
    return 0
  fi
  if [ ! -f "${token_file}" ] || [ ! -f "${ns_file}" ]; then
    log "Skipping sequence report: service account credentials not mounted"
    return 0
  fi
  if [ -z "${api_host}" ]; then
    log "Skipping sequence report: KUBERNETES_SERVICE_HOST unset"
    return 0
  fi

  parse_regions
  local desired_csv=""
  if [ "${#DESIRED_REGIONS[@]}" -gt 0 ]; then
    desired_csv="$(IFS=,; echo "${DESIRED_REGIONS[*]}")"
  fi
  local report
  report="$(
    PROJECT_DIR="${PROJECT_DIR}" DESIRED_CSV="${desired_csv}" python3 - <<'PY'
import json, os, pathlib, re

project = pathlib.Path(os.environ["PROJECT_DIR"])
desired = [r for r in os.environ.get("DESIRED_CSV", "").split(",") if r]
if desired:
    regions = desired
else:
    regions = sorted(
        str(p.parent.relative_to(project / "update"))
        for p in (project / "update").glob("**/sequence.state")
    )

out = {}
for region in regions:
    state = project / "update" / region / "sequence.state"
    if not state.is_file():
        continue
    text = state.read_text(encoding="utf-8", errors="replace")
    seq_m = re.search(r"^sequenceNumber=(.+)$", text, re.M)
    ts_m = re.search(r"^timestamp=(.+)$", text, re.M)
    if not seq_m:
        continue
    seq = seq_m.group(1).strip()
    if ts_m:
        ts = ts_m.group(1).strip().replace("\\", "")
        out[region] = f"{seq}@{ts}"
    else:
        out[region] = seq
print(json.dumps(out, separators=(",", ":")))
PY
  )" || {
    log "Warning: failed to build sequence report JSON"
    return 0
  }

  if [ "${report}" = "{}" ] || [ -z "${report}" ]; then
    log "No sequence.state files to report"
    return 0
  fi

  local ns token
  ns="$(cat "${ns_file}")"
  token="$(cat "${token_file}")"
  local url="https://${api_host}:${api_port}/apis/nominatim.zebernst.dev/v1alpha1/namespaces/${ns}/nominatimoperations/${op_name}"
  local body
  body="$(REPORT_JSON="${report}" python3 - <<'PY'
import json, os
report = os.environ["REPORT_JSON"]
print(json.dumps({
    "metadata": {
        "annotations": {
            "nominatim.zebernst.dev/sequence-report": report,
        }
    }
}))
PY
)"

  if ! curl -fsS \
      --connect-timeout 5 --max-time 30 \
      -X PATCH \
      -H "Authorization: Bearer ${token}" \
      -H "Content-Type: application/merge-patch+json" \
      --cacert "${ca_file}" \
      -d "${body}" \
      "${url}" >/dev/null; then
    log "Warning: failed to PATCH sequence-report onto NominatimOperation ${op_name}"
    return 0
  fi
  log "Reported sequence state for $(echo "${report}" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))') region(s)"
}

prepare_worker() {
  require_db_env
  mkdir -p "${STAGING_DIR}" "${PROJECT_DIR}" "${PROJECT_DIR}/update"
  link_staging
  seed_project_env
  wait_for_postgres
  fix_project_ownership
}
