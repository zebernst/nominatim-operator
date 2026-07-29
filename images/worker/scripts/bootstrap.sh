#!/usr/bin/env bash
# Bootstrap: nominatim import against an externally provisioned Postgres database.
# Never runs createdb — CNPG (or admin) must create the DB + extensions first.
# Uses nominatim import --continue … so setup_database_skeleton is skipped.
#
# Multi-region: download every NOMINATIM_REGIONS extract and pass multiple
# --osm-file flags to a single nominatim import (upstream Advanced-Installations).
# Do NOT use add-data here — it is orders of magnitude slower than initial import.
# AddRegions is the only path that uses add-data for new countries after Bootstrap.
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPTS_DIR}/common.sh"

prepare_worker

finish_bootstrap() {
  if ! import_schema_ready; then
    die "Import finished without a ready Nominatim schema (placex / database_version missing)"
  fi

  # Best-effort check (resume-import pattern).
  run_nominatim admin --check-database --project-dir "${PROJECT_DIR}" || true

  touch "${IMPORT_FINISHED}"
  log "Bootstrap complete; wrote ${IMPORT_FINISHED}"
}

# A leftover import-finished on the project PVC must not succeed Bootstrap when the
# database was recreated empty (false Succeeded unlocked API/UI early).
if [ -f "${IMPORT_FINISHED}" ]; then
  if import_schema_ready; then
    log "import-finished present and Nominatim schema ready; verifying imported regions"
    assert_imported_list_complete
    finish_bootstrap
    exit 0
  fi
  log "Stale ${IMPORT_FINISHED} (schema incomplete); removing and continuing import"
  rm -f "${IMPORT_FINISHED}"
fi

stage="$(detect_continue_at)"
log "Detected import stage: ${stage}"

case "${stage}" in
  done)
    log "Primary import schema already complete; verifying imported regions"
    assert_imported_list_complete
    finish_bootstrap
    exit 0
    ;;
  missing-db)
    die "Database '${PGDATABASE}' does not exist on ${PGHOST}. Create it declaratively (CNPG Cluster bootstrap.initdb or Database CR) before Bootstrap; the worker does not run createdb."
    ;;
  import-from-file)
    ensure_import_osm_files
    build_osm_file_args
    # Keep a stable staging name for single-file / tooling that looks for data.osm.pbf.
    if [ "${#OSM_FILES[@]}" -eq 1 ]; then
      link_staging_name "$(basename "${OSM_FILES[0]}")"
    fi
    log "Running: nominatim import --continue import-from-file with ${#OSM_FILES[@]} OSM file(s)"
    run_nominatim import \
      --continue import-from-file \
      "${OSM_FILE_ARGS[@]}" \
      --project-dir "${PROJECT_DIR}" \
      --threads "${THREADS}"
    record_imported_regions_from_spec
    ;;
  load-data | indexing | db-postprocess)
    log "Running: nominatim import --continue ${stage}"
    run_nominatim import \
      --continue "${stage}" \
      --project-dir "${PROJECT_DIR}" \
      --threads "${THREADS}"
    # Regions were recorded when import-from-file completed; if resume skipped that
    # stage after a crash mid-list write, re-record from NOMINATIM_REGIONS.
    record_imported_regions_from_spec
    ;;
  *)
    die "Unhandled import stage '${stage}'"
    ;;
esac

# Best-effort index after import (resume-import pattern).
run_nominatim index --project-dir "${PROJECT_DIR}" --threads "${THREADS}" || true
run_nominatim admin --check-database --project-dir "${PROJECT_DIR}"

finish_bootstrap
