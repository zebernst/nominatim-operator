#!/usr/bin/env bats
# bats: Update round exit contract + shared index_if_changed (nominatim-kfy.8).

load test_helper

@test "update_round_exit_code is 0 when idle" {
  run update_round_exit_code false
  [ "$status" -eq 0 ]
}

@test "update_round_exit_code is UPDATE_APPLIED_EXIT when diffs applied" {
  run update_round_exit_code true
  [ "$status" -eq "${UPDATE_APPLIED_EXIT}" ]
  [ "${UPDATE_APPLIED_EXIT}" -eq 10 ]
}

@test "index_if_changed skips nominatim index when idle" {
  index_if_changed false "Re-indexing after Update" "No new data applied"
  [ ! -f "${PROJECT_DIR}/nominatim-index.ran" ]
}

@test "index_if_changed runs nominatim index when changed" {
  index_if_changed true "Re-indexing after Update" "No new data applied"
  [ -f "${PROJECT_DIR}/nominatim-index.ran" ]
}
