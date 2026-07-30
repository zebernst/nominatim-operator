#!/usr/bin/env bash
# AddRegions: nominatim add-data for every Geofabrik region in NOMINATIM_REGIONS
# not yet in imported-regions.txt. NOMINATIM_REGIONS is the exact import set for
# this Operation (operator sets it from op.Spec.Regions / parent.Spec.Regions and
# chunks drift-driven AddRegions to one region at a time — see images/README.md).
# Knowledge from maintain-regions.sh import_new_regions — single-phase, operator gates concurrency.
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPTS_DIR}/common.sh"

prepare_worker

require_bootstrap_ready

parse_regions
if [ "${#DESIRED_REGIONS[@]}" -eq 0 ]; then
  die "NOMINATIM_REGIONS is required for AddRegions"
fi

if [ ! -f "${IMPORTED_LIST}" ]; then
  touch "${IMPORTED_LIST}"
fi

CHANGED=false

for region in "${DESIRED_REGIONS[@]}"; do
  if region_already_imported "${region}"; then
    log "Region ${region} already imported; skipping"
    continue
  fi

  import_geofabrik_region "${region}"
  CHANGED=true
done

if [ "${CHANGED}" = "true" ]; then
  # Upstream Advanced-Installations: refresh --postcodes after add-data, then index.
  log "Running nominatim refresh --postcodes after AddRegions"
  run_nominatim refresh --postcodes --project-dir "${PROJECT_DIR}" || true
  log "Re-indexing after AddRegions"
  run_nominatim index --project-dir "${PROJECT_DIR}" --threads "${THREADS}"
else
  log "No new regions imported"
fi
