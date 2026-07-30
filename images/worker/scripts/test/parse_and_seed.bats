#!/usr/bin/env bats
# bats tests for parse_regions and seed_project_env (common.sh).

load test_helper

@test "parse_regions empty leaves DESIRED_REGIONS empty" {
  unset NOMINATIM_REGIONS
  parse_regions
  [ "${#DESIRED_REGIONS[@]}" -eq 0 ]
}

@test "parse_regions splits commas and spaces" {
  export NOMINATIM_REGIONS="europe/monaco, europe/andorra  asia/georgia"
  parse_regions
  [ "${#DESIRED_REGIONS[@]}" -eq 3 ]
  [ "${DESIRED_REGIONS[0]}" = "europe/monaco" ]
  [ "${DESIRED_REGIONS[1]}" = "europe/andorra" ]
  [ "${DESIRED_REGIONS[2]}" = "asia/georgia" ]
}

@test "seed_project_env copies defaults and sets import style" {
  export IMPORT_STYLE=address
  seed_project_env
  [ -f "${PROJECT_DIR}/.env" ]
  grep -qE '^NOMINATIM_IMPORT_STYLE=address$' "${PROJECT_DIR}/.env"
  grep -qE '^NOMINATIM_DATABASE_WEBUSER=www-data$' "${PROJECT_DIR}/.env"
}

@test "seed_project_env updates existing .env keys" {
  cp "${ENV_DEFAULTS}" "${PROJECT_DIR}/.env"
  export IMPORT_STYLE=full
  export NOMINATIM_DATABASE_WEBUSER=custom-web
  export NOMINATIM_FLATNODE_FILE=/data/flatnode.file
  export NOMINATIM_REPLICATION_URL=https://example.test/updates
  seed_project_env
  grep -qE '^NOMINATIM_IMPORT_STYLE=full$' "${PROJECT_DIR}/.env"
  grep -qE '^NOMINATIM_DATABASE_WEBUSER=custom-web$' "${PROJECT_DIR}/.env"
  grep -qE '^NOMINATIM_FLATNODE_FILE=/data/flatnode.file$' "${PROJECT_DIR}/.env"
  grep -qE '^NOMINATIM_REPLICATION_URL=https://example.test/updates$' "${PROJECT_DIR}/.env"
}
