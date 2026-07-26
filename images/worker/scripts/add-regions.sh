#!/usr/bin/env bash
# AddRegions: nominatim add-data for Geofabrik regions not yet in imported-regions.txt.
# Knowledge from maintain-regions.sh import_new_regions — single-phase, operator gates concurrency.
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPTS_DIR}/common.sh"

prepare_worker

if [ ! -f "${IMPORT_FINISHED}" ]; then
  die "Import not finished (${IMPORT_FINISHED} missing); run Bootstrap first"
fi

parse_regions
if [ "${#DESIRED_REGIONS[@]}" -eq 0 ]; then
  die "NOMINATIM_REGIONS is required for AddRegions"
fi

IMPORT_MAX_REGIONS="${NOMINATIM_IMPORT_MAX_REGIONS:-1}"
IMPORT_ONLY_REGION="${NOMINATIM_IMPORT_ONLY_REGION:-}"

if [ ! -f "${IMPORTED_LIST}" ]; then
  touch "${IMPORTED_LIST}"
fi

CHANGED=false
imported=0

for region in "${DESIRED_REGIONS[@]}"; do
  if [ -n "${IMPORT_ONLY_REGION}" ] && [ "${region}" != "${IMPORT_ONLY_REGION}" ]; then
    continue
  fi
  if grep -qxF "${region}" "${IMPORTED_LIST}"; then
    log "Region ${region} already imported; skipping"
    continue
  fi
  if [ "${imported}" -ge "${IMPORT_MAX_REGIONS}" ]; then
    log "Reached NOMINATIM_IMPORT_MAX_REGIONS=${IMPORT_MAX_REGIONS}; deferring remaining regions"
    break
  fi

  import_file="${STAGING_DIR}/$(echo "${region}" | tr '/' '-')-latest.osm.pbf"
  log "Downloading and importing region ${region}"
  curl -L -C - -A "${CURL_USER_AGENT}" --fail-with-body \
    "${DOWNURL}/${region}-latest.osm.pbf" -o "${import_file}"
  run_nominatim add-data --project-dir "${PROJECT_DIR}" --file "${import_file}"
  seed_region_state "${region}"
  echo "${region}" >> "${IMPORTED_LIST}"
  rm -f "${import_file}"
  imported=$((imported + 1))
  CHANGED=true
done

if [ "${CHANGED}" = "true" ]; then
  log "Re-indexing after AddRegions"
  run_nominatim index --project-dir "${PROJECT_DIR}" --threads "${THREADS}"
else
  log "No new regions imported"
fi
