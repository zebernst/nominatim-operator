#!/usr/bin/env bash
# Freeze: drop Nominatim tables kept only for dynamic OSM updates (upstream 4.5
# Import docs — "Dropping Data Required for Dynamic Updates").
#
# Same effect as importing with --no-updates. After Freeze:
#   - Geocoding/API serving continues
#   - Update / CatchUp / AddRegions / aux imports that need update structures fail
#
# Do NOT Freeze before adding external data that needs update tables (e.g. TIGER).
# Import → add aux data → then Freeze if you will not apply further OSM diffs.
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPTS_DIR}/common.sh"

prepare_worker

require_bootstrap_ready

log "Running nominatim freeze (drop dynamic-update tables; serve-only database)"
run_nominatim freeze --project-dir "${PROJECT_DIR}"
log "Freeze complete"
