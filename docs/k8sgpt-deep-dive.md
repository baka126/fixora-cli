# K8sGPT Deep Dive for Fixora CLI

This audit reviews the local `k8sgpt/` clone and identifies what Fixora should import, reference, reimplement, or avoid.

Implementation note: Fixora reimplemented the first wave of K8sGPT-inspired behavior in its own lightweight `kubectl` JSON style. No K8sGPT source files were copied into Fixora.

## License

K8sGPT is Apache-2.0 licensed. Copying code is allowed if Fixora preserves required copyright/license notices and marks modified files. There is no `NOTICE` file in the local clone, but copied source files include K8sGPT copyright headers.

Recommendation: prefer reimplementing analyzer logic in Fixora's lighter `kubectl` JSON style. Copy code only for small, self-contained algorithms, and keep attribution comments if any code is copied substantially.

## Architectural Fit

K8sGPT uses `client-go`, typed Kubernetes APIs, controller-runtime clients, Cobra/Viper, Prometheus metrics, Helm, cloud SDKs, and Go 1.24. Fixora currently stays much lighter: `kubectl` shell calls, local JSON parsing, small standard-library helpers, and conservative production workflows.

Directly importing K8sGPT packages would significantly increase binary size, dependency risk, and release maintenance. The better path is selective feature parity by porting rules and behavior.

## Best Things to Port First

### 1. Analyzer Rules

High-value K8sGPT analyzers that map well to Fixora:

- Pod: pending/unschedulable, evicted, waiting reasons, terminated exit code, readiness failures.
- Service: no endpoints, not-ready endpoints, selector mismatch, service-related warning events.
- Ingress: missing ingress class, missing backend service, missing TLS secret.
- HPA: invalid target kind, missing target, missing resource requests for CPU/memory scaling.
- PDB: disruption budget condition failures and selector expectations.
- PVC/Storage: pending/lost PVC, failed/released PV, multiple default StorageClasses, deprecated provisioner.
- Security: default service account usage, wildcard Role permissions, privileged containers, missing pod security context.
- Webhooks: service missing, no active endpoint pods, risky webhook configuration.
- Gateway/HTTPRoute: unaccepted routes, cross-namespace reference restrictions, missing backend refs.
- OLM/OpenShift: ClusterServiceVersion, Subscription, InstallPlan, CatalogSource, OperatorGroup, ClusterCatalog, ClusterExtension.

Fixora already has broad resource coverage, but many registered-object checks are generic. Porting the above rules will make findings more precise.

### 2. AI Provider Breadth

K8sGPT supports these provider ideas worth copying by behavior:

- Google Gemini / Google GenAI (`google`)
- Google Vertex AI (`googlevertexai`)
- Azure OpenAI
- Cohere
- AWS Bedrock and Bedrock Converse
- SageMaker
- Hugging Face
- OCI GenAI
- Custom REST
- IBM Watsonx
- Groq

Best Fixora order:

1. Gemini (`gemini` or `google`) because the user already asked about it.
2. Azure OpenAI because it is common in enterprise clusters.
3. Custom REST because it supports internal gateways.
4. Groq/Cohere/Hugging Face as lightweight HTTP providers.
5. Bedrock/Vertex later because cloud auth and SDKs add complexity.

Implementation preference: use HTTP APIs where possible instead of importing large SDKs. Keep provider output normalized into Fixora's existing `AIResult`.

### 3. Output Contract

K8sGPT's analysis output includes status, provider, errors, problem count, and results. Fixora should adopt a similar top-level response envelope for machine users:

- `schemaVersion`
- `status`
- `provider`
- `problems`
- `findings`
- `skipped`
- `warnings`

This would improve CI, automation, and incident bots.

### 4. Cache Backends

K8sGPT supports file, S3, Azure Blob, GCS, and Interplex cache backends. Fixora currently has local cache only.

Recommended Fixora path:

- Keep local cache as default.
- Add optional S3/GCS/Azure cache through interfaces and build tags or minimal HTTP/SDK wrappers.
- Avoid remote cache as default because production evidence can be sensitive.

### 5. Custom Analyzer Model

K8sGPT has named custom analyzer registration with URL/port validation. Fixora currently supports local executables. We should add:

- named custom analyzer entries
- duplicate detection
- DNS-safe names
- future HTTP custom analyzer mode

Do not replace local executable analyzers; they are useful and simple.

### 6. MCP Server

K8sGPT has an MCP server exposing tools like analyze, list resources, get logs, list events, config, and filters.

Fixora's local HTTP server is smaller. A Fixora MCP mode would be useful for AI assistants and on-call workflows:

- `serve --mcp`
- tools: `analyze`, `health`, `runbook`, `preflight`, `policy-check`, `get-logs`, `list-events`
- prompts: `troubleshoot-pod`, `troubleshoot-deployment`, `incident-runbook`

This should be implemented separately, not imported directly.

## What We Should Avoid Importing Directly

- Full `pkg/analysis` pipeline: too tied to Viper, client-go, cache interfaces, and progress UI.
- Full analyzer packages: typed Kubernetes dependencies and Prometheus metrics would bloat Fixora.
- Full cache package: cloud SDK surface is large.
- Full server/MCP package: useful design, but it assumes K8sGPT's analysis/config stack.
- Helm chart/operator assets: Fixora is currently a local CLI, not an operator.

## Copy vs Reference Matrix

| Area | Recommendation | Why |
| --- | --- | --- |
| Pod failure reason list | Reimplement | Small, high value, easy to express with Fixora types |
| Service endpoint logic | Reimplement | Fixora already reads services/endpoints |
| Ingress backend/TLS checks | Reimplement | Fits `trace` and `health` workflows |
| HPA/PDB checks | Reimplement | Improves production autoscaling/policy signals |
| Security checks | Reimplement | High production value; keep RBAC-aware partial results |
| OLM/OpenShift analyzers | Reference then reimplement | Valuable but optional and CRD-sensitive |
| Google GenAI/Gemini | Reference then implement HTTP/SDK-light | User-facing provider gap |
| Bedrock/Vertex providers | Reference later | Auth/model complexity |
| Output envelope | Adapt concept | No need to copy code |
| Custom analyzer validation | Copy small regex idea with attribution or reimplement | Self-contained |
| MCP | Reference architecture | Better to build against Fixora commands |

## Immediate Implementation Plan

### Phase 1: Analyzer Precision

Implemented first wave:

- Service endpoints and not-ready endpoints.
- Ingress class/backend service checks.
- HPA scale target and missing resource request checks.
- Storage checks for failed/released PVs and multiple default StorageClasses.
- Security checks for default SA, host namespace usage, privileged containers, and missing `runAsNonRoot`.

Remaining:

- TLS secret existence checks without reading Secret data.
- PDB selector expectation checks.
- Wildcard RBAC checks.
- Webhook backend endpoint checks.
- Gateway/HTTPRoute backend and reference-grant checks.

### Phase 2: Gemini and Provider Expansion

Implemented:

- `gemini` provider with `generateContent`.
- `azureopenai` mode as OpenAI-compatible endpoint with deployment configuration.
- `customrest` provider for internal AI gateways.
- `groq` and `localai` OpenAI-compatible provider modes.

### Phase 3: Output Envelope and Stats

Implemented:

- top-level JSON/YAML/Markdown `AnalysisReport` envelope for incident scans.
- stable API version, status, problem count, skipped checks, and summary.

Remaining:

- `--stats` analyzer timing.
- exit-code contract docs.

### Phase 4: MCP

Add Fixora-native MCP server mode:

- stdio first
- HTTP later
- tools mapped to existing commands

### Phase 5: Optional Remote Cache

Add opt-in remote cache only after secret classification and redaction policies are stricter.

## Attribution Strategy

If we copy any non-trivial K8sGPT source:

- preserve the Apache-2.0 header in the copied/derived file
- add a note that the file contains code derived from K8sGPT
- keep K8sGPT's Apache-2.0 license available in repository documentation
- document modifications in comments or commit messages

Preferred approach remains clean-room style reimplementation from observed behavior, which avoids embedding upstream dependency assumptions.
