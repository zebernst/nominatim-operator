---
title: "Worker control-plane guards → operator + AddRegions chunking"
date: 2026-07-29
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-plan-bootstrap
execution: code
beads_epic: nominatim-b68
origin: "POV + guard inventory (session 2026-07-29); fuller pass = inventory items 1–2 (regions gate + postgres readiness docs/wait) PLUS AddRegions chunking"
gate_iteration: 3
---

# Worker control-plane guards → operator + AddRegions chunking

## Goal Capsule

Move **operator-owned preconditions and AddRegions batching** into the Go controller, keep **bash belts + Nominatim CLI/SQL** in the Job, and fix the AddRegions status lie (`IMPORT_MAX_REGIONS=1` vs multi-region `Spec.Regions`).

**Authorized scope (user “fuller pass”):** inventory items 1–2 **and** AddRegions chunking:
1. Operator fail-early for empty effective regions (AddRegions/Update/CatchUp), plus Bootstrap-done gate in regions mode (R2; bash `IMPORT_FINISHED` remains belt / PBF-only path)
2. Document CNPG readiness as primary gate; shorten Job `wait_for_postgres` as last-mile
3. Operator-driven AddRegions chunking (one missing region per drift Operation) + worker Spec.Regions = exact import set

**Out of scope:** Go/Python worker rewrite; controller-owned `detect_continue_at` / `import_schema_ready`; CatchUp→N Updates; kubectl-in-worker; remediating already-corrupted `status.regions`.

**Stop when:** T1–T10 green; docs updated; `make test` / `make lint` pass for touched packages.

**Authority:** POV hybrid split; guard inventory; beads `nominatim-b68`.

## Product Contract

### Problem

Drift creates AddRegions with **all** missing regions; worker defaults to importing **one**; `syncRegionsFromDriftOps` merges **entire** `Spec.Regions` on Succeeded → false Imported status. Preconditions for regions / Bootstrap-done still live mainly in bash.

### Requirements

- R1. AddRegions / Update / CatchUp: empty `effectiveRegions` → `failOperation`, **no Job**, **no staging PVC**, **no pre-Job CNPG effects**.
- R2. **Regions mode** (`len(parent.Spec.Regions) > 0`): AddRegions / Update / CatchUp require Bootstrap-done before Job create. Bootstrap-done = `len(parent.Status.Regions) > 0` **OR** a peer Bootstrap Operation for this parent is `Succeeded` (covers status-persist race). **PBF-only** (`parent.Spec.Regions` empty): skip this gate; worker `IMPORT_FINISHED` belt remains.
- R3. Drift AddRegions: `Spec.Regions` = first missing region only (serial create unchanged).
- R4. AddRegions Job imports **every** region in effective `NOMINATIM_REGIONS` / Spec, then indexes once. Remove worker deferral. Status sync may keep merging Spec on Succeeded.
- R5. Docs: `cnpgClusterReadyForJobs` is primary readiness; Job wait is last-mile. Default Job wait ≈ 30s (15×2s); honor `NOMINATIM_PG_WAIT_ATTEMPTS` when set.
- R6. Keep bash `die` belts for import-finished / regions.
- R7. Bootstrap / Reimport resume+CLI stay Job-owned. Reimport drift keeps full desired region list (full rebuild, not chunked).

### Acceptance examples

- AE1. `status=[a]`, `spec=[a,b,c]` → drift AddRegions `Regions=[b]` only; after Succeeded + status merge, next op for `c`.
- AE2. Manual AddRegions `Spec.Regions=[b,c]` with Bootstrap done → imports both, indexes once.
- AE3. AddRegions empty effective regions → Failed; no Job; no staging PVC.
- AE4. Update, regions-mode parent, empty status.regions, no Succeeded Bootstrap peer → Failed, no Job.
- AE5. Bootstrap still starts when CNPG ready with shorter Job wait.
- AE6. CatchUp empty effective regions → Failed, no Job.
- AE6b. CatchUp / AddRegions, regions-mode, empty status.regions, no Succeeded Bootstrap → Failed, no Job.
- AE7. Update empty op.Spec.Regions, parent Spec+Status regions set → Job created.
- AE8. PBF-only parent (empty Spec.Regions, empty Status.Regions) + AddRegions with op.Spec.Regions set → not Failed by R2.
- AE9. Regions-mode parent, status.regions still empty, but Bootstrap peer `Succeeded` → AddRegions **not** Failed by R2 (race window).

### Product scope

| In | Out |
|---|---|
| R1–R7 / U1–U4 as authorized fuller pass | Language rewrite; CatchUp rewrite; status repair |

## Planning Contract

### Key technical decisions

- KTD1. Align Spec to work (chunk + import-all); do not parse PVC for status.
- KTD2. Run R1/R2 gates **immediately after conflict check**, before `ensureStagingPVC` and `applyPreJobCNPGEffects` (before Secret/CNPG waits). Use existing `failOperation`.
- KTD3. Mode-aware Bootstrap-done (R2) with Succeeded-Bootstrap alternate signal for the status-persist race. Document PBF-only vs regions mode in `images/README.md` as part of this work (not claimed as pre-existing).
- KTD4. Remove `IMPORT_MAX_REGIONS` / `IMPORT_ONLY_REGION` from `add-regions.sh`. Operator Job env must not set those vars (`buildOperationJob` today does not; keep it that way — no overlay allowlist change required unless those keys are reserved elsewhere).
- KTD5. Do not shorten `cnpgClusterReadyForJobs`.
- KTD6. Ship operator + worker image together for U1; U2 alone is safe with old worker. Note in CHANGELOG.
- KTD7. Reimport is not chunked: it replaces the DB and takes the full desired region set.

### Patterns to follow

- `failOperation` — `nominatimoperation_controller.go`
- Regions fallback — `buildOperationJob` (`nominatimoperation_resources.go` ~238–241) → extract `effectiveRegions`
- Drift — `nominatim_region_drift.go`; status merge — `syncRegionsFromDriftOps`
- Bootstrap sync — `nominatim_bootstrap.go` (`syncRegionsFromBootstrap`)
- Envtest Job absence — conflict tests in `nominatimoperation_controller_test.go`
- Update fixture parent — `minimalNominatim` / BeforeEach must gain regions for happy-path Update/CatchUp cases once R1 lands

### Assumptions

- A1. `effectiveRegions` = op.Spec.Regions if non-empty else parent.Spec.Regions.
- A2. PR #18 Bootstrap multi-file is separate; no Bootstrap script changes required here except wait helper in `common.sh`.
- A3. Forward-fix only for lied status.

### Sequencing

U1 → U2 → U3 → U4 (U3/U4 can parallelize after U1).

### Risks

| Risk | Mitigation |
|---|---|
| Envtest Update cases with empty regions break under R1 | U3 explicitly updates **all** such fixtures / BeforeEach parents |
| Manual AddRegions during status lag | R2 Succeeded-Bootstrap alternate (AE9) |
| Operator before worker image | CHANGELOG + KTD6 |
| Short PG wait | Tunable env; operator still waits Ready+applied |

## Implementation Units

### U1. Worker AddRegions imports entire Spec.Regions set

**Beads:** `nominatim-b68.2`

**Files:** `images/worker/scripts/add-regions.sh`, `images/README.md`, `images/worker/scripts/contract_test.go` (**`package scripts`**, test-only package with no non-test `.go` production code — or single `doc.go` with `package scripts` if required), `CHANGELOG.md` (Unreleased note can land with U1 or U4)

**Approach:** Remove max/only deferral; import every desired region not already listed; index once if changed.

**Tests:**
- T1: `go test` in `images/worker/scripts` reads `add-regions.sh`; asserts no `NOMINATIM_IMPORT_MAX_REGIONS` and no `deferring remaining regions`.
- T1b: asserts `IMPORT_FINISHED` and `NOMINATIM_REGIONS` belts still present.

### U2. Drift one-region AddRegions

**Beads:** `nominatim-b68.2`

**Files:** `internal/controller/nominatim_region_drift.go`, `internal/controller/nominatim_region_drift_test.go`

**Approach:** `Spec.Regions = []string{missing[0]}`; log single region.

**Tests:**
- T2: multi-missing → `Regions=[b]` only.
- T3: Succeeded op merges `b` into status (test sets status or calls sync); re-reconcile → op for `c`.
- T4: existing serial / Bootstrap-active tests still pass.

### U3. Precondition gates (early fail)

**Beads:** `nominatim-b68.1`

**Files:** `internal/controller/nominatimoperation_controller.go`, `internal/controller/nominatimoperation_resources.go`, `internal/controller/nominatimoperation_controller_test.go`, `internal/controller/nominatimoperation_helpers_test.go` (if regions helper tested there)

**Approach:** After conflict, before staging PVC / CNPG effects: R1 then R2 for AddRegions/Update/CatchUp. Share `effectiveRegions` with Job build. Helper `bootstrapComplete(parent, peers)` implements R2 alternate signal.

**Tests:**
- T5: AddRegions empty regions → Failed; no Job; no staging PVC.
- T6: Update regions-mode empty status, no Succeeded Bootstrap → Failed, no Job.
- T6b: CatchUp empty regions → Failed, no Job.
- T6c: AddRegions regions-mode empty status, no Succeeded Bootstrap → Failed, no Job.
- T6d: CatchUp regions-mode empty status, no Succeeded Bootstrap → Failed, no Job.
- T7: AddRegions happy path → Job created.
- T8: Update parent-fallback regions → Job created.
- T9: PBF-only + AddRegions with op regions → not R2-Failed.
- T9b: regions-mode, empty status, Succeeded Bootstrap peer → not R2-Failed (AE9).
- T-fix: Update existing envtest BeforeEach / cases that currently expect Jobs with empty regions — all updated so CI stays green.

### U4. Docs + shorten postgres wait

**Beads:** `nominatim-b68.3`

**Files:** `images/README.md`, `images/worker/scripts/common.sh`, `images/worker/scripts/contract_test.go`, `CHANGELOG.md`

**Approach:** Document primary CNPG gate, PBF-only vs regions mode, AddRegions Spec contract. Default wait attempts = **15** (2s sleep → 30s); support `NOMINATIM_PG_WAIT_ATTEMPTS`.

**Tests:**
- T10: contract test asserts `common.sh` default upper bound is `15` (or explicit `NOMINATIM_PG_WAIT_ATTEMPTS` default path equals 15) and env var name is referenced.

## Verification Contract

- `go test ./internal/controller/ -count=1`
- `go test ./images/worker/scripts/ -count=1`
- `make lint` / `make vet` / `make fmt`
- Coverage thresholds for changed packages
- Optional `make validate-kind` AddRegions smoke
- TDD: failing tests first

## Definition of Done

- [ ] R1–R7 + T1–T10 (+ T-fix) green
- [ ] README: Spec contract, mode-aware Bootstrap gate, postgres readiness split
- [ ] CHANGELOG Unreleased notes operator+worker couple / forward-fix
- [ ] No worker language rewrite
- [ ] Beads `nominatim-b68.1`–`.3` closable

## Appendix

### Ownership after

| Item | Owner |
|---|---|
| Mutex / CNPG pause / API suspend / Reimport DB reset | Operator |
| Regions required + Bootstrap-done (regions mode) | Operator (+ bash belt) |
| AddRegions chunking | Operator drift; worker imports all Spec |
| `wait_for_postgres` | Job last-mile |
| Resume / schema / CLI | Job |
| Reimport region list | Full desired set (not chunked) |

### Bug evidence

- `add-regions.sh` max=1 deferral
- `nominatim_region_drift.go` passes all `missing`
- `syncRegionsFromDriftOps` merges full Spec

### Scope note (gate)

User “fuller pass” = inventory items **1 and 2** (regions precondition + postgres readiness/wait) **plus** AddRegions chunking — not chunking alone. U1 is required so Spec equals imported work for manual multi-region and so status sync stays honest after removing deferral.

### Gate revision notes

- Iter 2: mode-aware R2; CatchUp tests; contract tests; early fail intent.
- Iter 3: Succeeded-Bootstrap race; gates before PVC/CNPG; envtest fixture sweep; package `scripts`; CHANGELOG in U files; Reimport chunking exemption; tightened T10; scope origin clarified for U4/U1.
