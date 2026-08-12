# Changelog

## Unreleased

### Fixed

* **ci/test:** skip Manager + reconcile smoke under `E2E_IMPORT=1`; check `go mod tidy` via `git diff --exit-code`; nil-guard envtest `AfterSuite`; add `timeout-minutes` on lint/test/worker-shell jobs.
* **controller:** seed Gateway API HTTPRoute `rules[].matches` (and related parentRef/backendRef defaults) and preserve existing matches across reconciles so CreateOrUpdate stays idempotent.

### Features

* **images:** add `ghcr.io/zebernst/nominatim-ui` — packages upstream [osm-search/nominatim-ui](https://github.com/osm-search/nominatim-ui/releases) into unprivileged nginx; operator defaults `spec.ui` to this image, supports `spec.ui.enabled`, and sets `NOMINATIM_API_ENDPOINT` from `spec.api.route.hostnames` when present.
* **controller/worker:** `spec.auxData` toggles Wikipedia importance, secondary importance, and US postcode downloads during Bootstrap; `status.auxData` reports observed files via the sequence probe.
* **controller/worker:** implement `NominatimOperation` types `Migrate` and `Freeze` (write-heavy; cannot run beside Update/CatchUp). All CRD Operation types are now implemented.
* **controller/worker:** implement `NominatimOperation` type `Refresh` (`nominatim refresh`, overridable via `NOMINATIM_REFRESH_TASKS`).

### Changed

* **docs:** rewrite operator docs for humans — Getting started, Concepts, Configuration, Database & backup, Operations; add `.github/CONTRIBUTING.md`; slim README and `images/README.md`. Keep `docs/CONTEXT.md` for agents.
* **api:** rename Operation type and `regionChangePolicy` value `Reimport` → `Rebuild`; related impact/env renames (`NOMINATIM_REBUILD_CONFIRM`).
* **controller:** consolidate CNPG Operation side-effects, region observation, sequence probe ensure/observe, and write-plane peer evaluation; serialize write Operations via `status.activeOperationRefs`.
* **api:** drop unused `status.regions[].phase` / `.message` — presence in `status.regions` is the imported source of truth.
* **api:** read-only API serving plane (emptyDir workdir only); default probes on `/status`; typed `spec.nominatim.api` knobs; `spec.api.gunicornWorkers` with cgroup-CPU fallback.
* **worker:** CatchUp uses Update exit codes; AddRegions imports every region in `NOMINATIM_REGIONS` not already bookmarked; shorter `wait_for_postgres` last-mile wait (operator CNPG gate is primary).
* **build:** keep tests out of production image contexts; rename Dockerfiles for editor highlighting where applicable.
* **ci:** enforce `internal/controller` coverage against `.coverage-thresholds.json` (90% floor).
* **test:** bats/shellcheck for worker helpers; envtest manager watch smoke; import e2e for multi-region Bootstrap and Rebuild API scale-down.

## [0.1.1](https://github.com/zebernst/nominatim-operator/compare/0.1.0...0.1.1) (2026-07-27)


### Features

* **api:** define Nominatim v1alpha1 GitOps CRD surface ([eeb217d](https://github.com/zebernst/nominatim-operator/commit/eeb217d6e5ea8f19d94b115908280821cd3ca235))
* **api:** define NominatimOperation v1alpha1 workflow CRD ([eb27daf](https://github.com/zebernst/nominatim-operator/commit/eb27dafb8fec4ed93d0d2b13e2057b1298e14cfa))
* **chart:** package nominatim-operator Helm chart on bjw-s common ([571d6bc](https://github.com/zebernst/nominatim-operator/commit/571d6bcf6f88e4ed174337af15d7b41800405b43))
* **controller:** auto-bootstrap empty Nominatim and sync status.regions ([#6](https://github.com/zebernst/nominatim-operator/issues/6)) ([43be2d8](https://github.com/zebernst/nominatim-operator/commit/43be2d8dac691ef5ac4951db0cb04b53a23d2364))
* **controller:** drive AddRegions/Rebuild from region drift ([#10](https://github.com/zebernst/nominatim-operator/issues/10)) ([040473a](https://github.com/zebernst/nominatim-operator/commit/040473ae46eeb10339c7f54c1913bf576d716dae))
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
