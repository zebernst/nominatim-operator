#!/usr/bin/env bash
# Reimport: full re-bootstrap. Operator arms this with NOMINATIM_REIMPORT_CONFIRM=1
# after drop/recreate of the owned DB (empty/ready application database). This script
# clears worker-local resume bookmarks on the project PVC (import-finished /
# imported-regions.txt) — not Nominatim CR status; the operator replaces
# status.regions when Reimport Succeeds — then runs Bootstrap.
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPTS_DIR}/common.sh"

prepare_worker

if [ "${NOMINATIM_REIMPORT_CONFIRM:-}" != "1" ] && [ "${NOMINATIM_REIMPORT_CONFIRM:-}" != "true" ]; then
  die "Refusing Reimport without NOMINATIM_REIMPORT_CONFIRM=1 (operator must reset DB + project first)"
fi

log "Clearing import markers for Reimport"
rm -f "${IMPORT_FINISHED}" "${IMPORTED_LIST}"
rm -rf "${PROJECT_DIR}/update"

# Optional: wipe staging PBF so a fresh download is used when PBF_URL is set.
if truthy "${NOMINATIM_REIMPORT_CLEAR_STAGING:-false}"; then
  log "Clearing staging extract"
  rm -f "${STAGING_DIR}/data.osm.pbf"
fi

exec "${SCRIPTS_DIR}/bootstrap.sh" "$@"
