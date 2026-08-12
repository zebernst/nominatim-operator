# nominatim-operator

Kubernetes operator for running [Nominatim](https://nominatim.org/) in-cluster.

| Kind | Purpose |
|------|---------|
| `NominatimInstance` (`nominatim.zebernst.dev/v1alpha1`) | Desired state for one Nominatim install (GitOps-friendly) |
| `NominatimOperation` | Finite workflow (Job) created by the operator or `kubectl` — **not** a Flux/GitOps object |

Images: `ghcr.io/zebernst/nominatim-{operator,api,worker,ui}` — see [`images/README.md`](images/README.md) for builds.

## Quick start

1. Install [CloudNativePG](https://cloudnative-pg.io/) CRDs (and [Gateway API](https://gateway-api.sigs.k8s.io/) CRDs if you use HTTPRoutes).
2. Install the operator chart — see [Getting started](docs/getting-started.md) and the [Helm chart README](charts/nominatim-operator/README.md).
3. Apply a [`NominatimInstance`](config/samples/nominatim_v1alpha1_nominatiminstance.yaml), wait for Bootstrap, then query the API.

## Documentation

| Doc | For |
|-----|-----|
| [Getting started](docs/getting-started.md) | First install and smoke test |
| [Concepts](docs/concepts.md) | Instance vs Operation, volumes, status |
| [Configuration](docs/configuration.md) | Spec fields, Postgres attach modes, GitOps |
| [Database & backup](docs/database.md) | CNPG backup/restore via `clusterRef` |
| [Operations](docs/operations.md) | Bootstrap, updates, rebuild, freeze, … |
| [Contributing](.github/CONTRIBUTING.md) | Build, test, e2e |
| [Helm chart](charts/nominatim-operator/README.md) | Values, OCI install, Flux notes |

## License

Apache License 2.0
