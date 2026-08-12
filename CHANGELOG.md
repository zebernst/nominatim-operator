# Changelog

## Unreleased

### Features

* **controller/worker:** `spec.auxData` toggles Wikipedia importance, secondary importance, and US postcode downloads during Bootstrap staging; `status.auxData` reports observed files via the sequence probe (nominatim-5et.11).
* **controller/worker:** implement `NominatimOperation` types `Migrate` (`nominatim admin --migrate`) and `Freeze` (`nominatim freeze`) per upstream 4.5 Migration / Import docs; both are write-heavy so Update/CatchUp cannot run beside them. All CRD Operation types are now implemented (nominatim-5et.13 / 5et.18).
* **controller/worker:** implement `NominatimOperation` type `Refresh` — worker `refresh.sh` runs `nominatim refresh` (default `--postcodes --word-counts --functions --importance`, overridable via `NOMINATIM_REFRESH_TASKS`) (nominatim-5et.12).

### Changed

* **controller:** collapse CNPG Operation side-effects onto `CNPGEffects` — pause/profile live next to `applyPreJob`/`applyTerminal`; unattached `status.database.mode` no-ops in the shared helpers; Nominatim reconcile no longer owns pause/profile wrappers (nominatim-kfy.2).
* **controller:** observe `status.regions` from Succeeded Operations in one `observeRegionsFromSucceededOps` path; Bootstrap/drift reconcile only ensure creates (nominatim-kfy.3).
* **worker:** split `prepare_db` vs `prepare_import` so Migrate/Freeze/Update/Refresh do not link staging PBF/aux extracts (nominatim-kfy.4).
* **build:** keep tests out of production assets — `.dockerignore` / `.helmignore` exclude `*_test.go`, bats, and `test/` trees; worker image `COPY *.sh` only.
* **controller:** unify write-plane peer evaluation in `evaluateWritePlane` — Operation claim (`Hold` / `RaceWait` / `Ok`) and Nominatim schedule probes (`ScheduleBusy`) share one module; deleted shallow `findConflictingOperation` (nominatim-kfy.1).
* **controller:** harden Operation write-plane mutex — atomic claim via parent `status.activeOperationRefs` (retry-on-conflict); creation-race peers requeue instead of dual terminal `Conflict`; terminal `Conflict` only when a peer is `Running` or has armed a Job.
* **api:** default HTTP startup/readiness/liveness probes on `/status`; typed `spec.nominatim.api` runtime knobs (pool, query/request timeouts, CORS, default language); `spec.api.gunicornWorkers` with entrypoint cgroup-CPU fallback (nominatim-5et.14).
* **test:** kind e2e smoke arms Refresh/Migrate/Freeze Jobs (replacing NotImplemented coverage) and asserts write-heavy Conflict when a peer holds the write plane.
* **docs:** document control / serving / data planes, sequence observation, and RWO project-flatnode vs multi-replica API in the root README, `images/README.md`, and samples (nominatim-5et.35.4).
* **build:** rename image build files to `*.Dockerfile` (`operator.Dockerfile`, `api.Dockerfile`, `worker.Dockerfile`) so editors apply Dockerfile syntax highlighting.
* **api:** read-only serving plane — API Deployment uses an emptyDir workdir only (no project/flatnode PVC mounts); entrypoint takes config from process env, checks `placex`, and starts gunicorn without writing `.env` / `import-finished` or running `nominatim refresh --functions` (nominatim-5et.35.1).
* **ci:** enforce `internal/controller` statement coverage against `.coverage-thresholds.json` (ratcheted to 90.0%; current ~90.6%). `make test` and pre-push run `hack/check-coverage.sh`; CI Test job fails below the floor (nominatim-5et.25).
* **status:** document `status.regions` as Bootstrap-done / imported-set source of truth; worker `require_bootstrap_ready` heals missing `import-finished` when the schema is ready (PVC markers are local bookmarks only; nominatim-5et.35.2).
* **status:** operator-owned sequence probe Job reads project `update/*/sequence.state` into a ConfigMap and merges `status.regions[].sequenceState` (workers do not call the Kubernetes API; nominatim-5et.35.3).
* **test:** bats + shellcheck CI for worker `common.sh` helpers (`detect_continue_at`, `parse_regions`, `seed_project_env`); portable `sed -i.bak` in `seed_project_env` (nominatim-5et.26).
* **test:** envtest starts the manager and asserts the API Deployment appears via watches without calling `Reconcile()` directly (nominatim-5et.29).
* **test:** CI import e2e asserts the API Deployment scales to zero while Reimport is active and restores afterward (`suspendDuringOperations` / Reimport-always-quiesce; nominatim-5et.27).
* **test:** CI import e2e (`make test-e2e-import`) bootstraps `europe/monaco` + `europe/andorra` via multi `--osm-file` (`test/e2e/testdata/nominatim-monaco-andorra.yaml`), asserts both names on `status.regions`, and probes each country with `countrycodes=` so a regions[0]-only import cannot pass. The monaco-only fixture remains for `hack/validate-kind.sh` day-2 AddRegions. See `images/README.md` for the Bootstrap vs AddRegions contract.
* **worker:** `add-regions.sh` now imports every region in `NOMINATIM_REGIONS` (the operator's `Spec.Regions` contract) not already in `imported-regions.txt`, indexing once if any region changed. Removed the `NOMINATIM_IMPORT_MAX_REGIONS` / `NOMINATIM_IMPORT_ONLY_REGION` single-region deferral; the operator (not the worker) now owns AddRegions chunking. Deploy the operator and worker images together — see `images/README.md` for the Spec/`NOMINATIM_REGIONS` contract.
* **worker:** `wait_for_postgres` in `scripts/common.sh` now defaults to 15 attempts at a 2s sleep (~30s, down from ~180s), honoring `NOMINATIM_PG_WAIT_ATTEMPTS` to override the attempt count. The operator's CNPG readiness gate (`cnpgClusterReadyForJobs`) is the primary check before a Job is created; this shortened loop is last-mile only. See `images/README.md` for the readiness split and the mode-aware Bootstrap-done gate (PBF-only vs regions mode).

## [0.1.1](https://github.com/zebernst/nominatim-operator/compare/0.1.0...0.1.1) (2026-07-27)


### Features

* **api:** define Nominatim v1alpha1 GitOps CRD surface ([eeb217d](https://github.com/zebernst/nominatim-operator/commit/eeb217d6e5ea8f19d94b115908280821cd3ca235))
* **api:** define NominatimOperation v1alpha1 workflow CRD ([eb27daf](https://github.com/zebernst/nominatim-operator/commit/eb27dafb8fec4ed93d0d2b13e2057b1298e14cfa))
* **chart:** package nominatim-operator Helm chart on bjw-s common ([571d6bc](https://github.com/zebernst/nominatim-operator/commit/571d6bcf6f88e4ed174337af15d7b41800405b43))
* **controller:** auto-bootstrap empty Nominatim and sync status.regions ([#6](https://github.com/zebernst/nominatim-operator/issues/6)) ([43be2d8](https://github.com/zebernst/nominatim-operator/commit/43be2d8dac691ef5ac4951db0cb04b53a23d2364))
* **controller:** drive AddRegions/Reimport from region drift ([#10](https://github.com/zebernst/nominatim-operator/issues/10)) ([040473a](https://github.com/zebernst/nominatim-operator/commit/040473ae46eeb10339c7f54c1913bf576d716dae))
* **controller:** NominatimOperation Jobs, staging PVC, write-heavy mutex ([#3](https://github.com/zebernst/nominatim-operator/issues/3)) ([eb3ee13](https://github.com/zebernst/nominatim-operator/commit/eb3ee138b5959512e36715a5487614447e7d5e85))
* **controller:** Operation lifecycle side-effects (ActiveOperationRefs, CNPG pause, profiles) ([#8](https://github.com/zebernst/nominatim-operator/issues/8)) ([e0ab05f](https://github.com/zebernst/nominatim-operator/commit/e0ab05f4dd976d374e7ead2c5e8a87bfaf99b5d7))
* **controller:** reconcile API/UI Deployments, Services, HTTPRoutes, PVCs ([#5](https://github.com/zebernst/nominatim-operator/issues/5)) ([3b1477f](https://github.com/zebernst/nominatim-operator/commit/3b1477f2c1f8ba016ed83491425c13e7aa4c3ce9))
* **controller:** reconcile Nominatim status conditions and finalizer ([#1](https://github.com/zebernst/nominatim-operator/issues/1)) ([6dbdd6e](https://github.com/zebernst/nominatim-operator/commit/6dbdd6e27f7c3d354bf1a7537d6bb0aab9a9914b))
* **controller:** schedule-driven Update Operations without CronJobs ([#9](https://github.com/zebernst/nominatim-operator/issues/9)) ([c216b2f](https://github.com/zebernst/nominatim-operator/commit/c216b2f3ddd2be6c55783945c07e87d22c8e054c))
* **images:** add api/worker Dockerfiles and GHCR publish workflow ([11b1a86](https://github.com/zebernst/nominatim-operator/commit/11b1a86499a01cec0dfc4a5575ffbc3ef10bef2f))


### Documentation

* **chart:** refer to Prepare Release workflow by name ([3631a1c](https://github.com/zebernst/nominatim-operator/commit/3631a1c15613a6ab67d52865b277e230cfa42565))


### Miscellaneous Chores

* bootstrap metaswarm, beads tooling, and agent docs ([741a2ce](https://github.com/zebernst/nominatim-operator/commit/741a2cecb3ac431fd218399626e4f1e9cba01235))
* scaffold kubebuilder operator for Nominatim ([b9dad6b](https://github.com/zebernst/nominatim-operator/commit/b9dad6b521206d6dd6f4e2498916eca3ab56c86a))

## Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
