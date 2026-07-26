#!/usr/bin/env bash
# Bootstrap: nominatim import (fresh or --continue) against external Postgres.
# Knowledge from resume-import.sh / start-external.sh — phases only, no local PG.
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPTS_DIR}/common.sh"

prepare_worker

if [ -f "${IMPORT_FINISHED}" ]; then
  log "import-finished already present; Bootstrap is a no-op"
  exit 0
fi

stage="$(detect_continue_at)"
log "Detected import stage: ${stage}"

case "${stage}" in
  done)
    touch "${IMPORT_FINISHED}"
    log "Wrote ${IMPORT_FINISHED}"
    exit 0
    ;;
  fresh)
    osmfile="$(ensure_osm_file)"
    link_staging_name "data.osm.pbf"
    log "Running: nominatim import --osm-file ${osmfile}"
    run_nominatim import \
      --osm-file "${osmfile}" \
      --project-dir "${PROJECT_DIR}" \
      --threads "${THREADS}"
    ;;
  import-from-file)
    osmfile="$(ensure_osm_file)"
    link_staging_name "data.osm.pbf"
    log "Running: nominatim import --continue import-from-file"
    run_nominatim import \
      --continue import-from-file \
      --osm-file "${osmfile}" \
      --project-dir "${PROJECT_DIR}" \
      --threads "${THREADS}"
    ;;
  load-data | indexing | db-postprocess)
    log "Running: nominatim import --continue ${stage}"
    run_nominatim import \
      --continue "${stage}" \
      --project-dir "${PROJECT_DIR}" \
      --threads "${THREADS}"
    ;;
  *)
    die "Unhandled import stage '${stage}'"
    ;;
esac

# Best-effort index + check (resume-import pattern).
run_nominatim index --project-dir "${PROJECT_DIR}" --threads "${THREADS}" || true
run_nominatim admin --check-database --project-dir "${PROJECT_DIR}"

# Seed imported-regions list from NOMINATIM_REGIONS when provided.
parse_regions
if [ "${#DESIRED_REGIONS[@]}" -gt 0 ]; then
  : > "${IMPORTED_LIST}"
  for region in "${DESIRED_REGIONS[@]}"; do
    echo "${region}" >> "${IMPORTED_LIST}"
    seed_region_state "${region}" || log "Warning: could not seed update state for ${region}"
  done
fi

touch "${IMPORT_FINISHED}"
log "Bootstrap complete; wrote ${IMPORT_FINISHED}"
