# kubectl-fixora

[![Documentation](https://img.shields.io/badge/docs-GitHub_Pages-blue.svg)](https://baka126.github.io/fixora-cli/)

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

The installer places the kubectl plugin binary at `kubectl-fixora` in a directory on your `PATH`. If the selected install directory is not writable, the script will request `sudo`. You can also choose a directory explicitly:

```sh
curl -fsSL https://raw.githubusercontent.com/baka126/fixora-cli/main/scripts/install.sh | INSTALL_DIR="$HOME/.local/bin" sh
```

Make sure the chosen directory is on `PATH`; kubectl discovers plugins by finding `kubectl-fixora`.

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
kubectl fixora health -n prod
kubectl fixora runbook deployment/api -n prod
kubectl fixora readiness deployment/api -n prod
kubectl fixora changes deployment/api -n prod
kubectl fixora plan deployment/api -n prod
kubectl fixora plan deployment/api -n prod --repo ./charts/api
kubectl fixora diff deployment/api -n prod --proof
kubectl fixora patch deployment/api -n prod --out fixora-patch.yaml
kubectl fixora patch deployment/api -n prod --container api --image ghcr.io/acme/api:v1.2.3 --out fixora-patch.yaml
kubectl fixora patch deployment/api -n prod --repo ./charts/api --source-patch
kubectl fixora patch deployment/api -n prod --container api --memory-request 512Mi --cpu-request 250m --memory-limit 1Gi --shadow --delivery patch
kubectl fixora fix statefulset/db -n prod --container db --memory-request 2Gi --cpu-request 500m --memory-limit 4Gi --shadow --delivery pr --repo ./charts/db --branch fixora/db-shadow --pr-base main
kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/acme/api:v1.2.3 --shadow --delivery cluster
kubectl fixora patch deployment/api -n prod --preview
kubectl fixora rollback deployment/api -n prod --preview
kubectl fixora report deployment/api -n prod --include-logs --ai --out report.md
kubectl fixora bundle deployment/api -n prod --profile incident --out fixora-bundle.tgz
kubectl fixora bundle deployment/api -n prod --profile network --out fixora-network.tgz
kubectl fixora cost nodes
kubectl fixora predict -A
kubectl fixora lint -f manifests/deployment.yaml
kubectl fixora policy-check -f manifests/deployment.yaml
kubectl fixora preflight -f manifests/deployment.yaml
kubectl fixora watch incidents -A
kubectl fixora repo ./charts/api
kubectl fixora validate ./charts/api
kubectl fixora ui -A
kubectl fixora ui --tui -A --include-logs
kubectl fixora ui --tui -n prod --include-logs --repo ./charts/api --shadow-retries 1 --pr-base main
kubectl fixora auth set openai "$OPENAI_API_KEY"
kubectl fixora config view
kubectl fixora cache stats
kubectl fixora custom-analyzers add ./my-analyzer
kubectl fixora ai doctor
kubectl fixora ai profiles
kubectl fixora memory list
kubectl fixora serve 127.0.0.1:8089
```

## Config Management

Fixora loads configuration in this order:

```text
CLI flags > environment variables > config file > defaults
```

Inspect the local config without exposing secrets:

```sh
kubectl fixora config view
kubectl fixora config view --resolved
kubectl fixora config view --resolved --show-sources
kubectl fixora config path
```

Validate and manage settings:

```sh
kubectl fixora config validate
kubectl fixora config set timeout 45s
kubectl fixora config set log_tail 80
kubectl fixora config set max_log_bytes 16000
kubectl fixora config set default_output json
kubectl fixora config unset timeout
kubectl fixora config profile create prod
kubectl fixora config profile set prod timeout 45s
kubectl fixora config profile use prod
kubectl fixora config context set prod-us-east namespace platform
kubectl fixora config context set prod-us-east paranoid true
kubectl fixora config export
kubectl fixora config reset
```

`config export` redacts API keys by default. `config view` never prints the API key; it only reports whether a key is set. For production clusters, prefer environment variables for secrets:

```sh
export FIXORA_AI_API_KEY="..."
```

Named profiles let teams keep reusable local/production defaults. Context overrides apply when `--context <name>` is provided, with CLI flags still taking precedence.

`auth set` is convenient for local development, but it stores the AI key in the local config file with `0600` permissions. `config validate` warns when a plaintext key is present.

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
- `groq`: Groq OpenAI-compatible chat completions.
- `localai`: LocalAI OpenAI-compatible chat completions, no API key required by default.
- `customrest`: custom OpenAI-compatible endpoint; set `FIXORA_AI_BASE_URL`.
- `ollama`: local Ollama `/api/chat`, no API key required.
- `anthropic`: Anthropic Messages API.
- `gemini` or `google`: Google Gemini GenerateContent API.
- `azureopenai`: Azure OpenAI deployment endpoint; set `FIXORA_AI_BASE_URL` to the deployment base.
- `cohere`: Cohere Chat API.
- `huggingface`: Hugging Face Inference API.
- `googlevertexai`, `amazonbedrock`, `amazonbedrockconverse`, `amazonsagemaker`, `oci`, `watsonxai`, `ibmwatsonxai`: enterprise/cloud gateway modes; set `FIXORA_AI_BASE_URL` to an authenticated internal proxy or compatible endpoint.
- `noop`: deterministic analyzer output only.

Gemini example:

```sh
export FIXORA_AI_PROVIDER="gemini"
export FIXORA_AI_API_KEY="$GEMINI_API_KEY"
export FIXORA_AI_MODEL="gemini-1.5-flash"
```

Azure OpenAI example:

```sh
export FIXORA_AI_PROVIDER="azureopenai"
export FIXORA_AI_API_KEY="$AZURE_OPENAI_API_KEY"
export FIXORA_AI_BASE_URL="https://<resource>.openai.azure.com/openai/deployments/<deployment>"
```

The request includes redacted Kubernetes evidence. The CLI never sends Secret values because it does not read Secret data by default. JSON, YAML, Markdown, SARIF, JUnit, and Prometheus incident output uses a stable `AnalysisReport` envelope with `status`, `provider`, `problems`, `results`, `skipped`, `warnings`, and `summary` fields.

## Analyzer Filters

`kubectl fixora filters` lists the analyzer catalog. `--filter` narrows scans:

```sh
kubectl fixora incidents -A --filter Pod,Deployment,Service,Ingress
```

The catalog includes workload, networking, storage, policy, node, Kyverno, Trivy, OLM, and KEDA-style analyzers. Fixora also includes K8sGPT-inspired precision checks for Services without ready endpoints, Ingresses with missing backend Services or TLS Secret references, HPA targets and resource requests, PDB disruption blocking, admission webhook backends, Gateway API conditions/backend refs, risky RBAC, risky pod security context, PersistentVolume failures, and multiple default StorageClasses. Missing CRDs or denied reads are skipped cleanly.

For larger production clusters, analyzer reads can use the typed Kubernetes client stack instead of shelling out to `kubectl`:

```sh
kubectl fixora incidents -A --typed-client
```

This path uses `client-go`, dynamic discovery, and a controller-runtime client for typed Pods, Events, Nodes, logs, and generic resource reads. The original `kubectl` path remains available as the default fallback for maximum compatibility.

## MCP

Fixora can run as a local MCP stdio server for AI assistants:

```sh
kubectl fixora serve --mcp
```

Available MCP tools include `analyze`, `incidents`, `health`, `runbook`, `list-resources`, `get-resource`, `get-logs`, `list-events`, `list-filters`, and `config`.

## Cache

Local AI responses are cached by default. Fixora also supports K8sGPT-style remote cache configuration metadata:

```sh
kubectl fixora cache add s3 --region us-east-1 --bucket fixora-cache
kubectl fixora cache add azure --storageacc mystorage --container fixora
kubectl fixora cache add gcs --projectid my-project --bucket fixora-cache
kubectl fixora cache add interplex --endpoint https://cache.internal.example
kubectl fixora cache get
kubectl fixora cache list
kubectl fixora cache purge <key>
kubectl fixora cache remove
```

Remote cache configuration is opt-in because production evidence can be sensitive.

## High-Impact Workflows

- `why <resource>` gives a concise incident explanation, confidence score, rollback hint, and optional proof.
- `runbook <resource>` turns incident evidence into an operator runbook with verify, safe fix, rollback, and warning sections.
- `readiness <resource>` scores whether Fixora has enough evidence for a safe fix.
- `health` summarizes namespace or cluster incident count, skipped checks, severity, and services without endpoints.
- `changes <resource>` surfaces rollout metadata, revisions, checksum/image annotations, and generation signals.
- `rollback <resource> --preview` shows the safest rollback command. `--apply` executes only when a deterministic command exists.
- `graph <resource>` outputs a dependency graph as text, JSON, YAML, or Mermaid.
- `trace`, `storage`, `rbac`, `dns`, `security`, and `node-pressure` provide focused production debuggers.
- `repo` detects raw, Helm, or Kustomize source mode.
- `validate` renders or dry-runs local source where the required tool is available.
- `preflight -f <path>` runs static policy checks and server dry-run before a manifest apply.
- `policy-check -f <path>` runs production policy lint without touching the cluster.
- `patch --preview` shows the fix plan, risk, confidence, blocked reasons, and rollback command without writing files.
- `fix <resource>` uses a structured production remediation plan with confidence gates, rollback, verification commands, and `applyEligible` checks before any live apply.
- `fix <resource> --strategy right-size|repair-selector|add-requests|rollback --repo <path> --source-patch` prefers GitOps source patches for production clusters.
- `patch --repo <path> --source-patch` writes the generated patch into the source repo for GitOps review.
- `patch|fix <resource> --shadow` shows a git-style diff, asks permission, creates an isolated shadow Pod from the target Pod or high-level workload template, applies the patch to the clone, deploys a matching NetworkPolicy, waits for readiness, reports parity, then cleans up.
- `--shadow` supports Pods and high-level Pod-template resources including Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, and CronJob. Helm charts and Kustomize overlays still deliver through `--repo`; Fixora verifies the rendered workload shape by cloning the live template.
- `--delivery patch|cluster|pr` controls what happens after shadow verification. `patch` leaves a verified local patch, `cluster` performs the normal dry-run and final apply confirmation, and `pr` requires `--yes`, writes the source patch, checks for unrelated dirty files, commits only the generated patch path, pushes it, and opens a GitHub PR or GitLab MR when the matching CLI is installed.
- `bundle --profile incident|network|storage|security` creates scoped redacted audit bundles for sharing.
- `ui` gives a compact terminal incident dashboard without running a server.
- `ui --tui` enables the optional interactive Bubble Tea dashboard on demand. It keeps the default output script-friendly while adding a full-screen SRE triage view with incident filtering, command palette, refresh, severity health score, AI root-cause analysis, fix-plan/runbook pane, shadow verification, GitHub/GitLab delivery, owner graph, events, logs, and focused workload/network/storage/security views.
- In the TUI, select a failed Pod, Deployment, StatefulSet, Helm-managed workload, or related incident, press `i` for AI root cause, `f` for the fix plan, `s` to inspect the diff and deploy a shadow clone, then press `a` for direct cluster apply or `p` to review branch/files/diff/remote details before pushing a GitHub PR or GitLab MR from `--repo`.
- `watch incidents` polls incident state until interrupted.
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
- `--apply` is rejected unless the generated patch is concrete and safe.
- Production operators should start with read-only diagnostics (`incidents`, `analyze`, `why`, `health`, `runbook`, `preflight`) and enable mutating paths only for trusted users.
- GitOps-managed workloads are reported with source-target advice so users patch Helm values or Kustomize overlays instead of rendered YAML. Helm source output is advisory unless Fixora can map a patch to chart-native values; review the chart schema and verify with `helm template`.
- Logs are bounded and redacted by default.
- External AI calls receive redacted evidence only when redaction is enabled. Shadow AI retry is disabled when redaction is off.
- AI providers may process logs, events, metadata, and suggested patches; use local/noop providers or disable AI for restricted data environments.
- Production scans can be bounded with `--timeout`, `--log-tail`, and `--max-logs-bytes`.
- AI results are cached locally when cache is enabled.
- `--paranoid` forces secret-safe redaction behavior.
- `--ai-budget-tokens` prevents accidental expensive AI calls.
- `--apply` runs a server-side dry-run first and refuses advisory/TODO patches.
- `--shadow` requires an apply-eligible concrete patch before creating any sandbox resources. Revised AI retry patches are rejected unless they match a narrow safe strategy allowlist and do not change identity, metadata, selectors, scheduling, service accounts, privileged settings, host networking, or volumes.
- Shadow clones strip `UID`, `ownerReferences`, finalizers, status, node pinning, and original labels so Services should not route traffic to the clone.
- Shadow verification injects `fixora.io/sandbox=true`, `fixora.io/original-pod`, `fixora.io/session`, and `fixora.io/expires-at` labels/annotations for audit and cleanup.
- Shadow NetworkPolicies block ingress. Egress is allowed by default for parity and can be blocked with `--shadow-egress deny`.
- `--keep-shadow` is available for debugging, but production use should let Fixora tear down the shadow Pod and NetworkPolicy automatically.
- TUI PR/MR delivery asks for final confirmation with branch, changed files, diff summary, remote, and provider action. The default answer is No.
- Rollback execution is limited to structured `kubectl` and `helm` commands. Advisory rollback text is not executed.
- Use separate RBAC grants for diagnostics, shadow validation, and apply/auto-fix. Diagnostics need read access; shadow validation needs create/delete for Pods and NetworkPolicies; apply/auto-fix needs workload write permissions and should be limited to an operator group.
- Large-cluster scans use bounded worker concurrency and Kubernetes chunking. Scope scans by namespace and filters for incident response when possible.
- `incidents`, `health`, and `ui` return partial results when optional resource checks are forbidden or unavailable, and include skipped checks instead of failing the whole scan.

For production clusters, start from the minimal read-only RBAC example in `docs/rbac.yaml` and remove optional CRD permissions your cluster does not use.

Known unsupported or review-only cases: chart-specific Helm values inference, arbitrary multi-document shadow patches, Service selector rewrites, admission webhook bypasses, scheduling constraint rewrites, service account changes, hostPath/privileged changes, and fixes that require business-specific application config.

## Release Verification

Tagged releases publish checksums, an SPDX SBOM, and keyless Sigstore bundles for release artifacts. Verify downloaded artifacts before installing:

```sh
sha256sum -c checksums.txt
cosign verify-blob kubectl-fixora_v0.2.0_linux_amd64.tar.gz \
  --bundle kubectl-fixora_v0.2.0_linux_amd64.tar.gz.bundle \
  --certificate-identity-regexp 'https://github.com/baka126/fixora-cli/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Free vs Paid Boundary

This plugin is designed for a free standalone repository. It should stay independent from the paid Fixora controller/backend. Paid/backend features such as continuous monitoring, Slack approvals, PR creation, closed-loop validation, multi-cluster history, and database-backed learning should remain outside this CLI unless explicitly split into separate enterprise modules later.
