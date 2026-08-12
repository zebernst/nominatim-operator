#!/usr/bin/env bats
# bats tests for phase-scoped prepare_db vs prepare_import.

load test_helper

@test "prepare_db waits and seeds without linking staging or fetching aux" {
  prepare_db
  [ -d "${PROJECT_DIR}/update" ]
  [ -f "${PROJECT_DIR}/.env" ]
  [ ! -e "${PROJECT_DIR}/data.osm.pbf" ]
  [ ! -e "${PROJECT_DIR}/us_postcodes.csv.gz" ]
}

@test "prepare_import links PBF and materializes enabled aux datasets" {
  export NOMINATIM_AUX_US_POSTCODES=true
  prepare_import
  [ -L "${PROJECT_DIR}/data.osm.pbf" ]
  [ "$(readlink "${PROJECT_DIR}/data.osm.pbf")" = "${IMPORT_STAGING}/data.osm.pbf" ]
  [ -f "${PROJECT_DIR}/us_postcodes.csv.gz" ]
  [ ! -L "${PROJECT_DIR}/us_postcodes.csv.gz" ]
}
