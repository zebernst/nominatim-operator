# Kind validation lab

Canonical Monaco Nominatim fixture (shared with the kind validation lab):

[`../e2e/testdata/nominatim-monaco.yaml`](../e2e/testdata/nominatim-monaco.yaml)

CI import e2e uses a separate multi-region fixture
([`nominatim-monaco-andorra.yaml`](../e2e/testdata/nominatim-monaco-andorra.yaml)) so Bootstrap
exercises multiple `--osm-file` flags. The lab keeps monaco-only Bootstrap and grows coverage
via day-2 **AddRegions** (`RUN_ADD_REGIONS=1`).

## Quick start

```bash
# From repo root — creates/reuses kind, installs CNPG, builds/loads images,
# deploys the operator, imports Monaco, probes search, then AddRegions + Reimport.
make validate-kind
```

Or run the script directly:

```bash
./hack/validate-kind.sh
```

Environment overrides: `OPERATOR_IMG`, `API_IMG`, `WORKER_IMG`, `KIND_CLUSTER`,
`RUN_ADD_REGIONS=0`, `RUN_REIMPORT=0`.

## Reimport database reset

Reimport must start from an empty application database so CloudNativePG reinstalls
PostGIS/hstore as superuser. The controller therefore drops the owned `Database` CR,
recreates it, and only arms the worker Job once the replacement is a different object
(new `metadata.uid`) reporting `status.applied=true`.

Before that drop, the Operation registers itself on `status.activeOperationRefs` and
quiesces the API Deployment (Reimport always suspends, even when
`suspendDuringOperations: Never`) so open connections cannot block `DROP DATABASE`.
While `nominatim.zebernst.dev/reimport-db-reset=pending`, the Nominatim reconciler
skips recreating the owned Database so it does not fight the delete.

That ordering is asserted in three places:

| Where | What it covers |
| --- | --- |
| `internal/controller/nominatimoperation_reimport_test.go` | Every branch of the UID handshake (missing / same-UID / pending / applied / API errors) against a fake client |
| `make test-e2e-import` (CI job **E2E Import**) | Real CNPG on kind: Database UID changes and reports `applied=true` before the Reimport Job exists; API scales to 0 for the duration of Reimport and restores afterward |
| `make validate-kind` | Same invariant in the interactive lab, after AddRegions |

The e2e and lab checks poll, so they can miss a very short-lived violation; they fail
immediately when they do observe a Job next to the pre-Reimport UID, and the Operation's
`nominatim.zebernst.dev/reimport-db-reset` / `-prev-uid` annotations are the deterministic
record that the handshake ran.

## Tear down

```bash
kubectl delete ns nominatim-validation
make undeploy uninstall
# optional: kind delete cluster --name kind
```

This lab never uses the homelab kubeconfig or Flux.
