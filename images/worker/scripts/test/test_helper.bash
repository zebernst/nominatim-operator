#!/usr/bin/env bash
# Shared bats setup for worker common.sh helpers.
# shellcheck shell=bash

setup() {
  TEST_TMP="$(mktemp -d)"
  export PROJECT_DIR="${TEST_TMP}/project"
  export IMPORT_STAGING="${TEST_TMP}/staging"
  export IMPORT_FINISHED="${PROJECT_DIR}/import-finished"
  export IMPORTED_LIST="${PROJECT_DIR}/imported-regions.txt"
  export ENV_DEFAULTS="${BATS_TEST_DIRNAME}/../../env.defaults"
  export PGHOST=localhost
  export PGDATABASE=nominatim
  export PGUSER=nominatim
  export PGPASSWORD=secret
  # Avoid nproc (coreutils) — not always on PATH in macOS CI runners / local shells.
  export THREADS=1
  unset NOMINATIM_CONTINUE_AT || true
  unset NOMINATIM_REGIONS || true
  unset NOMINATIM_FLATNODE_FILE || true
  unset NOMINATIM_REPLICATION_URL || true
  unset IMPORT_STYLE || true
  unset NOMINATIM_DATABASE_WEBUSER || true
  unset NOMINATIM_AUX_WIKIMEDIA_IMPORTANCE || true
  unset NOMINATIM_AUX_SECONDARY_IMPORTANCE || true
  unset NOMINATIM_AUX_US_POSTCODES || true

  mkdir -p "${PROJECT_DIR}" "${IMPORT_STAGING}"
  chmod +x "${BATS_TEST_DIRNAME}/stubs/"*
  export PATH="${BATS_TEST_DIRNAME}/stubs:${PATH}"

  # Reset stub answers to "empty DB / nothing imported".
  export STUB_DB_EXISTS=1
  export STUB_HAS_PLACE=f
  export STUB_HAS_PLACEX_REL=f
  export STUB_TO_REGCLASS_PLACEX=f
  export STUB_PLACEX_LOADED=f
  export STUB_HAS_VERSION=f
  export STUB_INDEXING_STARTED=f
  export STUB_HAS_PENDING=f

  # shellcheck source=../common.sh
  source "${BATS_TEST_DIRNAME}/../common.sh"
}

teardown() {
  rm -rf "${TEST_TMP}"
}
