#!/usr/bin/env bats
# bats tests for ensure_aux_data_downloads / materialize_aux_file_to_project.

load test_helper

@test "ensure_aux_data_downloads skips when NOMINATIM_AUX_* unset" {
  unset NOMINATIM_AUX_WIKIMEDIA_IMPORTANCE || true
  unset NOMINATIM_AUX_SECONDARY_IMPORTANCE || true
  unset NOMINATIM_AUX_US_POSTCODES || true
  ensure_aux_data_downloads
  [ ! -e "${IMPORT_STAGING}/us_postcodes.csv.gz" ]
  [ ! -e "${PROJECT_DIR}/us_postcodes.csv.gz" ]
}

@test "ensure_aux_data_downloads fetches and materializes enabled datasets" {
  export NOMINATIM_AUX_US_POSTCODES=true
  export NOMINATIM_AUX_SECONDARY_IMPORTANCE=1
  export NOMINATIM_AUX_WIKIMEDIA_IMPORTANCE=false
  ensure_aux_data_downloads

  [ -f "${IMPORT_STAGING}/us_postcodes.csv.gz" ]
  [ -s "${PROJECT_DIR}/us_postcodes.csv.gz" ]
  [ ! -L "${PROJECT_DIR}/us_postcodes.csv.gz" ]
  grep -q 'us_postcodes.csv.gz' "${PROJECT_DIR}/us_postcodes.csv.gz"

  [ -f "${IMPORT_STAGING}/secondary_importance.sql.gz" ]
  [ -s "${PROJECT_DIR}/secondary_importance.sql.gz" ]
  grep -q 'wikimedia-secondary-importance.sql.gz' "${PROJECT_DIR}/secondary_importance.sql.gz"

  [ ! -e "${IMPORT_STAGING}/wikimedia-importance.csv.gz" ]
  [ ! -e "${PROJECT_DIR}/wikimedia-importance.csv.gz" ]
}

@test "ensure_aux_data_downloads reuses existing staging file without re-download" {
  export NOMINATIM_AUX_US_POSTCODES=true
  mkdir -p "${IMPORT_STAGING}"
  printf 'already-staged\n' > "${IMPORT_STAGING}/us_postcodes.csv.gz"
  ensure_aux_data_downloads
  grep -q 'already-staged' "${PROJECT_DIR}/us_postcodes.csv.gz"
}

@test "link_staging does not create broken aux symlinks" {
  link_staging
  [ -L "${PROJECT_DIR}/data.osm.pbf" ]
  [ ! -e "${PROJECT_DIR}/wikimedia-importance.csv.gz" ]
  [ ! -e "${PROJECT_DIR}/us_postcodes.csv.gz" ]
  [ ! -e "${PROJECT_DIR}/secondary_importance.sql.gz" ]
}
