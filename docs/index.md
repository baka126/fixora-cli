---
layout: default
title: Fixora CLI Documentation
description: Local Kubernetes diagnostics, shadow verification, and gated remediation as a kubectl plugin.
---

# kubectl-fixora

`kubectl-fixora` is a standalone kubectl plugin for local Kubernetes incident
diagnosis and remediation. It runs entirely from your machine against the
current `kubeconfig`, does not talk to any Fixora backend, and can optionally
call an AI provider with redacted evidence for explanations.

For install and quick-start, see the [repository README](https://github.com/baka126/fixora-cli#readme).
This page covers architecture, the security model, and the full command surface.

---

## 1. What it does

* **Incident discovery** — reads Pods, Events, owner references, bounded logs,
  GitOps annotations, node metadata, and a k8sgpt-style analyzer catalog, and
  reports failing workloads with a root cause, proof, and rollback hint.
* **AI-assisted explanation** — routes a redacted finding to your configured
  provider (`openai`, `anthropic`, `gemini`, `ollama`, Azure/Bedrock/Vertex
  gateways, or `noop`) and never sends Secret values.
* **Gated remediation** — builds a concrete patch for image, resource, runtime,
  env/config, and scheduling issues, verifies it in an isolated shadow clone,
  then delivers it as a local patch, a cluster apply, or a GitOps pull request.
* **Coordinated fixes** — `coordinate` applies an ordered set of single-resource
  fixes as one transaction with consent-gated partial rollback.

It differs from pure observers like `k8sgpt` by adding the shadow-verification
and delivery layer — every mutating path is gated by
`fix.Plan.ApplyEligible`, a server dry-run, and (for AI-revised patches) a
narrow safe-strategy allowlist.

---

## 2. Architecture

`kubectl-fixora` is stateless and concurrent. It has no daemon and no database.

* **Cluster access** — uses the local `kubeconfig` against the Kubernetes API
  server. The default path shells out to `kubectl`; `--typed-client` switches
  to a `client-go` + controller-runtime stack (typed Pods, Events, Nodes,
  logs, and dynamic resource reads) for large clusters. The `kubectl` path
  stays as the compatible fallback.
* **Analyzer pipeline** — a worker pool fans a finding request out to the
  selected analyzers. Shared reads (pods, events, nodes, resource items) are
  cached behind a mutex so concurrent analyzers do not duplicate API calls.
  Missing CRDs or denied reads become `SkippedCheck` entries, not errors.
* **AI** — the finding is JSON-marshalled, redacted, and POSTed to the provider
  endpoint. AI is off unless `--ai` is passed, and requires `--redact`
  (default for incident commands) or an explicit `--unsafe-ai-no-redact`.

### Diagnostic flow

1. Fetch the target workload (and its owned failing Pod) plus namespace Events.
2. Collect bounded logs when `--include-logs` is set; classify them with a
   deterministic log-signal matcher (permission-denied, exec-format, OOM,
   DNS, TLS, DB-unreachable, panic, …).
3. Assemble evidence, redact it, and — with `--ai` — request a structured
   root-cause and remediation.

### Remediation flow

1. **Plan** — `BuildPlan` maps the finding status to a strategy
   (`image`, `resources`, `runtime`, `env`, `fix-architecture`, `security`,
   `repair-selector`, `hpa`, `pdb`, …) and a patch template.
2. **Concretize** — `--container`, `--image`, `--memory-request`, etc. fill the
   template. A plan that still contains a `TODO_` placeholder is not
   apply-eligible.
3. **Shadow verify** (`--shadow`, default for `fix`) — clone the target Pod or
   the workload's pod template, strip identity (`UID`, `ownerReferences`,
   finalizers, status, node pinning, original labels), inject
   `fixora.io/sandbox=true` plus session/expiry labels, attach an
   ingress-deny NetworkPolicy, apply the patch to the clone, wait for
   readiness, report parity, then tear the clone and NetworkPolicy down.
4. **Deliver** (`--delivery`) — `patch` writes a verified local file, `cluster`
   runs a server-side dry-run then a server-side apply, `pr` commits the
   source patch and opens a GitHub PR / GitLab MR from `--repo`.
5. **Post-apply health gate** — after a cluster apply, Fixora watches the
   rollout (or Job/CronJob completion). If it does not become healthy it
   prints events and cause hints, then offers a deterministic `kubectl` /
   `helm` rollback — never run automatically under `--yes`.

---

## 3. Security model

The plugin is conservative by default.

### Redaction

All evidence sent to an AI provider is scrubbed. The redactor understands
Kubernetes structure:

* **Secrets** — values are removed, keys kept for context
  (`DB_PASSWORD: [REDACTED]`). Fixora does not read Secret `data` unless
  `--secret-keys` is passed, and even then only checks key presence and
  base64 validity.
* **Logs, env, annotations** — regex rules replace URLs with embedded
  credentials, bearer tokens, JWTs, emails, and connection strings.
* `--paranoid` (default for `fix`) forces the strictest redaction.

### Execution guardrails

Before any patch touches a cluster or a shadow sandbox:

* `fix.Plan.ApplyEligible` must be true — concrete, safe, high-confidence,
  no blocked reasons.
* A **server dry-run** must pass.
* AI-revised retry patches must match a narrow strategy allowlist and must
  **not** change identity, metadata (labels/annotations/ownerReferences),
  selectors, scheduling, service accounts, `privileged`, host
  networking/PID/IPC, or volumes.
* Helm/GitOps-managed workloads **refuse direct cluster apply** and are
  routed to `--delivery pr`. For Helm, Fixora render-validates the patch
  against `helm template` and names the `values` key(s) controlling each
  divergent field.
* Shadow NetworkPolicies deny ingress; egress is allowed by default for
  parity and can be blocked with `--shadow-egress deny`.
* Rollback execution is limited to structured `kubectl` / `helm` commands;
  advisory rollback text is never executed.
* `--ai-budget-tokens` caps prompt size; `--timeout`, `--log-tail`, and
  `--max-logs-bytes` bound scans.

### RBAC

Split grants by capability:

* **Diagnostics (read-only)** — `get`, `list`, `watch` on Pods, Events,
  workloads, and the optional CRDs you use.
* **Shadow verification** — `create` / `delete` on `pods` and
  `networking.k8s.io/networkpolicies`.
* **Apply / `coordinate`** — workload write permissions; limit to an
  operator group. `coordinate` mutates more than one resource per run, so
  restrict it to operators already trusted to apply each fix individually.

```yaml
# Explicit shadow-verification Role (example)
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kubectl-fixora-shadow
rules:
  - apiGroups: [""]
    resources: ["pods", "pods/log", "events"]
    verbs: ["create", "delete", "get", "list", "watch"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["networkpolicies"]
    verbs: ["create", "delete", "get", "list", "watch"]
```

A minimal read-only diagnostics Role is in `docs/rbac.yaml`.

---

## 4. Setup

### Prerequisites

* `kubectl` on `PATH` with a valid `kubeconfig`.
* `helm` and/or `kustomize` locally only if you use `--repo` GitOps delivery.
* An AI provider API key only if you use `--ai`.
* Go 1.26+ to build from source.

### Install

```bash
curl -fsSL https://raw.githubusercontent.com/baka126/fixora-cli/main/scripts/install.sh | sh
kubectl fixora version
```

```bash
# from source
go build -trimpath -o kubectl-fixora ./cmd/kubectl-fixora
install -m 0755 kubectl-fixora /usr/local/bin/kubectl-fixora
```

### Environment

```bash
export FIXORA_AI_PROVIDER="gemini"
export FIXORA_AI_API_KEY="..."
export FIXORA_AI_MODEL="gemini-2.0-flash"          # provider default is dated
# export FIXORA_AI_BASE_URL="..."                  # Azure / gateways only

export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4318"   # optional tracing
```

Configuration precedence: CLI flags > environment > config file > defaults.
`kubectl fixora config view` never prints the API key; `config export` redacts
it. Prefer `FIXORA_AI_API_KEY` over `auth set` for production.

---

## 5. Command reference

There is no Cobra; commands dispatch through a `switch` after manual flag
parsing. `kubectl fixora help` shows the incident-focused surface;
`help --advanced` shows everything.

### Fast incident workflow

| Command | Purpose |
|---|---|
| `scan` (alias `incidents`) | List failing workloads with bounded logs, typed reads, and redaction on by default. |
| `why <kind/name>` | Root cause, proof, confidence, rollback hint, next step. `-o json` prints the plan. |
| `fix <kind/name>` | Guided walkthrough: RCA → concrete patch → shadow verify → deliver. Auto-enables `--ai`, `--shadow`, `--paranoid`. |
| `coordinate <kind/name>...` (alias `fix-set`) | Apply an ordered set of fixes as one transaction; fail-closed preflight; consent-gated reverse rollback. `--from <root>` derives the set. |
| `doctor` | Validate access, RBAC, logs, events, metrics, and Helm/GitOps CRDs. |
| `ui` / `ui --tui` | Compact incident dashboard / full-screen Bubble Tea triage view. |
| `cluster` | Full-screen cluster dashboard (also the no-argument default). |

```bash
kubectl fixora scan -A
kubectl fixora why deployment/payments-api -n prod
kubectl fixora fix deployment/payments-api -n prod --container api --image ghcr.io/acme/api:v1.2.3
kubectl fixora fix deployment/payments-api -n prod --repo ./charts/api --delivery pr --yes
kubectl fixora coordinate deployment/payments-api configmap/payments-config -n prod
```

### Delivery

`--delivery patch` (default) leaves a verified local patch. `--delivery
cluster` runs the dry-run and apply, then the health gate. `--delivery pr`
requires `--yes`, commits only the generated patch, pushes it, and opens a
PR/MR when the matching CLI is installed. `--apply`, `--source-patch`, and
`--gitops` are deprecated aliases.

### Specialist workflows

| Group | Tools |
|---|---|
| `debug <tool>` | `trace`, `graph` (text/json/yaml/mermaid), `storage`, `rbac`, `dns`, `security`, `node-pressure`, `changes`, `readiness`, `rollback` |
| `source <tool>` | `repo`, `validate`, `lint`, `preflight`, `policy-check` |

```bash
kubectl fixora debug trace service/payments-api -n prod
kubectl fixora debug graph deployment/payments-api -n prod -o mermaid
kubectl fixora debug rollback deployment/payments-api -n prod --preview
kubectl fixora source validate ./charts/api
kubectl fixora source lint -f manifests/deployment.yaml
```

### Analyzer selection

Fixora picks the right analyzer set automatically (`why service/x` → networking,
`why pvc/x` → storage). `kubectl fixora filters` lists the catalog; `--filter`
forces a set. Two analyzers are off by default because they inspect
Secret/TLS material:

```bash
kubectl fixora incidents -n prod --secret-keys   # key presence + base64; never values
kubectl fixora incidents -n prod --cert-expiry   # Ingress TLS expiry from public tls.crt only
```

### Setup, output, and integrations

| Command | Purpose |
|---|---|
| `auth` | Configure AI provider credentials interactively or directly. |
| `config` | `view` / `set` / `unset` / `validate` / `export`, plus named `profile`s and per-`context` overrides. |
| `ai doctor` | AI provider pre-flight checks. |
| `serve --mcp` | Local MCP stdio server (tools: `analyze`, `incidents`, `health`, `runbook`, `plan-fix`, `preview-fix`, `validate-fix`, `list-resources`, `get-resource`, `get-logs`, `list-events`, `list-filters`, `config`; prompts: `troubleshoot-pod/-deployment/-cluster`, `incident-runbook`). |
| `serve <addr>` | Local HTTP API (`/healthz`, `/analyzers`, `/incidents`, `/analyze/<kind/name>`); set `FIXORA_SERVE_TOKEN` for bearer auth. |
| `integrations` | Detect local Prometheus, EKS, Kyverno, KEDA from cluster objects. |
| `custom-analyzers` | Register and run explicit local analyzer executables (never automatic). |
| `bundle --profile incident\|network\|storage\|security` | Scoped, redacted audit bundle. |
| `watch incidents` | Poll incident state until interrupted. |

Structured incident output (`-o json\|yaml\|markdown\|sarif\|junit\|prometheus`)
uses a stable `AnalysisReport` envelope: `status`, `provider`, `problems`,
`results`, `skipped`, `warnings`, `summary`.

---

## 6. Observability & troubleshooting

### OpenTelemetry

The analyzer worker pool emits nested spans (e.g. `AnalyzePod`) attributed with
`pod.namespace` / `pod.name`, so an incident session correlates directly in
Jaeger, Datadog, or Honeycomb. Set `OTEL_EXPORTER_OTLP_ENDPOINT`.

### Structured logs

Warnings and non-fatal failures go to `stderr`:

* `cleanup failed for pod/… : …` — the namespace RBAC denied shadow deletion;
  the clone carries `fixora.io/expires-at` for manual cleanup.
* Skipped-check lines — an optional analyzer's read was forbidden or the CRD
  is absent; the scan continues with partial results.

### Common errors

| Error | Cause / fix |
|---|---|
| `helm template failed` / `kustomize build failed` | `helm` / `kustomize` not on `PATH`, or the chart/overlay is invalid. Only needed for `--repo` delivery. |
| `direct cluster delivery is blocked for Helm/GitOps-managed resources` | The workload has Helm/Argo/Flux markers. Use `--delivery pr --repo <path>`. |
| `… is not allowed` during shadow verify | The AI patch touched a blocked field (volumes, `privileged`, host networking, selectors, …). Review with `--preview`. |
| `coordinated apply aborted; no changes were made` (exit 2) | A step failed preflight (not apply-eligible, source-managed, or dry-run rejected). Nothing was mutated. |
| `FIXORA_AI_API_KEY is not set` | `--ai` was passed without a configured provider key. |
