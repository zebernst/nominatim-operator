# Getting started

Install the operator, create a `NominatimInstance`, wait for the first import, and run a search query.

## Prerequisites

| Dependency | Why |
|------------|-----|
| Kubernetes ≥ 1.32 | Chart `kubeVersion` |
| [CloudNativePG](https://cloudnative-pg.io/) CRDs | Postgres for Nominatim |
| Helm 4 | Chart install |
| Gateway API CRDs | Only if you set `spec.api.route` / `spec.ui.route` |

This chart does **not** install CNPG or Gateway API CRDs. Install those first.

You also need a project PVC (and optionally a flatnode PVC) that worker Jobs can mount. Samples and e2e fixtures assume RWO volumes.

## Install the operator

From OCI (version without a `v` prefix; pin a released chart):

```bash
helm install nominatim-operator \
  oci://ghcr.io/zebernst/charts/nominatim-operator \
  --version 0.1.1 \
  --namespace nominatim-system \
  --create-namespace
```

From a local checkout:

```bash
helm dependency update charts/nominatim-operator
helm install nominatim-operator charts/nominatim-operator \
  --namespace nominatim-system \
  --create-namespace
```

More detail (Flux, CRD upgrade strategy, values): [charts/nominatim-operator/README.md](../charts/nominatim-operator/README.md).

## Create a NominatimInstance

1. Ensure a CNPG `Cluster` exists in the target namespace (or use `spec.database.cluster` to let the operator create a basic one — see [Configuration](configuration.md)).
2. Create a project PVC (and bind it in `spec.project`).
3. Apply a sample or your own CR:

```bash
kubectl apply -f config/samples/nominatim_v1alpha1_nominatiminstance.yaml
```

Validate against the API server (CRDs must already be installed):

```bash
kubectl apply --dry-run=server -f config/samples/nominatim_v1alpha1_nominatiminstance.yaml
kubectl apply --dry-run=server -f config/samples/nominatim_v1alpha1_nominatimoperation.yaml
```

Or via kustomize:

```bash
kubectl kustomize config/samples | kubectl apply --dry-run=server -f -
```

## Wait for Bootstrap

The operator creates a `NominatimOperation` of type `Bootstrap` when the instance needs an initial import. Watch:

```bash
kubectl get nominatiminstance -w
kubectl get nominatimoperation -w
```

Serving (API/UI) starts once Bootstrap has succeeded and imported regions appear on `status.regions` (for region-based installs). Do **not** apply `NominatimOperation` objects through Flux — see [Concepts](concepts.md).

## Probe search

Port-forward the API Service (name follows the instance) and hit Nominatim’s HTTP API:

```bash
kubectl port-forward svc/<instance-name>-api 8080:8080
curl -s 'http://127.0.0.1:8080/search?q=Monaco&format=json' | head
```

Exact Service names and routes depend on your CR; with Gateway API, use the configured hostname instead of port-forward.

## Next steps

- [Configuration](configuration.md) — regions, attach modes, updates schedule
- [Database & backup](database.md) — backup-capable Postgres via `clusterRef`
- [Operations](operations.md) — AddRegions, Update, Rebuild, Freeze, …
