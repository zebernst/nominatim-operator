# nominatim-operator

Kubernetes operator for running [Nominatim](https://nominatim.org/) in-cluster.

| Kind | Purpose |
|------|---------|
| `NominatimInstance` (`nominatim.zebernst.dev/v1alpha1`) | GitOps desired state (one top-level CR) |
| `NominatimOperation` | Controller-created finite workflows (Jobs); **not** Flux-driven |

Images: `ghcr.io/zebernst/nominatim-{api,worker,operator,ui}` — API/worker rebuilt for Kubernetes from upstream `nominatim-db` / `nominatim-api`; UI packages [osm-search/nominatim-ui](https://github.com/osm-search/nominatim-ui/releases) (**not** `mediagis/nominatim`). See [`images/README.md`](images/README.md).

## Control, serving, and data planes

The operator keeps Kubernetes orchestration and status separate from Nominatim CLI I/O and from the read-only search API:

| Plane | Components | Owns |
|-------|------------|------|
| **Control** | Operator Deployment | CR status, `NominatimOperation` mutex, CNPG lifecycle, env from `spec.nominatim`, sequence observation |
| **Serving** | API (and optional UI) Deployments | Search/reverse HTTP against Postgres; config from Secrets / process env only |
| **Data / write** | project PVC, optional flatnode PVC, Operation Jobs + short-lived sequence probe Jobs | Import, region growth, updates; worker-local resume markers on the project volume |

**Rules (nominatim-5et.35):**

1. **API is read-only serving** — no project or flatnode mounts; no writes of `.env` / `import-finished`; no `nominatim refresh --functions` (or other admin work) on boot. Admin belongs in Operations (`Refresh` runs `nominatim refresh` in a worker Job).
2. **Workers are Nominatim CLI only** — no Kubernetes client, ServiceAccount tokens for the API server are not part of Operation Jobs. Sequencing into `status.regions` is operator-owned.
3. **CR status is GitOps / cluster truth** — `status.regions` (and Succeeded Bootstrap peers) gate serving and day-2 Jobs. PVC files (`import-finished`, `imported-regions.txt`, `update/*/sequence.state`) are **worker-local bookmarks**, not coordination.
4. **Flatnode is write-plane only** — mount on import/update Jobs when `spec.flatnode` is set; never on the API Deployment.
5. **Sequence state** — after Succeeded Bootstrap / AddRegions / Rebuild / Update / CatchUp, the operator starts a short **sequence probe** Job that read-only mounts the project PVC, writes ConfigMap `{name}-sequence`, and the reconciler merges into `status.regions[].sequenceState`. That string is a **pyosmium / Geofabrik** cursor (`sequenceNumber@timestamp`), not Nominatim `NOMINATIM_REPLICATION_*` lag.

Details, contracts, and shell tests: [`images/README.md`](images/README.md).

### Volumes and API replicas

| Volume | Typical access mode | Who mounts it |
|--------|---------------------|---------------|
| `spec.project` | RWO (samples / e2e PVCs) | Worker Jobs + sequence probe (read-only) |
| `spec.flatnode` (optional) | RWO | Worker Jobs only |
| Operation staging | RWO | Worker Jobs only |
| API workdir | emptyDir | API pods only |

Because the API no longer shares the project/flatnode PVCs, **`spec.api.replicas` may be greater than 1** (stateless serving against Postgres). Keep **project and flatnode on RWO** unless you deliberately design multi-writer storage for Jobs — do **not** treat multi-replica API as a reason to put a shared flatnode on serving pods.

Operation Jobs remain single-writer against those volumes; concurrency is the Operation mutex, not PVC access modes.

## Install

Helm chart: [`charts/nominatim-operator`](charts/nominatim-operator/README.md). Sample CR: [`config/samples/nominatim_v1alpha1_nominatiminstance.yaml`](config/samples/nominatim_v1alpha1_nominatiminstance.yaml).

## Development

```bash
mise install          # Go via .mise.toml
make manifests generate test
```

Prerequisites: Go (see `go.mod`), Docker for image builds, a cluster for e2e (`make test-e2e`, `make test-e2e-import`).

Worker shell tests (bats + shellcheck): `make test-worker-shell` / `make shellcheck-worker`.

## License

Apache License 2.0
