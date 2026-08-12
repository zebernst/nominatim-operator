# Operations

`NominatimOperation` runs a finite worker Job against a `NominatimInstance`. Most Operations are created by the instance controller (Bootstrap, region drift, scheduled Update). You can also apply one with `kubectl` for a one-off.

**Do not** put `NominatimOperation` objects in Flux/GitOps desired state.

Sample: [`config/samples/nominatim_v1alpha1_nominatimoperation.yaml`](../config/samples/nominatim_v1alpha1_nominatimoperation.yaml).

## Types

| Type | What it does |
|------|----------------|
| **Bootstrap** | First import from `spec.regions` (or PBF). Controller creates this when the instance is empty. Multi-region Bootstrap uses one `nominatim import` with multiple `--osm-file` flags — not `add-data`. |
| **AddRegions** | Import regions that are desired but not yet in `status.regions`, without wiping the database. |
| **Rebuild** | Wipe and rebuild (confirm env on the Job). Operator resets an owned CNPG Database first so extensions reinstall on an empty DB. Always scales the API down for the duration. |
| **Update** | Apply Geofabrik/pyosmium diffs for imported regions. |
| **CatchUp** | Loop Update until idle. |
| **Refresh** | `nominatim refresh` admin tasks (postcodes, word counts, functions, importance by default). |
| **Migrate** | `nominatim admin --migrate` after rolling to a newer `nominatim-db`. Stop updates first; prefer suspending the API while migrating. |
| **Freeze** | `nominatim freeze` — drop tables kept only for OSM diffs. Serving continues; further Update/AddRegions that need those structures fail. Do not Freeze before aux data you still plan to load. |

Worker scripts live under `images/worker/scripts/`. Image packaging: [`images/README.md`](../images/README.md).

## Who creates what

| Trigger | Typical Operation |
|---------|-------------------|
| Empty instance with regions | Bootstrap |
| New paths in `spec.regions` | AddRegions (or Rebuild if policy says so) |
| `spec.updates` schedule | Update |
| Manual `kubectl apply` | Any type you set |

## Serving during Operations

- **Bootstrap** — API/UI wait until import is reflected on status (region-based installs).
- **Rebuild** — API is always scaled down until the Operation finishes.
- **AddRegions / Update / CatchUp** — controlled by `spec.api.suspendDuringOperations` (`Never` keeps the API up).
- **Migrate** — prefer suspending serving; upstream advises not serving during upgrades.
- **Freeze / Refresh** — serving usually continues; Refresh must not run in parallel with Update/CatchUp/AddRegions (upstream Nominatim constraint).

## Concurrency

Write-heavy work (Bootstrap, AddRegions, Rebuild, Migrate, Freeze, and conflicting updates) is serialized per instance. A second conflicting Operation ends in `Conflict` or waits and retries, rather than two Jobs writing at once.

Refresh is not treated as write-heavy for that mutex, but still must not overlap update-style work.

## Manual example

```yaml
apiVersion: nominatim.zebernst.dev/v1alpha1
kind: NominatimOperation
metadata:
  name: nominatim-catchup
spec:
  type: CatchUp
  nominatimInstanceRef:
    name: nominatim
```

```bash
kubectl apply -f catchup.yaml
kubectl get nominatimoperation nominatim-catchup -w
```

## Status to watch

- `NominatimOperation` phase: Pending → Running → Succeeded / Failed
- Parent `NominatimInstance` `status.regions` after Bootstrap / AddRegions / Rebuild
- `status.regions[].sequenceState` after successful write Operations (update cursor)

## Related

- [Concepts](concepts.md) — Instance vs Operation, status source of truth
- [Database & backup](database.md) — Freeze before dumps; backup pause around write-heavy work
