# nominatim-operator Helm chart

Deploys the [Nominatim](https://nominatim.org/) Kubernetes operator using the
[bjw-s-labs common](https://github.com/bjw-s-labs/helm-charts/tree/main/charts/library/common)
library chart (v5, Kubernetes ≥ 1.32).

## Prerequisites

| Dependency | Why |
|------------|-----|
| **Kubernetes ≥ 1.32** | Chart `kubeVersion`; aligns with common v5 |
| **CloudNativePG (CNPG) CRDs** | Required for full Nominatim features (Postgres clusters owned by Nominatim installs) |
| **Gateway API CRDs** | Required when exposing Nominatim via Gateway API HTTPRoutes |
| **Helm 4** | Chart dependency pulls from the bjw-s Helm repo |

Install CNPG and Gateway API CRDs **before** creating `Nominatim` resources that
reference them. This chart does **not** install those third-party CRDs.

### NominatimOperation and Flux

`NominatimOperation` resources are **controller-created finite workflows** (Jobs).
They must **not** be applied as Flux desired-state (GitOps) objects. Manage only
`Nominatim` CRs via Flux; let the operator create and garbage-collect operations.

## CRDs (Nominatim / NominatimOperation)

Operator CRDs ship in the Helm-native [`crds/`](./crds/) directory (copied from
`config/crd/bases`). Helm installs them on first `helm install` only — it does
**not** upgrade or delete CRDs on upgrade/uninstall.

| Strategy | When to use |
|----------|-------------|
| **`crds/` (default)** | Plain Helm installs; CRDs applied once with the chart |
| **Flux / GitOps** | Prefer a separate Flux `HelmRelease`/`Kustomization` with
  `install.crds: CreateReplace` (or apply CRDs from `config/crd/bases` via
  Kustomize) so CRD upgrades are controlled independently of the operator
  Deployment |

Do **not** also enable a values-driven `rawResources` CRD install unless you
remove files from `crds/` — double-applying CRDs is confusing and error-prone.

## Install

### From OCI (recommended)

Charts are published to GHCR **only on GitHub Releases** created by
[release-please](https://github.com/googleapis/release-please) (semver tags
**without** a `v` prefix, e.g. `0.1.0`). Images are also tagged with that
version; the published chart pins the operator image **digest**.

```bash
helm install nominatim-operator \
  oci://ghcr.io/zebernst/charts/nominatim-operator \
  --version 0.1.0 \
  --namespace nominatim-system \
  --create-namespace
```

Flux (OCI `HelmRepository` + `HelmRelease`):

```yaml
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: zebernst-charts
  namespace: flux-system
spec:
  type: oci
  url: oci://ghcr.io/zebernst/charts
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: nominatim-operator
  namespace: nominatim-system
spec:
  interval: 30m
  chart:
    spec:
      chart: nominatim-operator
      version: "0.1.0" # x-release-please-version
      sourceRef:
        kind: HelmRepository
        name: zebernst-charts
        namespace: flux-system
```

If the GHCR package is private, configure Flux image/chart pull credentials for `ghcr.io`.

### Release flow

1. Land conventional commits on `main` (`feat:`, `fix:`, …).
2. The **Prepare Release** workflow (release-please) opens a PR bumping `Chart.yaml` / changelog / manifest.
3. Merge that PR → GitHub Release + semver tag (no `v`).
4. `release.yaml` builds/pushes images for that version and publishes the chart to
   `oci://ghcr.io/zebernst/charts` (Cosign-signed).

Main-branch image tags (`latest`, branch, SHA) still build for testing; they are
**not** chart OCI releases.

### From a local checkout

```bash
# Fetch the common library dependency
helm dependency update charts/nominatim-operator

# Install (example)
helm install nominatim-operator charts/nominatim-operator \
  --namespace nominatim-system \
  --create-namespace
```

### Dependency repository

`Chart.yaml` depends on:

```yaml
dependencies:
  - name: common
    version: 5.0.1
    repository: https://bjw-s-labs.github.io/helm-charts/
```

`oci://ghcr.io/bjw-s-labs/helm/common` is the intended OCI path for app charts, but
the **common library** chart is not currently pullable from OCI (403). Use the
HTTP repo above until OCI publishes `common`.

## Template / dry-run

```bash
helm dependency update charts/nominatim-operator
helm template test charts/nominatim-operator
```

Expect at least: `Deployment`, `ServiceAccount`, `ClusterRole`,
`ClusterRoleBinding`, plus leader-election `Role` / `RoleBinding` when
`leaderElection.enabled` is true.

## Configuration

Convenience keys (recommended):

| Key | Description | Default |
|-----|-------------|---------|
| `image.repository` | Operator image repository | `ghcr.io/zebernst/nominatim-operator` |
| `image.tag` | Image tag (empty → `Chart.AppVersion` when no digest) | `""` |
| `image.digest` | Image digest (set on OCI chart publish; preferred over tag) | `""` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `replicaCount` | Deployment replicas | `1` |
| `resources` | Container resources | see `values.yaml` |
| `leaderElection.enabled` | Pass `--leader-elect` and create LE RBAC | `true` |
| `watchNamespaces` | Reserved for future namespace scoping; empty = all namespaces | `[]` |

Advanced overrides use [bjw-s common](https://bjw-s-labs.github.io/helm-charts/docs/)
shapes and are deep-merged after convenience defaults:

- `controllers`
- `serviceAccount`
- `rbac`
- `defaultPodOptions`
- `global`

Example image override:

```yaml
image:
  repository: ghcr.io/zebernst/nominatim-operator
  tag: "0.1.0"
  pullPolicy: IfNotPresent

replicaCount: 1

leaderElection:
  enabled: true

resources:
  limits:
    cpu: 500m
    memory: 128Mi
  requests:
    cpu: 10m
    memory: 64Mi
```

Manager binary: command `/manager`, health probe bind address `:8081`
(`liveness` `/healthz`, `readiness` `/readyz`).

## Source of truth for RBAC

Manager ClusterRole rules match `config/rbac/role.yaml`. Leader-election Role
rules match `config/rbac/leader_election_role.yaml`. Regenerate the chart RBAC
section in `templates/_helpers.tpl` when kubebuilder markers change.
