#!/usr/bin/env bash
# Compare Go statement coverage for unit-tested packages against .coverage-thresholds.json.
# Usage:
#   make test && ./hack/check-coverage.sh
#   ./hack/check-coverage.sh build/cover.out
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PROFILE="${1:-build/cover.out}"
THRESH_FILE="${COVERAGE_THRESHOLDS_FILE:-.coverage-thresholds.json}"

if [[ ! -f "$PROFILE" ]]; then
  echo "coverage profile not found: $PROFILE" >&2
  echo "run: make test (writes build/cover.out) or pass a coverprofile path" >&2
  exit 1
fi

if [[ ! -f "$THRESH_FILE" ]]; then
  echo "missing $THRESH_FILE" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required to read $THRESH_FILE" >&2
  exit 1
fi

# Primary unit-tested package; e2e / generated / thin cmd wiring are exempt (see thresholds file).
PKG="${COVERAGE_PACKAGE:-github.com/zebernst/nominatim-operator/internal/controller}"
MIN="$(jq -r '.thresholds.statements // .thresholds.lines // empty' "$THRESH_FILE")"
if [[ -z "$MIN" || "$MIN" == "null" ]]; then
  echo "$THRESH_FILE: thresholds.statements (or .lines) is required" >&2
  exit 1
fi

# Filter the combined profile to the package under gate (coverprofile format: mode line, then file:line.col,count).
FILTERED="$(mktemp)"
trap 'rm -f "$FILTERED"' EXIT
{
  head -n 1 "$PROFILE"
  # Paths in the profile are filesystem paths; match on the module import path suffix.
  grep -E "/internal/controller/" "$PROFILE" || true
} >"$FILTERED"

if [[ "$(wc -l <"$FILTERED" | tr -d ' ')" -le 1 ]]; then
  echo "no coverage entries for $PKG in $PROFILE" >&2
  exit 1
fi

TOTAL="$(go tool cover -func="$FILTERED" | awk '/^total:/ { gsub(/%/, "", $NF); print $NF }')"
if [[ -z "$TOTAL" ]]; then
  echo "could not parse total coverage from $PROFILE" >&2
  exit 1
fi

echo "coverage gate: $PKG statements ${TOTAL}% (minimum ${MIN}%)"

awk -v total="$TOTAL" -v min="$MIN" 'BEGIN {
  if (total + 0 < min + 0) {
    printf "coverage %.1f%% is below threshold %.1f%%\n", total, min > "/dev/stderr"
    exit 1
  }
}'
