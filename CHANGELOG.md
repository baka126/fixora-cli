# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
