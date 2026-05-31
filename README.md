# kubectl-fixora

`kubectl-fixora` is a standalone free kubectl plugin for local Kubernetes diagnostics. It does not talk to the Fixora controller/backend. It uses the current kubeconfig, reads local cluster evidence through `kubectl`, and can optionally call AI providers for explanations.

## Scope

- Local incident discovery from Pods, Events, owner references, logs, GitOps annotations, node metadata, and a k8sgpt-style analyzer catalog.
- AI-assisted explanation with redacted evidence.
- Advisory remediation plans for image, resource, runtime, env/config, and scheduling issues.
- Local reports for sharing with a team.
- Cost and prediction helpers from local Kubernetes signals.
- Optional local integrations, custom analyzers, local cache, and local serve mode.
- No cloud service, no Fixora backend integration, and no automatic paid workflow dependency.

## Install

Install the latest GitHub release:

```sh
curl -fsSL https://raw.githubusercontent.com/baka126/fixora-cli/main/scripts/install.sh | sh
kubectl fixora version
```

Install a specific release:

```sh
curl -fsSL https://raw.githubusercontent.com/baka126/fixora-cli/main/scripts/install.sh | VERSION=v0.1.0 sh
```

Or build the binary locally and put it on your `PATH` with the exact name `kubectl-fixora`.

```sh
go build -o kubectl-fixora ./cmd/kubectl-fixora
install -m 0755 kubectl-fixora /usr/local/bin/kubectl-fixora
kubectl fixora version
```

GitHub Actions builds Linux, macOS, and Windows release archives for every `v*` tag and attaches them to the GitHub release with `checksums.txt`.

## Commands

```sh
kubectl fixora status
kubectl fixora doctor -A
kubectl fixora filters
kubectl fixora integrations
kubectl fixora incidents -A --include-logs
kubectl fixora incidents -A --filter Pod,Deployment,Service
kubectl fixora why deployment/api -n prod --proof
kubectl fixora graph deployment/api -n prod -o mermaid
kubectl fixora trace service/api -n prod
kubectl fixora storage -A
kubectl fixora rbac default get secrets -n prod
kubectl fixora dns -n prod
kubectl fixora security -n prod
kubectl fixora node-pressure
kubectl fixora analyze deployment/api -n prod
kubectl fixora explain pod/api-abc123 -n prod --include-logs --ai
kubectl fixora plan deployment/api -n prod
kubectl fixora plan deployment/api -n prod --repo ./charts/api
kubectl fixora diff deployment/api -n prod --proof
kubectl fixora patch deployment/api -n prod --out fixora-patch.yaml
kubectl fixora patch deployment/api -n prod --container api --image ghcr.io/acme/api:v1.2.3 --out fixora-patch.yaml
kubectl fixora patch deployment/api -n prod --preview
kubectl fixora report deployment/api -n prod --include-logs --ai --out report.md
kubectl fixora bundle deployment/api -n prod --out fixora-bundle.tgz
kubectl fixora cost nodes
kubectl fixora predict -A
kubectl fixora lint -f manifests/deployment.yaml
kubectl fixora repo ./charts/api
kubectl fixora validate ./charts/api
kubectl fixora ui -A
kubectl fixora auth set openai "$OPENAI_API_KEY"
kubectl fixora config view
kubectl fixora cache stats
kubectl fixora custom-analyzers add ./my-analyzer
kubectl fixora ai doctor
kubectl fixora ai profiles
kubectl fixora memory list
kubectl fixora serve 127.0.0.1:8089
```

## AI Configuration

AI is disabled unless `--ai` is passed. Credentials can be provided through environment variables or `kubectl fixora auth set`.

```sh
export FIXORA_AI_PROVIDER="openai"
export FIXORA_AI_API_KEY="..."
export FIXORA_AI_MODEL="gpt-4o-mini"
export FIXORA_AI_BASE_URL="https://api.openai.com/v1"
```

Supported provider modes:

- `openai`: OpenAI-compatible `/chat/completions`.
- `ollama`: local Ollama `/api/chat`, no API key required.
- `anthropic`: Anthropic Messages API.
- `noop`: deterministic analyzer output only.

The request includes redacted Kubernetes evidence. The CLI never sends Secret values because it does not read Secret data by default.

## Analyzer Filters

`kubectl fixora filters` lists the analyzer catalog. `--filter` narrows scans:

```sh
kubectl fixora incidents -A --filter Pod,Deployment,Service,Ingress
```

The catalog includes workload, networking, storage, policy, node, Kyverno, and KEDA-style analyzers. Missing CRDs are skipped cleanly.

## High-Impact Workflows

- `why <resource>` gives a concise incident explanation, confidence score, rollback hint, and optional proof.
- `graph <resource>` outputs a dependency graph as text, JSON, YAML, or Mermaid.
- `trace`, `storage`, `rbac`, `dns`, `security`, and `node-pressure` provide focused production debuggers.
- `repo` detects raw, Helm, or Kustomize source mode.
- `validate` renders or dry-runs local source where the required tool is available.
- `patch --preview` shows the fix plan, risk, confidence, blocked reasons, and rollback command without writing files.
- `bundle` creates a redacted audit bundle for sharing.
- `ui` gives a compact terminal incident dashboard without running a server.
- `memory` stores local scenario history so repeated failures can reuse previous context.

## Integrations

`kubectl fixora integrations` detects local optional integrations from cluster objects. It does not call cloud APIs.

- Prometheus service discovery.
- AWS/EKS node provider detection.
- Kyverno `PolicyReport` discovery.
- KEDA `ScaledObject` discovery.

## Custom Analyzers

Custom analyzers are explicit local executables. They are never run automatically. `custom-analyzers run <resource>` sends the selected finding as JSON on stdin and captures stdout/stderr.

```sh
kubectl fixora custom-analyzers add ./scripts/my-check
kubectl fixora custom-analyzers run deployment/api -n prod
```

## Local Serve Mode

`kubectl fixora serve 127.0.0.1:8089` exposes a small local API:

- `GET /healthz`
- `GET /analyzers`
- `GET /incidents`
- `GET /analyze/<kind/name>`

Set `FIXORA_SERVE_TOKEN` to require `Authorization: Bearer <token>`.

## Safety Model

The plugin is intentionally conservative:

- `patch` writes a local patch template.
- `--apply` is rejected unless a future planner marks the patch as concrete and safe.
- GitOps-managed workloads are reported with source-target advice so users patch Helm values or Kustomize overlays instead of rendered YAML.
- Logs are bounded and redacted by default.
- AI results are cached locally when cache is enabled.
- `--paranoid` forces secret-safe redaction behavior.
- `--ai-budget-tokens` prevents accidental expensive AI calls.

## Free vs Paid Boundary

This plugin is designed for a free standalone repository. It should stay independent from the paid Fixora controller/backend. Paid/backend features such as continuous monitoring, Slack approvals, PR creation, closed-loop validation, multi-cluster history, and database-backed learning should remain outside this CLI unless explicitly split into separate enterprise modules later.
