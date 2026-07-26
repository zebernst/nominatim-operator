# nominatim-operator

Kubernetes operator for running [Nominatim](https://nominatim.org/) in-cluster.

Owns the Nominatim lifecycle (bootstrap, region changes, updates, API/UI) via:

| Kind | Purpose |
|------|---------|
| `Nominatim` (`nominatim.zebernst.dev/v1alpha1`) | GitOps desired state (one top-level CR) |
| `NominatimOperation` | Controller-created finite workflows (Jobs); not Flux-driven |

Images (planned): `ghcr.io/zebernst/nominatim-{api,worker,operator}` — not based on `mediagis/nominatim`.

## Status

Scaffolded with [kubebuilder](https://book.kubebuilder.io/) (`domain: zebernst.dev`, `group: nominatim`). CRD fields and controllers are still stubs; implementation is tracked from the consuming homelab epic (`homelab-5et` / memory `nominatim-operator-design`).

## Development

```bash
mise install          # Go via .mise.toml
make manifests generate test
```

Prerequisites: Go 1.23+, Docker (for image builds), access to a Kubernetes cluster for deploy/e2e.

## License

Apache License 2.0
