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

## [1.0.0](https://github.com/zebernst/nominatim-operator/compare/0.1.1...1.0.0) (2026-08-12)


### ⚠ BREAKING CHANGES

* the v1alpha1 kind "Nominatim" is now "NominatimInstance" and the CRD is nominatiminstances.nominatim.zebernst.dev. Existing manifests must update `kind:` and any RBAC referencing the `nominatims` resource; the singular name is now `nominatiminstance` (shortName `nom` is unchanged).

### api

* rename Nominatim kind to NominatimInstance ([#43](https://github.com/zebernst/nominatim-operator/issues/43)) ([368807f](https://github.com/zebernst/nominatim-operator/commit/368807f58a7f76fc6cc990a4e0cbe11e2eea8f13))


### Features

* **api:** default /status probes and typed runtime knobs ([#35](https://github.com/zebernst/nominatim-operator/issues/35)) ([d90c2f2](https://github.com/zebernst/nominatim-operator/commit/d90c2f2ce74b7ea9ffbb29435d738c9ba6dfc0f6))
* **images:** package upstream nominatim-ui releases (5et.17) ([#45](https://github.com/zebernst/nominatim-operator/issues/45)) ([a214dbf](https://github.com/zebernst/nominatim-operator/commit/a214dbf9d36c40c660929199bd710facfddcda9a))
* kind Monaco validation with declarative CNPG bootstrap ([bbf1544](https://github.com/zebernst/nominatim-operator/commit/bbf15442bd3c6f960b92a5eb127b78d58d8fbce3))
* kind Monaco validation with declarative CNPG bootstrap ([f985d16](https://github.com/zebernst/nominatim-operator/commit/f985d16b10fda4db00a053fe4c7e99e33a28df15))
* **operation:** implement Refresh NominatimOperation (5et.12) ([#36](https://github.com/zebernst/nominatim-operator/issues/36)) ([ae6398e](https://github.com/zebernst/nominatim-operator/commit/ae6398efc982dd2d509fe61db4a2d06005dc2100))
* podSpec overlays and CNPG instance-tune surface ([#17](https://github.com/zebernst/nominatim-operator/issues/17)) ([c98b0c9](https://github.com/zebernst/nominatim-operator/commit/c98b0c9f16c5e3d607e1fa0c6c4425bedc1f379e))
* **status:** operator-owned sequenceState probe ([#33](https://github.com/zebernst/nominatim-operator/issues/33)) ([7e414ef](https://github.com/zebernst/nominatim-operator/commit/7e414ef2fce1ba3f53d4ef06fc5e13c1948fc08f))
* typed Nominatim config surface (spec.nominatim) ([#19](https://github.com/zebernst/nominatim-operator/issues/19)) ([f6aecd2](https://github.com/zebernst/nominatim-operator/commit/f6aecd23cb7893e5d7ac354b40b12ffccde9236e))
* **worker:** download optional aux datasets via spec.auxData ([#39](https://github.com/zebernst/nominatim-operator/issues/39)) ([290ff5c](https://github.com/zebernst/nominatim-operator/commit/290ff5cf0c44543a9b462faf4273dca26cdfeca1))


### Bug Fixes

* **ci:** resolve lint and status conflict failures on PR [#15](https://github.com/zebernst/nominatim-operator/issues/15) ([68f7059](https://github.com/zebernst/nominatim-operator/commit/68f7059a01ab6d9b4e956938de1790821e3b9d67))
* **controller:** drop and recreate CNPG Database before Reimport ([e200125](https://github.com/zebernst/nominatim-operator/commit/e200125965e1d8b36539e1efb55ed5729aa3e5e3))
* **controller:** harden Operation write-plane mutex with atomic claim ([c50a1f3](https://github.com/zebernst/nominatim-operator/commit/c50a1f368dcc4b358d368b2e64875f8ad66ca248))
* **controller:** harden Operation write-plane mutex with atomic claim ([8bc0050](https://github.com/zebernst/nominatim-operator/commit/8bc0050b8cc9942b43bff36c884677f04f35ddb1))
* **controller:** preserve HTTPRoute rules[].matches (5et.32) ([#44](https://github.com/zebernst/nominatim-operator/issues/44)) ([65528b0](https://github.com/zebernst/nominatim-operator/commit/65528b04ac501a2ccd6b7df5eea2a6de25dd92b8))
* move AddRegions chunking and Job preconditions into the operator ([#22](https://github.com/zebernst/nominatim-operator/issues/22)) ([8905a1c](https://github.com/zebernst/nominatim-operator/commit/8905a1cda38608b59ca9307312cb61b210221070))
* **worker:** multi-file Bootstrap import and reject unimplemented ops ([#18](https://github.com/zebernst/nominatim-operator/issues/18)) ([b2c51f7](https://github.com/zebernst/nominatim-operator/commit/b2c51f7ed2a29fcf71316ebbf56f0b5f4f0982c6))


### Documentation

* add domain glossary (CONTEXT.md) ([#41](https://github.com/zebernst/nominatim-operator/issues/41)) ([7b920f8](https://github.com/zebernst/nominatim-operator/commit/7b920f843109b5b26d67a18b49fcc9f4eb794fb9))
* plane separation, volume constraints, and *.Dockerfile names ([#34](https://github.com/zebernst/nominatim-operator/issues/34)) ([705b31a](https://github.com/zebernst/nominatim-operator/commit/705b31ab6c396a6c901439ae3f912aa8f37a18f2))
* rewrite operator docs for human readers ahead of v1 ([#48](https://github.com/zebernst/nominatim-operator/issues/48)) ([c2c99c5](https://github.com/zebernst/nominatim-operator/commit/c2c99c54d4b496ff6491a54d3f6f7381a8f26aa2))


### Miscellaneous Chores

* tidy repo root into build/, images/, and docs/ ([#47](https://github.com/zebernst/nominatim-operator/issues/47)) ([f0829a7](https://github.com/zebernst/nominatim-operator/commit/f0829a7199a40ad25dbc36d1dad7f4202334143e))


### Code Refactoring

* **api:** read-only serving plane without project/flatnode mounts ([#31](https://github.com/zebernst/nominatim-operator/issues/31)) ([c136176](https://github.com/zebernst/nominatim-operator/commit/c1361769cae225a8f0aada4bf370f3a56122e9cf))
* deepen Nominatim architecture (nominatim-kfy) ([#40](https://github.com/zebernst/nominatim-operator/issues/40)) ([42eb89e](https://github.com/zebernst/nominatim-operator/commit/42eb89ec5c81bd823a3f5d78995eaa3c3f713d5c))
* **status:** treat status.regions as Bootstrap-done source of truth ([#32](https://github.com/zebernst/nominatim-operator/issues/32)) ([000e669](https://github.com/zebernst/nominatim-operator/commit/000e66952c86945859a8ef2d80048525e238241c))

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
