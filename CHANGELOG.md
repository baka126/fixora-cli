# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

## [Unreleased]

### Added
- Typed Kubernetes client stack
- Production auto-fix workflow hardening
- Local MCP stdio server for AI assistants
- CI configuration for govulncheck and macOS runner
- CONTRIBUTING.md, SECURITY.md, and CHANGELOG.md
- Configurable maximum findings in watch mode
- Progress indicators

### Changed
- Improved watch mode output
- Fixed timeout zero-value flag logic
- Update install-local.sh build flags
- Graceful shutdown with signal contexts
- Makefile lint and code coverage targets
