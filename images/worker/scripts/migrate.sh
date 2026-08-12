#!/usr/bin/env bash
# Migrate: apply Nominatim DB schema upgrades after the worker image's nominatim-db
# package was bumped (upstream 4.5 Migration docs).
#
# Operator/cluster steps outside this Job (see nominatim.org Migration guide):
#   1. Stop Update/CatchUp (mutex: Migrate is write-heavy)
#   2. Roll the worker (and later API) images to the new nominatim-db / nominatim-api
#   3. Create this Operation → nominatim admin --migrate
#   4. Roll the API image; optionally resume updates
#
# Prefer suspending the API while Migrate runs (spec.api.suspendDuringOperations=
# WriteHeavy or All); upstream recommends not serving during software/schema upgrades.
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPTS_DIR}/common.sh"

prepare_worker

require_bootstrap_ready

log "Running nominatim admin --migrate (schema upgrade for current nominatim-db)"
run_nominatim admin --migrate --project-dir "${PROJECT_DIR}"
log "Migrate complete"
