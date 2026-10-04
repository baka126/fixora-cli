# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Include evidence-based diagnosis and available AI explanation in structured why/fix output.
- Provide AI with bounded live workload configuration and display diagnostic commands and caveats.
- `fix --startup-baseline <manifest>` restores command/args from an explicit matching Deployment baseline, requires shadow verification, and prevents AI revisions of the baseline.
- Infer database endpoint ConfigMap references from an explicit workload source annotation after checking current failure, ownership, command consumption, and ready Service endpoints.

### Fixed
- Evaluate AI repair proposals even when a deterministic candidate exists; validate their actual change fields independently of the initial symptom or known patch templates.
- Preserve model confidence and require shadow verification for AI-generated cluster repairs, including quick mode. Keep rejected alternatives from replacing a usable deterministic repair.
- Explain proven probe endpoint failures without AI while declining ambiguous historical event attribution.
- Decode merged shadow patches into a fresh Pod so removing an environment literal cannot leave stale values behind.
- Scope server-side apply ownership to each repair proposal so partial manifests do not inherit omitted fields from earlier repairs.
- Report already healthy workloads as requiring no repair.
- Preserve the OOM root cause during CrashLoopBackOff; infer memory headroom from proven stress allocations or memory-backed dd writes when metrics are absent or clipped.
- Infer a narrowly scoped writable emptyDir cache mount for proven read-only filesystem failures while retaining non-root and read-only controls; require shadow verification for delivery.
- Honor `--yes` during verified cluster delivery and retain noninteractive rollback refusal.
- Honor `--yes` for shadow deployment confirmation while retaining patch and delivery validation.
- Request typed Gemini remediation JSON and reject truncated responses; surface successful AI explanations in diagnosis output.
- Key AI cache entries by workload evidence, provider, model, endpoint, and prompt profile.
- Bind AI patches to the generated workload identity before safety validation or review-only delivery; reject plans without canonical identity.
- Retry transient AI failures with jittered exponential backoff and honor `Retry-After` within the caller’s deadline.
- Stop the fix walkthrough when no concrete repair exists; show a compact diagnosis with command, exit evidence, recent log, and next action. Full evidence is opt-in through `--proof` or `--verbose`.
- Exclude unrelated namespace/category matches from AI evidence; require a workload, pod, owner, or referenced-node relationship.
- Include configured container commands, arguments, and observed exit codes in crash-loop diagnostics.
- Treat successful empty EndpointSlice lists as authoritative, avoiding unnecessary deprecated Endpoints reads during analysis.
- Combine Gemini answer parts while excluding thought parts; distinguish empty, invalid JSON, and missing-explanation AI responses without printing raw provider output.

## [0.8.1]

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

