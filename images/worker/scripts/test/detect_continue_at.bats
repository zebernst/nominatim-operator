#!/usr/bin/env bats
# bats tests for detect_continue_at / import_schema_ready (common.sh).

load test_helper

@test "NOMINATIM_CONTINUE_AT override wins" {
  export NOMINATIM_CONTINUE_AT=indexing
  run detect_continue_at
  [ "$status" -eq 0 ]
  [ "$output" = "indexing" ]
}

@test "invalid NOMINATIM_CONTINUE_AT dies" {
  export NOMINATIM_CONTINUE_AT=bogus
  run detect_continue_at
  [ "$status" -ne 0 ]
  [[ "$output" == *"Invalid NOMINATIM_CONTINUE_AT"* ]]
}

@test "import-finished alone is not done without schema" {
  touch "${IMPORT_FINISHED}"
  export STUB_HAS_PLACEX_REL=f
  export STUB_HAS_VERSION=f
  export STUB_HAS_PLACE=f
  run detect_continue_at
  [ "$status" -eq 0 ]
  [ "$output" = "import-from-file" ]
}

@test "import-finished + ready schema reports done" {
  touch "${IMPORT_FINISHED}"
  export STUB_HAS_PLACEX_REL=t
  export STUB_HAS_VERSION=t
  run detect_continue_at
  [ "$status" -eq 0 ]
  [ "$output" = "done" ]
}

@test "missing database reports missing-db" {
  export STUB_DB_EXISTS=""
  run detect_continue_at
  [ "$status" -eq 0 ]
  [ "$output" = "missing-db" ]
}

@test "empty place table reports import-from-file" {
  export STUB_HAS_PLACE=f
  run detect_continue_at
  [ "$status" -eq 0 ]
  [ "$output" = "import-from-file" ]
}

@test "place without placex relation reports load-data" {
  export STUB_HAS_PLACE=t
  export STUB_TO_REGCLASS_PLACEX=f
  run detect_continue_at
  [ "$status" -eq 0 ]
  [ "$output" = "load-data" ]
}

@test "empty placex reports load-data" {
  export STUB_HAS_PLACE=t
  export STUB_TO_REGCLASS_PLACEX=t
  export STUB_PLACEX_LOADED=f
  run detect_continue_at
  [ "$status" -eq 0 ]
  [ "$output" = "load-data" ]
}

@test "database_version present reports done" {
  export STUB_HAS_PLACE=t
  export STUB_TO_REGCLASS_PLACEX=t
  export STUB_PLACEX_LOADED=t
  export STUB_HAS_VERSION=t
  run detect_continue_at
  [ "$status" -eq 0 ]
  [ "$output" = "done" ]
}

@test "indexing pending reports indexing" {
  export STUB_HAS_PLACE=t
  export STUB_TO_REGCLASS_PLACEX=t
  export STUB_PLACEX_LOADED=t
  export STUB_HAS_VERSION=f
  export STUB_INDEXING_STARTED=t
  export STUB_HAS_PENDING=t
  run detect_continue_at
  [ "$status" -eq 0 ]
  [ "$output" = "indexing" ]
}

@test "indexing complete without version reports db-postprocess" {
  export STUB_HAS_PLACE=t
  export STUB_TO_REGCLASS_PLACEX=t
  export STUB_PLACEX_LOADED=t
  export STUB_HAS_VERSION=f
  export STUB_INDEXING_STARTED=t
  export STUB_HAS_PENDING=f
  run detect_continue_at
  [ "$status" -eq 0 ]
  [ "$output" = "db-postprocess" ]
}

@test "placex rows but indexing not started reports load-data" {
  export STUB_HAS_PLACE=t
  export STUB_TO_REGCLASS_PLACEX=t
  export STUB_PLACEX_LOADED=t
  export STUB_HAS_VERSION=f
  export STUB_INDEXING_STARTED=f
  run detect_continue_at
  [ "$status" -eq 0 ]
  [ "$output" = "load-data" ]
}
