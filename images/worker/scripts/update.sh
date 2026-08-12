#!/usr/bin/env bash
# Update: fetch Geofabrik diffs for imported regions and nominatim add-data --diff.
# Knowledge from maintain-regions.sh incremental path — one Operation run, not a cron daemon.
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPTS_DIR}/common.sh"

prepare_db

require_bootstrap_ready

parse_regions
if [ "${#DESIRED_REGIONS[@]}" -eq 0 ]; then
  die "NOMINATIM_REGIONS is required for Update"
fi

if [ ! -f "${IMPORTED_LIST}" ]; then
  touch "${IMPORTED_LIST}"
  base="${DESIRED_REGIONS[0]}"
  log "Seeding imported list with base region ${base}"
  echo "${base}" >> "${IMPORTED_LIST}"
  seed_region_state "${base}"
fi

CHANGED=false

for region in "${DESIRED_REGIONS[@]}"; do
  if ! grep -qxF "${region}" "${IMPORTED_LIST}"; then
    log "Region ${region} not yet imported; skipping (use AddRegions)"
    continue
  fi

  state_file="${PROJECT_DIR}/update/${region}/sequence.state"
  if [ ! -f "${state_file}" ]; then
    log "Missing state for ${region}; seeding and continuing"
    seed_region_state "${region}"
    continue
  fi

  changes_file="${STAGING_DIR}/changes-$(echo "${region}" | tr '/' '-').osc.gz"
  log "Fetching changes for ${region}"
  if "${PYOSMIUM_GET_CHANGES}" \
      --server "${DOWNURL}/${region}-updates/" \
      --state-file "${state_file}" \
      -o "${changes_file}" \
      && [ -s "${changes_file}" ]; then
    run_nominatim add-data \
      --project-dir "${PROJECT_DIR}" \
      --diff "${changes_file}"
    CHANGED=true
  else
    log "No new changes for ${region}"
  fi
  rm -f "${changes_file}"
done

if [ "${CHANGED}" = "true" ]; then
  log "Re-indexing after Update"
  run_nominatim index --project-dir "${PROJECT_DIR}" --threads "${THREADS}"
else
  log "No new data applied"
fi

# Optional flag file for CatchUp (and Jobs) to observe whether diffs applied.
if [ -n "${NOMINATIM_UPDATE_FLAG:-}" ]; then
  if [ "${CHANGED}" = "true" ]; then
    touch "${NOMINATIM_UPDATE_FLAG}"
  else
    rm -f "${NOMINATIM_UPDATE_FLAG}"
  fi
fi
