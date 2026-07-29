# Kind validation lab

Canonical Monaco Nominatim fixture (shared with CI import e2e):

[`../e2e/testdata/nominatim-monaco.yaml`](../e2e/testdata/nominatim-monaco.yaml)

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

## Tear down

```bash
kubectl delete ns nominatim-validation
make undeploy uninstall
# optional: kind delete cluster --name kind
```

This lab never uses the homelab kubeconfig or Flux.
