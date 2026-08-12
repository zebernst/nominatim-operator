#!/usr/bin/env bash
# CatchUp: run Update repeatedly until a round applies no diffs (or max rounds).
# Same queue semantics as Update; operator decides when to create the Operation.
#
# Loops on run_update_round return contract: 0 = idle, UPDATE_APPLIED_EXIT (10) = continue.
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPTS_DIR}/common.sh"
# shellcheck source=update.sh
source "${SCRIPTS_DIR}/update.sh"

prepare_db
require_bootstrap_ready

MAX_ROUNDS="${NOMINATIM_CATCHUP_MAX_ROUNDS:-20}"

round=0
while [ "${round}" -lt "${MAX_ROUNDS}" ]; do
  round=$((round + 1))
  log "CatchUp round ${round}/${MAX_ROUNDS}"
  set +e
  run_update_round
  rc=$?
  set -e
  case "${rc}" in
    0)
      log "CatchUp complete after ${round} round(s); no further diffs"
      exit 0
      ;;
    "${UPDATE_APPLIED_EXIT}")
      ;;
    *)
      die "Update round failed with exit ${rc}"
      ;;
  esac
done

die "CatchUp hit NOMINATIM_CATCHUP_MAX_ROUNDS=${MAX_ROUNDS} with pending diffs still applying"
