# Kind validation lab

Shared Monaco fixture: [`../e2e/testdata/nominatim-monaco.yaml`](../e2e/testdata/nominatim-monaco.yaml).

CI import e2e uses [`nominatim-monaco-andorra.yaml`](../e2e/testdata/nominatim-monaco-andorra.yaml) so Bootstrap exercises multiple `--osm-file` flags. This lab keeps monaco-only Bootstrap and grows coverage with day-2 **AddRegions** (`RUN_ADD_REGIONS=1`).

## Quick start

```bash
# From repo root — kind + CNPG + images + operator + Monaco import + optional AddRegions/Rebuild
make validate-kind
```

Or: `./hack/validate-kind.sh`

Environment overrides: `OPERATOR_IMG`, `API_IMG`, `WORKER_IMG`, `KIND_CLUSTER`, `RUN_ADD_REGIONS=0`, `RUN_REBUILD=0`.

## Rebuild note

Rebuild starts from an empty application database so CloudNativePG can reinstall PostGIS/hstore. The controller drops the owned `Database` CR, recreates it, and arms the worker Job only after the replacement is a new object (`metadata.uid` changed) with `status.applied=true`. The API is scaled down for the whole Rebuild so open connections cannot block `DROP DATABASE`.

Covered by unit tests (`nominatimoperation_rebuild_test.go`), `make test-e2e-import`, and this lab. See [docs/operations.md](../../docs/operations.md).

## Tear down

```bash
kubectl delete ns nominatim-validation
make undeploy uninstall
# optional: kind delete cluster --name kind
```

This lab never uses the homelab kubeconfig or Flux.
