# Contributing

Thanks for contributing to nominatim-operator.

## Prerequisites

- Go version from `go.mod` (install via [mise](https://mise.jdx.dev/): `mise install`)
- Docker (image builds)
- Optional: kind, bats-core, shellcheck for e2e / worker shell tests

## Build and test

```bash
mise install
make manifests generate test
make lint
make vet
make fmt
```

Coverage for `internal/controller` is enforced against `.coverage-thresholds.json` (90% floor). `make test` writes `build/cover.out` and runs `hack/check-coverage.sh`. `make clean` removes `build/`.

Worker shell:

```bash
make test-worker-shell
make shellcheck-worker
```

## Git hooks

Pre-push checks live in `.githooks/pre-push` (fmt, vet, lint, coverage):

```bash
git config core.hooksPath .githooks
```

## End-to-end and kind lab

- CI import e2e: `make test-e2e-import`
- Interactive kind lab: `make validate-kind` — see [`test/validation/README.md`](../test/validation/README.md)
- Vendored CNPG/Gateway CRDs for envtest: [`test/crds/README.md`](../test/crds/README.md)

## Docs

Operator docs live under [`docs/`](../docs/). Image build notes: [`images/README.md`](../images/README.md). Chart: [`charts/nominatim-operator/README.md`](../charts/nominatim-operator/README.md).

## Agents

If you use coding agents in this repo, see [`AGENTS.md`](../AGENTS.md) and domain vocabulary in [`docs/CONTEXT.md`](../docs/CONTEXT.md).

## License

Contributions are under the Apache License 2.0.
