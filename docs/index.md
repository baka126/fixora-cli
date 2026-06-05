---
layout: default
title: Fixora CLI Enterprise Documentation
description: Advanced Kubernetes diagnostic, shadow-verification, and auto-remediation tool.
---

# fixora-cli: Enterprise Documentation Suite

## 1. Introduction & Overview

`fixora-cli` is an advanced Kubernetes diagnostic and remediation tool designed specifically for Site Reliability Engineers (SREs), DevOps practitioners, and Platform Engineers. Built on the robust `antigravity` CLI framework, it operates locally to analyze cluster state, diagnose complex failures, and safely propose—or execute—fixes without requiring a persistent cloud backend.

While it shares diagnostic similarities with tools like `k8sgpt`, `fixora-cli` differentiates itself through **extended, secure-by-default execution capabilities**. It goes beyond mere observation, offering autonomous, AI-driven remediation securely gated by enterprise-grade verification systems.

### Key Features
* **AI-Driven Diagnostics:** Leverages leading LLMs (Gemini, OpenAI, Anthropic, etc.) to analyze Kubernetes events, pod states, and logs to identify root causes.
* **Shadow Verification:** Safely tests generated patches against isolated clones of production workloads before applying them to live environments.
* **Strict Payload Redaction:** Ensures zero sensitive data (Secrets, tokens, PII) is transmitted to AI backends through structural YAML redaction.
* **Automated Rollbacks:** Intelligent rollback generation and execution to instantly undo problematic deployments.
* **OpenTelemetry (OTel) Tracing:** Fully instrumented distributed tracing for high-observability incident debugging.

---

## 2. Architecture & Operational Flow

`fixora-cli` is built to be stateless, highly concurrent, and deeply integrated with the Kubernetes control plane via `client-go`.

### System Architecture
1. **API Interaction:** The CLI uses the local `kubeconfig` to interact directly with the Kubernetes API and etcd. It relies on a fallback mechanism: attempting to use a highly efficient, typed `client-go` implementation, falling back to structured `kubectl` shell executions if necessary.
2. **Analysis Engine:** An internal worker pool concurrently processes cluster namespaces, retrieving Pods, Events, and configurations. 
3. **LLM Backend Integration:** Diagnostics and remediation plans are securely routed to external LLM gateways using the user's configured API provider (e.g., Gemini, Azure OpenAI). All outgoing context is strictly redacted.

### The Diagnostic Flow
1. **Data Fetching:** The `Analyzer` fetches workloads and Kubernetes Events using efficient paginated requests (pinning `ResourceVersion` to ensure etcd consistency).
2. **Context Assembly:** Logs are chunked, stripped of noise, and bundled with pod status and related events.
3. **Redaction:** The engine recursively scrubs mapped secret values, environment variables, and high-entropy strings, preserving only the keys and structure.
4. **AI Prompt Construction:** A specialized prompt is sent to the AI backend, returning a structured JSON or YAML response.

### The Remediation Flow
1. **Patch Generation:** The AI proposes a `strategic-merge` patch.
2. **Shadow Cloning:** If requested, `fixora-cli` strips the failing workload of its identity (`UID`, node affinity, active labels) and deploys an isolated "shadow clone" labeled `fixora.io/sandbox=true`.
3. **Validation Gates:** A strict `NetworkPolicy` isolates the clone (denying ingress/egress). The engine validates the patch locally, completely blocking privilege escalation attempts (e.g., `hostPath`, `secret` mounts).
4. **Verification & Deployment:** The patch is applied to the shadow clone. If it reaches `Ready` state, `fixora-cli` tears down the clone and executes the final patch on the production controller.

---

## 3. Security & Compliance (Enterprise Guardrails)

Security is the foundational principle of `fixora-cli`. It enforces a strict **"do no harm"** operational model.

### Data Privacy & Strict Redaction
By default, all data transmitted to AI providers is scrubbed. The redaction engine understands Kubernetes schemas:
* **Secrets:** Values are stripped, but keys are retained to provide the AI with context (e.g., `DB_PASSWORD: <REDACTED>`).
* **Environment Variables & Logs:** Standard Regex rules catch IPs, emails, hashes, and API keys.

### Execution Guardrails
Before any AI-generated patch touches a cluster (even a shadow sandbox), it must pass cryptographic validation constraints:
* **Privilege Dropping:** Automatically rejects patches attempting to introduce `hostPath`, `hostNetwork`, `hostPID`, or `privileged: true` contexts.
* **Mount Blocks:** Explicitly blocks the addition of new `secret` or `downwardAPI` volume mounts to prevent exfiltration.
* **Identity Preservation:** Patches cannot mutate `metadata.name`, `metadata.namespace`, or core workload selectors.

### RBAC Requirements
To comply with the Principle of Least Privilege, the CLI splits operational needs:
* **Diagnostics (Read-Only):** Requires only `get`, `list`, `watch` on standard resources (Pods, Events, Deployments).
* **Shadow Verification:** Requires explicit `create` and `delete` permissions for `pods` and `networking.k8s.io/networkpolicies`.

```yaml
# Required explicit Shadow Role (example)
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

---

## 4. Installation & Setup

### Prerequisites
* `kubectl` installed and configured.
* A valid `kubeconfig` pointing to your target cluster.
* API key for your chosen LLM provider (if using AI features).

### Installation Commands
Install via the automated script:
```bash
curl -fsSL https://raw.githubusercontent.com/baka126/fixora-cli/main/scripts/install.sh | sh
```
Or build from source using Go 1.21+:
```bash
go build -trimpath -o kubectl-fixora ./cmd/kubectl-fixora
sudo mv kubectl-fixora /usr/local/bin/
```

### Environment Configuration
Export your preferred AI provider configurations (e.g., Gemini):
```bash
export FIXORA_AI_PROVIDER="gemini"
export FIXORA_AI_API_KEY="AIzaSy..."
export FIXORA_AI_MODEL="gemini-1.5-flash"
```
To enable OpenTelemetry tracing to a local collector:
```bash
export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4318"
```

---

## 5. CLI Usage & Command Reference

The CLI utilizes the intuitive structures provided by the `antigravity` framework.

### Global Flags
* `-n, --namespace`: Target a specific namespace.
* `-A, --all-namespaces`: Scan the entire cluster.
* `--ai`: Explicitly enable AI integrations.
* `--shadow`: Execute fixes against isolated sandboxes instead of live targets.

### Core Commands

#### `fixora analyze`
Scans a resource or namespace, performs local heuristic checks, and optionally invokes the AI for a root-cause summary.
**Syntax:**
```bash
kubectl fixora analyze [resource] [flags]
```
**Example:**
```bash
# Analyze a specific deployment with AI insights
kubectl fixora analyze deployment/payments-api -n prod --ai --include-logs
```

#### `fixora patch` & `fixora fix`
Generates a remediation patch. When combined with `--shadow`, it enables autonomous sandbox verification.
**Syntax:**
```bash
kubectl fixora fix [resource] [flags]
```
**Example:**
```bash
# Safely verify an AI-generated fix in a shadow pod before applying
kubectl fixora fix pod/payments-api-1234 -n prod --ai --shadow --delivery patch
```

#### `fixora rollback`
Provides instant deployment rollbacks by parsing controller revisions.
**Syntax:**
```bash
kubectl fixora rollback [resource] [flags]
```
**Example:**
```bash
# Preview the exact shell rollback command
kubectl fixora rollback deployment/payments-api -n prod --preview

# Apply the rollback immediately
kubectl fixora rollback deployment/payments-api -n prod --apply
```

---

## 6. Observability & Troubleshooting

### OpenTelemetry Integration
`fixora-cli` emits rich, context-aware OpenTelemetry (OTel) traces. When the analyzer's worker pool evaluates resources, nested spans are generated:
* Trace names format as `AnalyzePod`.
* Spans are heavily attributed with `pod.namespace` and `pod.name`, ensuring debugging sessions can be directly correlated in tools like Jaeger, Datadog, or Honeycomb.

### Structured Logging
If a shadow verification fails, or if the API rate limits are hit (resulting in pagination re-fetches), the CLI outputs structured logs directly to `stderr`. 
* **Shadow Cleanup Failures:** Look for `[WARN] Failed to delete shadow sandbox...` if the namespace RBAC denies deletion.
* **Pagination Consistency:** Log traces indicate when etcd `ResourceVersion` caching successfully stabilizes highly concurrent API requests.

### Common Errors and Solutions
* **Error:** `helm template failed` or `kustomize build failed`.
  * **Solution:** Ensure `helm` and `kustomize` binaries are installed locally if utilizing GitOps `--repo` targeting.
* **Error:** `volume changes are not allowed` during shadow verify.
  * **Solution:** The AI generated a patch attempting to mount a blocked volume type (`secret` or `hostPath`). Review the generated patch via `--preview` and manually apply safe constraints.
