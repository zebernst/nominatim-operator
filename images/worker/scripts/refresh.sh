#!/usr/bin/env bash
# Refresh: recomputes auxiliary Nominatim data (postcodes, word counts, PL/pgSQL
# functions, place importance). Admin work that must not run on the API boot path
# (nominatim-5et.12 / plane separation).
#
# Default task set matches common maintenance after import / config changes.
# Override with NOMINATIM_REFRESH_TASKS (space-separated nominatim refresh flags),
# e.g. NOMINATIM_REFRESH_TASKS="--functions --postcodes".
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPTS_DIR}/common.sh"

prepare_worker

require_bootstrap_ready

# Default: postcodes, word-counts, functions, importance (not wiki-data / secondary-
# importance — those need staged aux files; appended below when present).
DEFAULT_TASKS="--postcodes --word-counts --functions --importance"
read -r -a TASKS <<< "${NOMINATIM_REFRESH_TASKS:-${DEFAULT_TASKS}}"

if [ "${#TASKS[@]}" -eq 0 ]; then
  die "NOMINATIM_REFRESH_TASKS is empty; pass at least one nominatim refresh flag"
fi

# Backfill wiki/secondary importance when aux files are on the project volume.
task_contains() {
  local needle="$1"
  local t
  for t in "${TASKS[@]}"; do
    [ "${t}" = "${needle}" ] && return 0
  done
  return 1
}
if [ -f "${PROJECT_DIR}/wikimedia-importance.csv.gz" ] && [ -s "${PROJECT_DIR}/wikimedia-importance.csv.gz" ]; then
  task_contains "--wiki-data" || TASKS+=(--wiki-data)
fi
if [ -f "${PROJECT_DIR}/secondary_importance.sql.gz" ] && [ -s "${PROJECT_DIR}/secondary_importance.sql.gz" ]; then
  task_contains "--secondary-importance" || TASKS+=(--secondary-importance)
fi

log "Running nominatim refresh ${TASKS[*]}"
run_nominatim refresh "${TASKS[@]}" --project-dir "${PROJECT_DIR}"
log "Refresh complete"
