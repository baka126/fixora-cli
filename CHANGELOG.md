# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `coordinate` command (alias `fix-set`): apply an ordered set of single-resource fixes as one transaction, with fail-closed preflight over the whole set and consent-gated reverse-order rollback of the applied prefix on the first failure. `coordinate --from <root kind/name>` derives the related set from the root workload's referenced ConfigMaps, Secrets, mounted PVCs, and selector-matched Services.
- Post-apply health gate: after a `--delivery cluster` apply, Fixora verifies rollout or Job/CronJob completion health, reports events and cause hints on failure, and offers a deterministic `kubectl`/`helm` rollback (never automatic under `--yes`).
- `--secret-keys`: opt-in analyzer for Secret key presence, base64 validity, missing `secretKeyRef`/`envFrom`/`volume` targets, and `imagePullSecrets` type resolution. Never prints Secret values.
- `--cert-expiry`: opt-in Ingress TLS certificate-expiry check that reads only the public `tls.crt`.
- Stuck-`Terminating` Pod detection with the blocking cause attributed to finalizers, a slow preStop hook, a failing volume detach, or an unreachable node.
- Helm delivery now render-validates the intended patch against `helm template` output, classifies each field (managed-divergent / managed-match / unmanaged), and suggests the chart `values` key(s) controlling each divergent field with a `pinpointed` / `likely` / `uncertain` / `unmapped` confidence.
- End-to-end test suites under `test/e2e/` (safety gates + demo scenarios), behind `e2e` / `e2e_delivery` build tags so `make test` stays cluster-free.

### Changed
- Cluster and shadow delivery use server-side apply, so a partial patch merges only the fields it names and never deletes the rest.
- Migrated the core workload analyzers (`ConfigMap`, `DaemonSet`, `StatefulSet`, `PVC`, `Job`, `CronJob`) to the native precision framework.
- `--apply`, `--source-patch`, and `--gitops` are now deprecated aliases for `--delivery cluster` / `--delivery pr`.

### Fixed
- RBAC-denied reads are labelled with a structured `SkippedCheck` field instead of a generic error.

## [0.8.0]

### Added
- Promoted `doctor` and config profiles to top-level commands.
- Interactive terminal UI (dashboard) is now the default when no arguments are provided.
- `fix` is now fully interactive when run without arguments.
- Typed Kubernetes client stack.
- Production auto-fix workflow hardening.
- Local MCP stdio server for AI assistants.
- CI configuration for govulncheck and a macOS runner.
- CONTRIBUTING.md, SECURITY.md, and this changelog.
- Configurable maximum findings in watch mode.
- Progress indicators.

### Changed
- Drastically simplified the CLI by removing redundant subcommands (`plan`, `diff`, `patch`, `report`, etc.) in favor of the unified `fix` and TUI workflows.
- Improved watch mode output and fixed timeout zero-value flag logic.
- Graceful shutdown with signal contexts; Makefile lint and coverage targets.

### Fixed
- Short flags interspersed with positional arguments (e.g. `kubectl-fixora fix deployment/api -n prod`) were mis-parsed; migrated to `pflag`.
- Filtered system resources from the RBAC and webhook analyzers to reduce noise.

## [0.7.5]

### Added
- Native precision analyzer framework replacing basic string-matching for core resources.
- Fully integrated precise logic for `DaemonSet`, `StatefulSet`, `ReplicaSet`, `Job`, and `CronJob` natively.
- Eliminated N+1 API fetching issues by retaining `fixora-cli`'s bulk event indexing while getting deep contextual precision.

## [0.7.4]

### Added
- Added a simplified incident-first CLI workflow with `scan`, `rca`, `repair`, grouped `debug`, and grouped `source` commands.
- Added guided `fix` output that shows RCA, remediation plan, suggested diff, and a concrete next command before any mutation.
- Added smart built-in analyzer selection so Fixora chooses one or multiple relevant analyzers automatically for targeted resources.
- Added fast TUI scan controls with `D` for deep analyzers and `L` for log collection.

### Changed
- Reworked default help output to focus on production incident response and moved the full command surface to `help --advanced`.
- Made the TUI start with a fast pod-incident scan by default instead of scanning every resource and fetching logs.
- Improved CLI ergonomics by allowing flags after positional resources, such as `kubectl fixora fix deployment/api -n prod`.
- Made `--filter` correctly split comma-separated analyzer lists.
- Documented the streamlined incident workflow, fast TUI behavior, and automatic analyzer selection.

### Fixed
- Fixed installer behavior for non-writable install directories by selecting writable PATH locations or requesting `sudo` cleanly.

## [0.7.3]

### Added
- Enterprise-grade documentation suite.
- Top-level controller resolution for targeted pod patching.
- OpenTelemetry span propagation in the concurrent analyzer.

### Fixed
- Fixed bug causing AWK syntax error in release CI workflow.
- Fixed volume validation to explicitly block `secret` and `downwardAPI`.
- Fixed missing trailing newline on repository patch writes.
- Fixed inconsistent etcd pagination by pinning `ResourceVersion`.

