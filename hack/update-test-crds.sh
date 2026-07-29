#!/usr/bin/env bash
# Refresh the third-party CRDs vendored under test/crds/ for envtest.
#
# envtest runs only the API server + etcd, so the operator's unstructured writes to
# CNPG and Gateway API types are validated against these schemas but no third-party
# controller reconciles them. Keep the pins below in sync with hack/validate-kind.sh
# (CNPG) and the Gateway API version documented in test/crds/README.md.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEST="${ROOT}/test/crds"

# Matches CNPG_VERSION in hack/validate-kind.sh.
CNPG_VERSION="${CNPG_VERSION:-1.26.1}"
GATEWAY_API_VERSION="${GATEWAY_API_VERSION:-v1.3.0}"

CNPG_BASE="https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/v${CNPG_VERSION}/config/crd/bases"
GATEWAY_BASE="https://raw.githubusercontent.com/kubernetes-sigs/gateway-api/${GATEWAY_API_VERSION}/config/crd/standard"

log() { printf '==> %s\n' "$*"; }

fetch() {
  local url="$1" dest="$2"
  log "${dest#"${ROOT}"/} <- ${url}"
  curl -sfL "${url}" -o "${dest}"
}

mkdir -p "${DEST}"

# CNPG: Cluster is written by reconcileDatabaseClusterCreate, Database by
# ensureOwnedCNPGDatabase. Other CNPG kinds are not touched by the operator.
fetch "${CNPG_BASE}/postgresql.cnpg.io_clusters.yaml" "${DEST}/postgresql.cnpg.io_clusters.yaml"
fetch "${CNPG_BASE}/postgresql.cnpg.io_databases.yaml" "${DEST}/postgresql.cnpg.io_databases.yaml"

# Gateway API standard channel: HTTPRoute is written by reconcileHTTPRoute.
fetch "${GATEWAY_BASE}/gateway.networking.k8s.io_httproutes.yaml" \
  "${DEST}/gateway.networking.k8s.io_httproutes.yaml"

log "done — remember to update the pin table in test/crds/README.md"
