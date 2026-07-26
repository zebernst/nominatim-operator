#!/usr/bin/env bash
# CatchUp: run Update repeatedly until a round applies no diffs (or max rounds).
# Same queue semantics as Update; operator decides when to create the Operation.
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPTS_DIR}/common.sh"

MAX_ROUNDS="${NOMINATIM_CATCHUP_MAX_ROUNDS:-20}"
FLAG="${IMPORT_STAGING:-/import-staging}/.update-changed"

round=0
while [ "${round}" -lt "${MAX_ROUNDS}" ]; do
  round=$((round + 1))
  log "CatchUp round ${round}/${MAX_ROUNDS}"
  rm -f "${FLAG}"
  # update.sh writes FLAG when diffs were applied (NOMINATIM_UPDATE_FLAG).
  NOMINATIM_UPDATE_FLAG="${FLAG}" "${SCRIPTS_DIR}/update.sh"
  if [ ! -f "${FLAG}" ]; then
    log "CatchUp complete after ${round} round(s); no further diffs"
    exit 0
  fi
done

die "CatchUp hit NOMINATIM_CATCHUP_MAX_ROUNDS=${MAX_ROUNDS} with pending diffs still applying"
