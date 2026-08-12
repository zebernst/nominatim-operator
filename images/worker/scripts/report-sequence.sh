#!/usr/bin/env bash
# Report Geofabrik sequence.state files for the operator (stdout + ConfigMap data patch).
# Invoked only by the operator-owned sequence probe Job — not by Bootstrap/Update.
# Cluster SoT for lag is Nominatim status.regions[].sequenceState (nominatim-5et.35.3).
set -euo pipefail

PROJECT_DIR="${PROJECT_DIR:-/nominatim}"
UPDATE="${PROJECT_DIR}/update"

if [ ! -d "${UPDATE}" ]; then
  echo '{}'
  exit 0
fi

report="$(
  PROJECT_DIR="${PROJECT_DIR}" python3 - <<'PY'
import json, os, pathlib, re

project = pathlib.Path(os.environ["PROJECT_DIR"])
update = project / "update"
out = {}
if update.is_dir():
    for state in sorted(update.glob("**/sequence.state")):
        region = str(state.parent.relative_to(update))
        text = state.read_text(encoding="utf-8", errors="replace")
        seq_m = re.search(r"^sequenceNumber=(.+)$", text, re.M)
        ts_m = re.search(r"^timestamp=(.+)$", text, re.M)
        if not seq_m:
            continue
        seq = seq_m.group(1).strip()
        if ts_m:
            ts = ts_m.group(1).strip().replace("\\", "")
            out[region] = f"{seq}@{ts}"
        else:
            out[region] = seq
print(json.dumps(out, separators=(",", ":"), sort_keys=True))
PY
)"

aux_report="$(
  PROJECT_DIR="${PROJECT_DIR}" python3 - <<'PY'
import json, os, pathlib

project = pathlib.Path(os.environ["PROJECT_DIR"])

def present(name: str) -> bool:
    path = project / name
    try:
        return path.is_file() and path.stat().st_size > 0
    except OSError:
        return False

print(json.dumps({
    "wikimediaImportance": present("wikimedia-importance.csv.gz"),
    "secondaryImportance": present("secondary_importance.sql.gz"),
    "usPostcodes": present("us_postcodes.csv.gz"),
}, separators=(",", ":"), sort_keys=True))
PY
)"

# Always print for Job logs / debugging.
printf '%s\n' "${report}"

# Best-effort: merge-patch ConfigMap data (preserves operator ownerReferences).
nom_name="${NOMINATIM_NAME:-}"
token_file="${KUBERNETES_SERVICEACCOUNT_TOKEN:-/var/run/secrets/kubernetes.io/serviceaccount/token}"
ca_file="${KUBERNETES_SERVICEACCOUNT_CA:-/var/run/secrets/kubernetes.io/serviceaccount/ca.crt}"
ns_file="${KUBERNETES_SERVICEACCOUNT_NAMESPACE:-/var/run/secrets/kubernetes.io/serviceaccount/namespace}"
api_host="${KUBERNETES_SERVICE_HOST:-}"
api_port="${KUBERNETES_SERVICE_PORT_HTTPS:-${KUBERNETES_SERVICE_PORT:-443}}"

if [ -z "${nom_name}" ] || [ ! -f "${token_file}" ] || [ ! -f "${ns_file}" ] || [ -z "${api_host}" ]; then
  exit 0
fi

ns="$(cat "${ns_file}")"
token="$(cat "${token_file}")"
cm_name="${nom_name}-sequence"
body="$(REPORT_JSON="${report}" AUX_JSON="${aux_report}" python3 - <<'PY'
import json, os
print(json.dumps({"data": {
    "report.json": os.environ["REPORT_JSON"],
    "aux-data.json": os.environ["AUX_JSON"],
}}))
PY
)"

curl -fsS \
  --connect-timeout 5 --max-time 30 \
  -X PATCH \
  -H "Authorization: Bearer ${token}" \
  -H "Content-Type: application/merge-patch+json" \
  --cacert "${ca_file}" \
  -d "${body}" \
  "https://${api_host}:${api_port}/api/v1/namespaces/${ns}/configmaps/${cm_name}" >/dev/null 2>&1 || true

exit 0
