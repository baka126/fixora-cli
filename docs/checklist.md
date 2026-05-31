# kubectl-fixora Plugin Checklist

## Core CLI

- [x] Kubectl plugin binary name: `kubectl-fixora`.
- [x] Standard commands: `status`, `doctor`, `filters`, `integrations`, `incidents`, `analyze`, `explain`, `why`, `graph`, `trace`, `storage`, `rbac`, `dns`, `security`, `node-pressure`, `repo`, `validate`, `plan`, `diff`, `patch`, `report`, `bundle`, `cost`, `predict`, `lint`, `auth`, `config`, `cache`, `custom-analyzers`, `ai`, `memory`, `ui`, `serve`, `version`.
- [x] Works from the user's kubeconfig and selected context.
- [x] Namespace and all-namespace scanning flags.
- [x] JSON, YAML, Markdown, and text-oriented output modes.
- [x] Wide/no-color terminal output controls.
- [x] Compact terminal dashboard via `ui`.

## Diagnostics

- [x] Detect failing Pods, init containers, crash loops, image pull errors, OOM kills, config errors, pending pods, and scheduling issues.
- [x] Read bounded Events and optional bounded logs.
- [x] Resolve basic owner chain from Kubernetes owner references.
- [x] Detect Helm, ArgoCD, Flux, and GitOps hints from labels/annotations.
- [x] Redact tokens, bearer values, JWT-like strings, API keys, passwords, and emails.
- [x] Analyzer registry with selectable filters.
- [x] Broader analyzer catalog for workloads, networking, storage, policy, nodes, Kyverno, and KEDA CRDs.
- [x] One-command troubleshooting via `why`.
- [x] Evidence-first proof mode.
- [x] Dependency graph command with Mermaid output.
- [x] Connectivity, storage, RBAC, DNS, security, and node-pressure debuggers.

## AI

- [x] Optional OpenAI-compatible provider.
- [x] Ollama provider.
- [x] Anthropic provider.
- [x] Noop provider for deterministic/offline mode.
- [x] Environment-driven API key, model, and base URL.
- [x] Local `auth` and `config` commands with validation, redacted export, resolved source view, unset, and reset.
- [x] Structured AI result attached to findings.
- [x] Fallback handling for non-JSON AI responses.
- [x] Local file cache for AI responses.
- [x] AI doctor command.
- [x] Prompt profiles: SRE, security, FinOps, platform, beginner.
- [x] AI budget guard.

## Integrations and Extensibility

- [x] Integration discovery command for Prometheus, AWS/EKS, Kyverno, and KEDA.
- [x] Custom analyzer registration and explicit execution.
- [x] Local HTTP serve mode.
- [x] Local scenario memory.
- [ ] Full MCP protocol implementation.
- [ ] Cloud cache backends such as S3/GCS/Azure.

## Remediation

- [x] Safe local remediation plan generation.
- [x] Patch templates for image, resources, runtime, env/config, and generic failures.
- [x] Guarded apply behavior.
- [x] Patch preview with risk, confidence, guardrails, blocked reasons, and rollback hints.
- [x] Repo mode detection for raw manifests, Helm charts, and Kustomize overlays.
- [x] Dry-run/render validation where `kubectl`, `helm`, or `kustomize` is available.
- [x] PR-ready local branch/commit hooks when explicitly requested.
- [x] Concrete patch generation when the user provides required safe values such as container, image, resources, or ConfigMap key.
- [x] Helm values patch planning and validation entrypoint.
- [x] Kustomize patch planning and validation entrypoint.

## Reports

- [x] Markdown report export.
- [x] Evidence, logs, owner chain, GitOps hints, recommendations, and AI analysis.
- [x] Redacted audit bundle export.

## Future Enhancements

- [ ] client-go mode for richer discovery without relying on `kubectl` subprocesses.
- [x] Manifest/repo mode with server-side dry-run validation for raw manifests where kubectl is available.
- [x] Dependency graph: workload -> service -> endpoints -> config -> secret -> node.
- [x] Local cache for repeated scenarios so recreated broken resources reuse previous recommendations.
- [x] Pluggable analyzers inspired by k8sgpt, but tuned for low-noise incident grouping.
- [x] Optional offline/local LLM provider.
