# Fixora CLI

[![Documentation](https://img.shields.io/badge/docs-GitHub%20Pages-087e78)](https://baka126.github.io/fixora-cli/) [![CI](https://github.com/baka126/fixora-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/baka126/fixora-cli/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/baka126/fixora-cli)](https://github.com/baka126/fixora-cli/releases)

**Diagnose Kubernetes incidents, review the evidence, and deliver guarded fixes from your terminal.** Fixora runs as a local `kubectl` plugin using your kubeconfig. It has no hosted Fixora control plane. Optional AI explanations use a provider you configure.

## Start in minutes

Install the published v0.8.0 release. The installer downloads a platform archive, checks its SHA-256 checksum, and places `kubectl-fixora` on your `PATH`. [Read the installer](https://github.com/baka126/fixora-cli/blob/main/scripts/install.sh) before running a remote script.

```sh
curl -fsSL https://raw.githubusercontent.com/baka126/fixora-cli/main/scripts/install.sh | VERSION=v0.8.0 sh
kubectl fixora version
```

You need `kubectl`, a working kubeconfig, and read access to the resources you investigate. [Get started](https://baka126.github.io/fixora-cli/get-started/) explains context selection and least-privilege RBAC.

```sh
kubectl fixora scan -n prod
kubectl fixora why deployment/api -n prod --proof
kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/example/api:v1.2.3 --preview
```

These commands read cluster evidence, including bounded container logs by default. Add `--include-logs=false` to each command to prevent log reads. The final command previews a plan and does not apply a production patch. AI is optional for diagnosis. Review the [incident workflow](https://baka126.github.io/fixora-cli/guides/incident-workflow/) before choosing shadow verification or a delivery mode.

## What it covers

- **Incident evidence:** Pods, Events, owner relationships, bounded logs by default in incident workflows, and a Kubernetes analyzer catalog.
- **Root-cause workflow:** `scan`, `why`, proof and confidence, optional AI explanation, and concrete patch planning.
- **Controlled delivery:** optional shadow verification followed by a reviewed local patch, cluster apply, or source/PR workflow.
- **Specialist investigations:** network routing, DNS, storage, RBAC, security policy, node pressure, Jobs, Helm/Kustomize source, and optional ecosystem CRDs.
- **Operator tools:** terminal views, structured reports and bundles, local cache, custom analyzers, local HTTP API, and MCP stdio server.

Find a symptom in [Use cases](https://baka126.github.io/fixora-cli/use-cases/), browse the [command and analyzer reference](https://baka126.github.io/fixora-cli/reference/), or read the [safety model](https://baka126.github.io/fixora-cli/safety/).

## Release and development branch

The public documentation describes **v0.8.0** first. Features that only exist on [`codex/production-hardening`](https://github.com/baka126/fixora-cli/tree/codex/production-hardening) are labeled **Upcoming**. Examples include separate cluster `doctor` / `ai doctor`, coordinated multi-resource fixes, MCP shadow opt-in, a no-argument command chooser, and Secret-key checks. Do not copy an Upcoming command into a v0.8.0 incident runbook.

In v0.8.0, top-level `doctor` routes to the AI setup check despite a conflicting cluster-check description in one help view. The [reference](https://baka126.github.io/fixora-cli/reference/commands/) records the exact distinction.

## Build and contribute

Use the Go version declared in [`go.mod`](go.mod). Build locally and run the repository checks:

```sh
go build -o kubectl-fixora ./cmd/kubectl-fixora
go test ./...
python3 -m unittest tests.test_check_docs
python3 scripts/check_docs.py --source docs
```

The Jekyll website source is in [`docs/`](docs/). Documentation changes build and check links in GitHub Actions; only reviewed `main` changes deploy to GitHub Pages. For bugs or missing use cases, [open an issue](https://github.com/baka126/fixora-cli/issues/new).

## Safety in one paragraph

Fixora uses your Kubernetes identity. A scan is a read workflow; shadow verification creates temporary resources; cluster delivery writes to the target workload; PR delivery changes source and can open a remote pull request. AI can send redacted evidence to your configured provider, and v0.8.0 MCP resource/log tools can return sensitive data under broad RBAC. Keep permissions narrow, review diffs and verification status, and treat AI and shadow results as evidence rather than guarantees. [Read the full safety guidance](https://baka126.github.io/fixora-cli/safety/).
