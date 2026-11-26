# Fixora CLI

[![Documentation](https://img.shields.io/badge/docs-GitHub%20Pages-087e78)](https://baka126.github.io/fixora-cli/) [![CI](https://github.com/baka126/fixora-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/baka126/fixora-cli/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/baka126/fixora-cli)](https://github.com/baka126/fixora-cli/releases)

**Find Kubernetes failures, understand their evidence, and verify a proposed repair in isolation before delivery.** Fixora is a local `kubectl` plugin: it uses your kubeconfig and has no hosted control plane. AI is optional and uses the provider you configure.

## Install and try it

The published release is v0.8.0. The installer checks the downloaded archive against its SHA-256 checksum. Review [the installer](https://github.com/baka126/fixora-cli/blob/main/scripts/install.sh) before running it.

```sh
curl -fsSL https://raw.githubusercontent.com/baka126/fixora-cli/main/scripts/install.sh | VERSION=v0.8.0 sh
kubectl fixora version
```

You need `kubectl`, a working kubeconfig, and read access to the resources you investigate. Start with:

```sh
kubectl fixora scan -n prod
kubectl fixora why deployment/api -n prod --proof
kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/example/api:v1.2.3 --preview
```

These commands read cluster evidence; incident workflows read bounded container logs by default. Use `--include-logs=false` to prevent log reads. `--preview` shows a diagnosis and proposed patch only—it does not verify or apply a repair. See [Get started](https://baka126.github.io/fixora-cli/get-started/) and the [incident workflow](https://baka126.github.io/fixora-cli/guides/incident-workflow/).

## How Fixora works

Fixora combines deterministic Kubernetes analysis with optional explanations from your configured AI provider. It does not use K8sGPT as its AI runtime. AI can explain evidence and propose a workload-specific change; Fixora still checks the candidate against its repair rules and evidence. When you choose shadow verification, Fixora tests the candidate in an isolated, default-deny namespace and watches startup, readiness, and applicable failure-specific evidence. A verified result is evidence about that isolated run, not a guarantee about every production condition. Delivery remains a separate reviewed step.

```text
1. Detect the failure and identify the affected workload.
2. Gather live status, events, logs, and related Kubernetes evidence.
3. Review the likely cause and a concrete candidate patch.
4. Validate the candidate against Fixora's repair and safety rules.
5. Verify it in an isolated, default-deny shadow environment.
6. Review the evidence card, limits, and verification result.
7. Choose a separate delivery path, or make no change.
```

After a verified result and successful delivery, text output includes a concise share line. Preview-only, failed, and inconclusive results do not claim a verified repair.

### How Fixora differs from K8sGPT

Fixora uses its own deterministic analyzers and AI provider adapters; K8sGPT is not embedded as Fixora's analysis engine. Fixora's main workflow is to validate a candidate repair and, when requested, test it in an isolated environment before offering a separate delivery step. AI is optional. The CLI is distributed under the Apache License 2.0; any AI provider, cloud resources, or Kubernetes infrastructure you use may have separate costs.

## Disposable demo (development branch)

`kubectl fixora demo` is available in the current development branch, not in the published v0.8.0 release. It creates a run-labeled failing workload in a generated namespace, proposes a repair, and can verify the candidate in a separate shadow namespace. Ingress and egress are denied by default. The demo does not apply a repair to the fixture or deliver it to production; run it only against a disposable test context.

The default probe fixture is available without AI. `config` and `oom` scenarios and up to two AI revisions are also available in this branch. Review the [demo guide](https://baka126.github.io/fixora-cli/testing/disposable-demo/) before running it.

```sh
kubectl fixora demo --preview
kubectl fixora demo probe --yes
kubectl fixora demo oom --yes --ai
```

The preview creates no resources. Run a scenario only in a disposable test context; it creates and cleans up a temporary workload and shadow resources, but does not apply a repair to the fixture or production.

## What it covers

- Pod and controller failures, events, ownership, bounded logs, and a Kubernetes analyzer catalog.
- Evidence-based diagnosis, optional AI analysis, and guarded repair planning.
- Shadow verification followed by a separate local patch, cluster apply, or source/PR delivery path where supported.
- Specialist investigations for routing, DNS, storage, RBAC, security policy, node pressure, Jobs, Helm, and GitOps.
- Operator tools including structured reports, local cache, custom analyzers, a local HTTP API, and an MCP server.

The [use cases](https://baka126.github.io/fixora-cli/use-cases/), [command and analyzer reference](https://baka126.github.io/fixora-cli/reference/), and [safety guide](https://baka126.github.io/fixora-cli/safety/) explain scope and limits.

## Development branch features

The current branch includes features beyond v0.8.0. The references mark these **Upcoming** until a release containing them is published. Examples include ChatGPT plan sign-in, Claude Code/Gemini CLI MCP setup, natural-language Ask, diagnosis-and-Ask live AI checks, coordinated fixes, namespaced policy-bound RBAC repair and receipt-backed revocation, opt-in telemetry, and the Claude Code plugin. Telemetry is disabled unless `--telemetry` is passed with a user-configured HTTPS endpoint. Do not use an Upcoming command from a v0.8.0 install.

The AI guide explains the difference between Fixora's direct API providers and using a signed-in assistant host, with setup and troubleshooting steps for [ChatGPT, Claude Code, Gemini CLI, and `ai doctor`](docs/guides/ai.md). MCP permissions and access boundaries are covered in the [MCP guide](docs/guides/mcp.md).

The development branch's `plugins/fixora` package adds a `/fixora` command for Claude Code. It provides read-only diagnosis and repair previews; it does not enable MCP shadow verification or apply tools. Install it only after installing a release that contains the plugin and confirming your kubeconfig targets the intended cluster. [Plugin package](https://github.com/baka126/fixora-cli/tree/main/plugins/fixora).

## Build and contribute

Use the Go version in [`go.mod`](go.mod). Build and run the repository checks with:

```sh
go build -o kubectl-fixora ./cmd/kubectl-fixora
go test ./...
python3 -m unittest tests.test_check_docs
python3 scripts/check_docs.py --source docs
```

The Jekyll documentation source is in [`docs/`](docs/). Documentation changes are checked in GitHub Actions; only reviewed `main` changes deploy to GitHub Pages. Report bugs or missing use cases in the [issue tracker](https://github.com/baka126/fixora-cli/issues/new).

## Safety

Fixora uses your Kubernetes identity. Scans read cluster state; shadow verification creates temporary resources; cluster delivery writes to the target; PR delivery changes repository source. AI can send redacted evidence to your configured provider. Development builds can send an identifier-free session summary only when explicitly enabled and pointed at a collector you trust. Review permissions, diffs, and verification status, and treat AI and shadow results as evidence rather than guarantees. See the [full safety guidance](https://baka126.github.io/fixora-cli/safety/).
