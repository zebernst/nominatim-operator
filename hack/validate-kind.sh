#!/usr/bin/env bash
# Local kind validation: CNPG + operator + Monaco import + search probe (+ optional AddRegions/Rebuild).
# Does NOT use the homelab kubeconfig. Requires docker, kind, kubectl, curl, python3.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

KIND_CLUSTER="${KIND_CLUSTER:-kind}"
OPERATOR_IMG="${OPERATOR_IMG:-example.com/nominatim-operator:e2e}"
API_IMG="${API_IMG:-example.com/nominatim-api:e2e}"
WORKER_IMG="${WORKER_IMG:-example.com/nominatim-worker:e2e}"
VALIDATION_NS="${VALIDATION_NS:-nominatim-validation}"
NOM_NAME="${NOM_NAME:-monaco}"
FIXTURE="${FIXTURE:-test/e2e/testdata/nominatim-monaco.yaml}"
CNPG_VERSION="${CNPG_VERSION:-1.26.1}"
CNPG_URL="https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.26/releases/cnpg-${CNPG_VERSION}.yaml"
RUN_ADD_REGIONS="${RUN_ADD_REGIONS:-1}"
RUN_REBUILD="${RUN_REBUILD:-1}"
SEARCH_QUERY="${SEARCH_QUERY:-avenue%20pasteur}"

log() { printf '==> %s\n' "$*"; }

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "missing required command: $1" >&2; exit 1; }
}

need kind
need kubectl
need docker
need curl
need python3

ensure_kind() {
  if ! kind get clusters 2>/dev/null | grep -qx "${KIND_CLUSTER}"; then
    log "creating kind cluster ${KIND_CLUSTER}"
    kind create cluster --name "${KIND_CLUSTER}" --wait 120s
  else
    log "reusing kind cluster ${KIND_CLUSTER}"
  fi
  kubectl cluster-info --context "kind-${KIND_CLUSTER}" >/dev/null
}

install_cnpg() {
  if kubectl get crd clusters.postgresql.cnpg.io >/dev/null 2>&1; then
    log "CloudNativePG CRD already present"
  else
    log "installing CloudNativePG ${CNPG_VERSION}"
    kubectl apply --server-side --force-conflicts -f "${CNPG_URL}"
  fi
  kubectl wait deployment.apps/cnpg-controller-manager \
    --for=condition=Available -n cnpg-system --timeout=5m
}

build_and_load() {
  log "building operator/api/worker images"
  make docker-build IMG="${OPERATOR_IMG}"
  make docker-build-api API_IMG="${API_IMG}"
  make docker-build-worker WORKER_IMG="${WORKER_IMG}"
  log "loading images into kind"
  kind load docker-image "${OPERATOR_IMG}" --name "${KIND_CLUSTER}"
  kind load docker-image "${API_IMG}" --name "${KIND_CLUSTER}"
  kind load docker-image "${WORKER_IMG}" --name "${KIND_CLUSTER}"
}

deploy_operator() {
  log "installing CRDs and deploying operator (${OPERATOR_IMG})"
  make install
  make deploy IMG="${OPERATOR_IMG}"
  kubectl wait deployment/nominatim-operator-controller-manager \
    -n nominatim-operator-system --for=condition=Available --timeout=5m
}

apply_fixture() {
  log "applying Monaco validation fixture"
  kubectl apply -f "${FIXTURE}"
}

wait_jsonpath() {
  local desc="$1" timeout="$2"
  shift 2
  log "waiting (${timeout}): ${desc}"
  kubectl wait "$@" --timeout="${timeout}"
}

wait_bootstrap() {
  log "waiting for CNPG Cluster object ${NOM_NAME}-pg"
  local deadline=$((SECONDS + 300))
  while (( SECONDS < deadline )); do
    if kubectl get "cluster.postgresql.cnpg.io/${NOM_NAME}-pg" -n "${VALIDATION_NS}" >/dev/null 2>&1; then
      break
    fi
    sleep 2
  done
  if ! kubectl get "cluster.postgresql.cnpg.io/${NOM_NAME}-pg" -n "${VALIDATION_NS}" >/dev/null 2>&1; then
    echo "timed out waiting for Cluster ${NOM_NAME}-pg to be created" >&2
    return 1
  fi

  wait_jsonpath "CNPG cluster Ready" "15m" \
    --for=condition=Ready "cluster.postgresql.cnpg.io/${NOM_NAME}-pg" -n "${VALIDATION_NS}"

  log "waiting for CNPG Database ${NOM_NAME}-pg-nominatim applied"
  deadline=$((SECONDS + 300))
  while (( SECONDS < deadline )); do
    applied="$(kubectl get database "${NOM_NAME}-pg-nominatim" -n "${VALIDATION_NS}" \
      -o jsonpath='{.status.applied}' 2>/dev/null || true)"
    if [[ "${applied}" == "true" ]]; then
      break
    fi
    sleep 2
  done
  if [[ "${applied:-}" != "true" ]]; then
    echo "timed out waiting for Database ${NOM_NAME}-pg-nominatim status.applied=true" >&2
    kubectl get database -n "${VALIDATION_NS}" -o yaml || true
    return 1
  fi

  log "waiting for Bootstrap NominatimOperation Succeeded"
  deadline=$((SECONDS + 2400))
  while (( SECONDS < deadline )); do
    phase="$(kubectl get nominatimoperation "${NOM_NAME}-bootstrap" -n "${VALIDATION_NS}" \
      -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    if [[ "${phase}" == "Succeeded" ]]; then
      log "Bootstrap Succeeded"
      return 0
    fi
    if [[ "${phase}" == "Failed" ]]; then
      kubectl get nominatimoperation "${NOM_NAME}-bootstrap" -n "${VALIDATION_NS}" -o yaml || true
      kubectl get jobs,pods -n "${VALIDATION_NS}" -o wide || true
      echo "Bootstrap Failed" >&2
      return 1
    fi
    sleep 10
  done
  echo "timed out waiting for Bootstrap" >&2
  return 1
}

assert_search() {
  log "waiting for API Deployment Available"
  local deadline=$((SECONDS + 300))
  while (( SECONDS < deadline )); do
    if kubectl get "svc/${NOM_NAME}-api" -n "${VALIDATION_NS}" >/dev/null 2>&1 \
      && kubectl get "deploy/${NOM_NAME}-api" -n "${VALIDATION_NS}" >/dev/null 2>&1; then
      break
    fi
    sleep 2
  done
  if ! kubectl get "deploy/${NOM_NAME}-api" -n "${VALIDATION_NS}" >/dev/null 2>&1; then
    echo "timed out waiting for ${NOM_NAME}-api Deployment to be created" >&2
    kubectl get deploy,pods,svc -n "${VALIDATION_NS}" -o wide || true
    return 1
  fi
  wait_jsonpath "API Deployment Available" "5m" \
    --for=condition=Available "deploy/${NOM_NAME}-api" -n "${VALIDATION_NS}"

  log "port-forward + search probe (${SEARCH_QUERY})"
  local pf_pid=""
  kubectl -n "${VALIDATION_NS}" port-forward "svc/${NOM_NAME}-api" 18080:80 >/tmp/nominatim-validation-pf.log 2>&1 &
  pf_pid=$!
  cleanup_pf() { kill "${pf_pid}" >/dev/null 2>&1 || true; }
  trap cleanup_pf RETURN

  # Give port-forward a moment to bind before the first probe.
  sleep 2

  python3 - <<'PY'
import json, time, urllib.error, urllib.request

url = "http://127.0.0.1:18080/search?q=avenue%20pasteur&format=json"
deadline = time.time() + 600
attempt = 0
while time.time() < deadline:
    attempt += 1
    try:
        with urllib.request.urlopen(url, timeout=30) as resp:
            body = resp.read()
            if resp.status == 200:
                data = json.loads(body)
                if isinstance(data, list) and len(data) > 0:
                    print(f"search ok after {attempt} attempt(s); hits={len(data)}")
                    raise SystemExit(0)
    except Exception as exc:  # noqa: BLE001 — retry until deadline
        sleep = min(2 ** min(attempt // 3, 4), 15)
        print(f"attempt {attempt}: {exc}; sleep {sleep}s")
        time.sleep(sleep)
print("search probe failed")
raise SystemExit(1)
PY
}

add_regions() {
  [[ "${RUN_ADD_REGIONS}" == "1" ]] || { log "skipping AddRegions"; return 0; }
  log "patching regions to include europe/andorra"
  kubectl -n "${VALIDATION_NS}" patch nominatiminstance "${NOM_NAME}" --type=merge -p \
    '{"spec":{"regions":["europe/monaco","europe/andorra"]}}'

  local deadline=$((SECONDS + 2400))
  while (( SECONDS < deadline )); do
    while IFS= read -r line; do
      [[ -z "${line}" ]] && continue
      name="${line%%=*}"
      phase="${line#*=}"
      if [[ "${phase}" == "Succeeded" ]]; then
        log "AddRegions ${name} Succeeded"
        return 0
      fi
      if [[ "${phase}" == "Failed" ]]; then
        kubectl get nominatimoperation "${name}" -n "${VALIDATION_NS}" -o yaml || true
        echo "AddRegions Failed" >&2
        return 1
      fi
    done < <(kubectl get nominatimoperation -n "${VALIDATION_NS}" \
      -o jsonpath='{range .items[?(@.spec.type=="AddRegions")]}{.metadata.name}{"="}{.status.phase}{"\n"}{end}' 2>/dev/null || true)
    sleep 10
  done
  echo "timed out waiting for AddRegions" >&2
  return 1
}

rebuild() {
  [[ "${RUN_REBUILD}" == "1" ]] || { log "skipping Rebuild"; return 0; }

  local db="${NOM_NAME}-pg-nominatim" prev_uid
  prev_uid="$(kubectl get database "${db}" -n "${VALIDATION_NS}" \
    -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
  if [[ -z "${prev_uid}" ]]; then
    echo "expected an owned CNPG Database ${db} before Rebuild" >&2
    return 1
  fi
  log "owned CNPG Database ${db} uid before Rebuild: ${prev_uid}"

  log "creating Rebuild NominatimOperation"
  kubectl apply -f - <<EOF
apiVersion: nominatim.zebernst.dev/v1alpha1
kind: NominatimOperation
metadata:
  name: ${NOM_NAME}-rebuild-validation
  namespace: ${VALIDATION_NS}
spec:
  type: Rebuild
  nominatimInstanceRef:
    name: ${NOM_NAME}
  regions:
    - europe/monaco
  image:
    repository: ${WORKER_IMG%:*}
    tag: ${WORKER_IMG##*:}
    pullPolicy: IfNotPresent
EOF


  assert_rebuild_database_replaced "${db}" "${prev_uid}" || return 1

  local deadline=$((SECONDS + 2400))
  while (( SECONDS < deadline )); do
    phase="$(kubectl get nominatimoperation "${NOM_NAME}-rebuild-validation" -n "${VALIDATION_NS}" \
      -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    if [[ "${phase}" == "Succeeded" ]]; then
      log "Rebuild Succeeded"
      return 0
    fi
    if [[ "${phase}" == "Failed" ]]; then
      kubectl get nominatimoperation "${NOM_NAME}-rebuild-validation" -n "${VALIDATION_NS}" -o yaml || true
      echo "Rebuild Failed" >&2
      return 1
    fi
    sleep 10
  done
  echo "timed out waiting for Rebuild" >&2
  return 1
}

# The worker Job may only be armed once the owned CNPG Database has been replaced by a new
# object (fresh uid) reporting status.applied=true, so PostGIS/hstore are reinstalled by
# CNPG on an empty database. Polling can miss a short violation, so this fails fast the
# moment it sees the Job next to the pre-Rebuild uid.
assert_rebuild_database_replaced() {
  local db="$1" prev_uid="$2"
  local op="${NOM_NAME}-rebuild-validation"
  local deadline=$((SECONDS + 900)) uid applied job

  log "waiting for owned CNPG Database ${db} to be replaced before the Rebuild Job"
  while (( SECONDS < deadline )); do
    uid="$(kubectl get database "${db}" -n "${VALIDATION_NS}" \
      -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
    applied="$(kubectl get database "${db}" -n "${VALIDATION_NS}" \
      -o jsonpath='{.status.applied}' 2>/dev/null || true)"
    job="$(kubectl get job "${op}" -n "${VALIDATION_NS}" \
      --ignore-not-found -o jsonpath='{.metadata.name}' 2>/dev/null || true)"

    if [[ -n "${job}" ]]; then
      if [[ "${uid}" == "${prev_uid}" ]]; then
        echo "Rebuild Job armed while the pre-Rebuild Database ${db} was still present" >&2
        return 1
      fi
      if [[ "${applied}" != "true" ]]; then
        echo "Rebuild Job armed before Database ${db} reported status.applied=true" >&2
        return 1
      fi
      log "Rebuild Job armed after Database ${db} was replaced (uid ${uid}, applied=true)"
      return 0
    fi
    sleep 2
  done
  echo "timed out waiting for the Rebuild Job to be armed" >&2
  kubectl get database,jobs,nominatimoperation -n "${VALIDATION_NS}" -o wide || true
  return 1
}

main() {
  ensure_kind
  install_cnpg
  build_and_load
  deploy_operator
  apply_fixture
  wait_bootstrap
  assert_search
  add_regions
  rebuild
  assert_search
  log "validation complete (namespace ${VALIDATION_NS} left running)"
  log "destroy with: kubectl delete ns ${VALIDATION_NS}; make undeploy uninstall"
}

main "$@"
