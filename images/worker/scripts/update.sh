#!/usr/bin/env bash
# Update: fetch Geofabrik diffs for imported regions and nominatim add-data --diff.
# Knowledge from maintain-regions.sh incremental path — one Operation run, not a cron daemon.
#
# run_update_round return contract (CatchUp consumes this; Jobs map both to success):
#   0  — idle (no diffs applied)
#   10 — applied (UPDATE_APPLIED_EXIT)
#   *  — failure
#
# When executed as a Job entrypoint, both idle and applied exit 0 so batch/v1 Job Succeeded.
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPTS_DIR}/common.sh"

run_update_round() {
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

  local region state_file changes_file
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

  index_if_changed "${CHANGED}" "Re-indexing after Update" "No new data applied"
  update_round_exit_code "${CHANGED}"
}

# Sourced by catch-up.sh for the return-code loop; Job entrypoint runs main below.
if [[ "${BASH_SOURCE[0]}" != "${0}" ]]; then
  return 0
fi

prepare_db
require_bootstrap_ready
set +e
run_update_round
rc=$?
set -e
case "${rc}" in
  0 | "${UPDATE_APPLIED_EXIT}")
    exit 0
    ;;
  *)
    exit "${rc}"
    ;;
esac
