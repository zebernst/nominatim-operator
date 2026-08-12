#!/usr/bin/env bash
# Worker entrypoint — dispatch Operation phases. Not a control plane: operator owns orchestration.
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPTS_DIR}/common.sh"

usage() {
  cat <<'EOF'
Usage: entrypoint.sh [OPERATION_TYPE]

OPERATION_TYPE (env or first arg):
  Bootstrap   Fresh or resumed nominatim import against external Postgres
  AddRegions  Import additional Geofabrik regions (add-data)
  Reimport    Full re-import (expects empty/ready DB; then Bootstrap)
  Update      Incremental Geofabrik diffs for imported regions
  CatchUp     Update loop until no further diffs (or max rounds)
  Refresh     Recompute postcodes / word-counts / functions / importance
  Migrate     Schema upgrade via nominatim admin --migrate (after image bump)
  Freeze      Drop dynamic-update tables (serve-only; no further OSM updates)

Extra args after OPERATION_TYPE are passed through to the phase script.
Without OPERATION_TYPE, remaining args are executed as a raw command
(e.g. nominatim --help) for debugging.
EOF
}

op="${OPERATION_TYPE:-}"
if [ -z "${op}" ] && [ "${#}" -gt 0 ]; then
  case "$1" in
    Bootstrap | AddRegions | Reimport | Update | CatchUp | Refresh | Migrate | Freeze | -h | --help)
      op="$1"
      shift
      ;;
  esac
fi

case "${op}" in
  "" )
    if [ "${#}" -eq 0 ]; then
      usage >&2
      exit 2
    fi
    exec "$@"
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  Bootstrap)
    exec "${SCRIPTS_DIR}/bootstrap.sh" "$@"
    ;;
  AddRegions)
    exec "${SCRIPTS_DIR}/add-regions.sh" "$@"
    ;;
  Reimport)
    exec "${SCRIPTS_DIR}/reimport.sh" "$@"
    ;;
  Update)
    exec "${SCRIPTS_DIR}/update.sh" "$@"
    ;;
  CatchUp)
    exec "${SCRIPTS_DIR}/catch-up.sh" "$@"
    ;;
  Refresh)
    exec "${SCRIPTS_DIR}/refresh.sh" "$@"
    ;;
  Migrate)
    exec "${SCRIPTS_DIR}/migrate.sh" "$@"
    ;;
  Freeze)
    exec "${SCRIPTS_DIR}/freeze.sh" "$@"
    ;;
  *)
    die "Unknown OPERATION_TYPE='${op}' (expected Bootstrap|AddRegions|Reimport|Update|CatchUp|Refresh|Migrate|Freeze)"
    ;;
esac
