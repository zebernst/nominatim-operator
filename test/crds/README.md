# Vendored third-party CRDs for envtest

The operator writes CloudNativePG `Cluster`/`Database` and Gateway API `HTTPRoute`
objects as `unstructured.Unstructured` (see `internal/controller/nominatim_database.go`
and `nominatim_workloads.go`) so it does not take a Go dependency on either project.
That means nothing in the operator's own type system checks those payloads — the only
way to catch a misspelled field or a wrong type is to let a real API server validate
them against the upstream schemas.

`internal/controller/suite_test.go` loads this directory alongside
`config/crd/bases`, so envtest specs can `Create`/`Update` the exact shapes the
operator builds and assert the API server accepts them.

envtest starts only `kube-apiserver` + `etcd`. **No CNPG or Gateway API controller
runs**, so these objects are never reconciled: `status` stays empty, no Postgres
pods appear, and no Gateway is programmed. Anything that needs a live controller
belongs in `test/e2e` or `hack/validate-kind.sh`.

## Pins

| File | Source | Version |
| --- | --- | --- |
| `postgresql.cnpg.io_clusters.yaml` | `cloudnative-pg/cloudnative-pg` `config/crd/bases` | `v1.26.1` |
| `postgresql.cnpg.io_databases.yaml` | `cloudnative-pg/cloudnative-pg` `config/crd/bases` | `v1.26.1` |
| `gateway.networking.k8s.io_httproutes.yaml` | `kubernetes-sigs/gateway-api` `config/crd/standard` | `v1.3.0` |

The CNPG pin matches `CNPG_VERSION` in `hack/validate-kind.sh` so unit tests and the
kind validation lab validate against the same schema. Gateway API uses the **standard**
channel, which is where `gateway.networking.k8s.io/v1` `HTTPRoute` — the version
`HTTPRouteGVK` targets — is GA.

## Refreshing

```bash
./hack/update-test-crds.sh
# or with explicit pins
CNPG_VERSION=1.27.0 GATEWAY_API_VERSION=v1.4.0 ./hack/update-test-crds.sh
```

Then update the table above. Bumping CNPG here without bumping
`hack/validate-kind.sh` (and vice versa) lets the two drift apart.
