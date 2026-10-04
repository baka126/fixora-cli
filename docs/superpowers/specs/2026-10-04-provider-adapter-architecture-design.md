# Provider Adapter Architecture Design

## Goal

Make Fixora's optional AI explanation and remediation path work predictably across provider APIs and models, including current and future models from OpenAI, Google, and Anthropic, as well as the provider and gateway backends Fixora advertises. A model/API mismatch must produce an actionable diagnostic rather than silently degrading to an unhelpful deterministic fallback. AI remains advisory: deterministic findings, evidence checks, shadow verification, policy gates, and apply controls remain authoritative.

## Current behavior and problem

`internal/ai/openai.go` currently combines configuration defaults, provider selection, HTTP transport, request serialization, response parsing, retries, and JSON-contract parsing in one `Client`. It has branches for several distinct API shapes, while some gateway branches route through the OpenAI Chat Completions shape. Model capabilities are inferred from provider IDs, and OpenAI strict JSON Schema is applied only to the provider ID `openai`. Anthropic and Gemini have their own response formats, but all responses ultimately pass through a permissive content parser that converts invalid JSON into an unstructured result.

This makes it difficult to tell whether a failure comes from model access, an endpoint mismatch, unsupported structured output, a rejected sampling parameter, transport failure, or malformed model output. It also makes adding future APIs likely to add more conditionals to the same file. The recent OpenAI incident—only `gpt-4o-mini` accepted the configured request while another model failed or returned unparsable content—illustrates the need to represent API protocol and capability differences explicitly. The separate case where a workload has become healthy must continue to be handled as a live-state result, not as an AI/provider error.

## Goals and non-goals

### Goals

- Support protocol-specific integrations for OpenAI, Google Gemini, and Anthropic, with current and future models selected by configuration rather than a stale built-in model allowlist.
- Support first-party cloud endpoints and common gateways, including Azure OpenAI, Google Vertex AI, AWS Bedrock, and OpenAI-compatible endpoints. Existing configured backends should be inventoried and either retained with a tested adapter or explicitly marked unsupported with a migration path.
- Keep provider-specific request, authentication, endpoint, output-format, response, and error behavior behind small independently testable adapters.
- Express optional capabilities (for example, strict JSON Schema output, JSON mode, temperature, and token limits) as capabilities, not assumptions based only on model names.
- Parse and validate the shared Fixora `AIResult` contract consistently. Do not present malformed or incomplete output as a valid AI diagnosis; retain deterministic fallback behavior and explain the failure clearly.
- Provide safe diagnostics for provider/model, endpoint protocol, HTTP status, and provider error code/parameter when available, without exposing API keys, full request bodies, raw cluster evidence, or potentially sensitive provider response text.
- Preserve timeouts, bounded response sizes, retry limits, cancellation, redaction behavior, cache identity, CLI compatibility, and safety controls.
- Make adding a supported protocol or provider preset possible without changing the remediation planner or CLI output model.

### Non-goals

- Guarantee access to every model regardless of account, region, project entitlement, provider outage, rate limit, or service policy.
- Promise every vendor model supports every structured-output feature.
- Build a hosted model router or require users to deploy LiteLLM. An OpenAI-compatible proxy may be configured as a gateway.
- Copy K8sGPT as a dependency or copy its provider implementation wholesale. Its architecture and selected code may be referenced; any copied code requires Apache-2.0 license and attribution compliance review.
- Change deterministic diagnosis, cluster permissions, approval flow, patch safety, or workload behavior as part of the provider subsystem.

## Approaches considered

1. **One adapter per vendor and hosted service.** This gives maximum control over each API but duplicates behavior and increases maintenance as endpoints and models evolve.
2. **Protocol adapters plus provider presets (recommended).** Implement adapters for genuinely distinct wire protocols and keep vendor endpoint/auth configuration in provider presets. OpenAI-compatible services share an adapter where the compatibility contract is real; Anthropic Messages, Gemini generateContent, and cloud-specific APIs use their own adapters. Capabilities are negotiated/configured per adapter and model, and unsupported features are omitted or downgraded deliberately.
3. **Require a unified proxy such as LiteLLM.** This simplifies Fixora but adds operational setup, a network dependency, and a new credential boundary. Keep proxy support as an optional OpenAI-compatible gateway, not a prerequisite.

## Proposed architecture

### Shared contract

Keep the existing public `Provider` abstraction conceptually stable: a configured adapter accepts a context and a redacted Kubernetes `Finding`, then returns an `AIResult` or a typed error. Internally, split responsibilities into:

- **Configuration/resolution:** resolve provider ID, protocol, base URL, model, authentication, and optional provider-specific settings from environment/config with existing precedence rules.
- **Provider registry/factory:** map provider IDs and protocol choices to an adapter. Unknown or incomplete configurations fail before any request with a safe actionable message.
- **Protocol adapter:** construct the endpoint, authentication headers, request body, and parse the provider-specific response.
- **Shared contract validation:** normalize a complete response into `AIResult`; reject missing required explanation fields, invalid confidence, malformed patch YAML, and unsupported/unsafe fields according to existing remediation validation boundaries.
- **Transport and diagnostics:** enforce context/timeouts, response size limits, retry only retryable statuses, honor bounded Retry-After, and map failures to typed errors suitable for CLI/doctor output.

The CLI, planner, and remediation safety path continue to consume `AIResult`; they do not depend on vendor response structures.

### Protocol and provider coverage

The initial protocol set should include:

- OpenAI Chat Completions and OpenAI Responses as explicitly selected or capability-tested request modes; do not assume a model supports both.
- OpenAI-compatible Chat Completions for compatible hosts, with provider presets for OpenAI, Azure OpenAI, local servers, and other verified compatible services. Azure endpoint/version requirements must be represented explicitly, not guessed from URL substring checks.
- Anthropic Messages for first-party Anthropic. Bedrock's Anthropic model access uses its own AWS authentication and service request path, even when the model family is Claude.
- Google Gemini API `generateContent` for Google AI Studio and a separate Vertex AI adapter for its endpoint, location, project, and authentication requirements.
- Explicit adapters or supported compatibility paths for other existing configured providers (Cohere, Hugging Face, Watsonx, SageMaker, OCI, Bedrock, and Ollama). The implementation plan must inventory existing claims and decide coverage based on tests and maintained API support; do not silently route an unrelated API through OpenAI format.

OpenAI-compatible gateways can expose models from other vendors. The configured protocol, not the model name alone, determines wire format. Provider/model aliases remain user-configurable; Fixora should avoid freezing a rapidly aging catalog into the CLI as the source of truth.

### Structured output and parsing

The canonical Fixora response contract stays provider-neutral. Each adapter declares supported output modes and maps the contract to the provider's current API feature where supported: strict JSON Schema, constrained JSON, or a prompt-only JSON request where necessary. The adapter must not send a feature simply because a model name appears to be recent. Unsupported optional parameters are omitted based on an explicit capability or a narrowly scoped compatibility fallback; Fixora must not repeatedly retry arbitrary 400 responses with progressively weaker requests.

All returned text is parsed through one contract validator. The validator handles provider envelopes and documented content blocks in their adapter; it must not search arbitrary prose for a JSON-looking substring and treat that as trustworthy. On incomplete, refused, empty, malformed, or schema-invalid output, return a typed failure or an explicit unstructured result that reliably triggers deterministic fallback. The user-facing message should state what failed and a safe next diagnostic command, while preserving the original deterministic finding and proof.

Provider output remains untrusted. AI content must not bypass evidence requirements, patch validation, shadow checks, policy, or user approval.

### Authentication, errors, and observability

Use provider-specific auth mechanisms: bearer/API-key headers for applicable APIs, Google API keys or Google credentials for the selected Gemini/Vertex mode, AWS credential-chain signing for Bedrock, and configured Azure keys/tokens for Azure OpenAI. Secrets must never appear in doctor output, logs, errors, URLs, cache keys shown to users, or tests. Do not accept credentials embedded in a base URL as an implicit auth mechanism.

Represent failures with safe categories such as configuration, authentication/authorization, unsupported model/endpoint/feature, rate limit, transient server error, timeout/cancellation, response incomplete, and response-contract failure. Preserve status plus a sanitized provider code/parameter when safe. Do not print provider response text or request payload by default. Retry only rate limits and transient server failures under the existing bounded retry policy; honor cancellation and bounded Retry-After.

`ai doctor` should distinguish local configuration validity from a live connectivity/model check. If it makes a network call, label that behavior clearly. It should report the selected protocol and a safe endpoint host, not expose path query secrets or keys. Add a non-mutating smoke test that can verify provider auth and basic generation without requiring a Kubernetes cluster, while warning that a successful smoke test does not guarantee support for every structured-output mode.

### Compatibility and migration

Preserve existing `FIXORA_AI_PROVIDER`, `FIXORA_AI_API_KEY`, `FIXORA_AI_BASE_URL`, `FIXORA_AI_MODEL`, config-file fields, and documented CLI setup wherever their meaning remains unambiguous. Add only the minimal settings needed to distinguish endpoint/protocol variants and provider-specific auth. Detect ambiguous legacy combinations and provide a migration diagnostic rather than silently selecting a different API.

Keep default OpenAI behavior on a broadly available, low-cost model unless the user configures another model. Default provider/model values and examples must be documented as defaults, not guarantees of current availability. Existing saved config must continue loading, and the cache key must include all request-shaping identity (provider, protocol, model, endpoint identity, prompt profile, and output mode) without including secrets.

## K8sGPT reuse and attribution

K8sGPT is Apache-2.0 licensed and documents a provider/backend architecture, including first-party and cloud backends plus LiteLLM as an optional route to many providers. Use it as a comparative reference for provider registration, configuration, and backend coverage. Before copying any implementation, identify exact source files, check dependency/API fit and current upstream state, preserve copyright and license headers, include required NOTICE/attribution material, and run license/security checks. Prefer independently implementing the protocol boundary where Fixora's safety, response contract, or dependency model differs.

## Testing and acceptance criteria

- Unit tests with `httptest` cover each adapter's URL, method, authentication, request shape, optional structured-output behavior, response parsing, and sanitized error mapping.
- A table-driven provider matrix verifies each supported provider ID resolves to the intended protocol and unsupported IDs fail clearly.
- Contract tests feed valid, incomplete, refused, empty, malformed, and schema-invalid provider responses through the common validator and confirm deterministic fallback remains useful.
- Tests prove retry behavior for transient statuses, no retry for non-retryable 4xx, bounded Retry-After, cancellation, response size limits, and absence of secrets/raw evidence in diagnostics.
- Compatibility tests load legacy configuration and verify the same provider/model/base URL precedence, including environment overrides.
- No test requires live API credentials. Optional manual smoke tests against user-owned accounts may validate current endpoints but are not required CI gates.
- `go test ./...`, `go test -race ./internal/ai`, `go vet ./...`, build, and documentation checks pass.
- A change is accepted only when a previously failing model/API combination is demonstrated by a request/response fixture or an explicitly documented unsupported capability with an actionable diagnostic; the healthy-workload path remains unchanged.

## Rollout

Implement behind the existing configuration surface in incremental adapter migrations. Keep a tested legacy path for each provider until its replacement passes adapter and contract tests. Update provider configuration help and AI documentation alongside each migration. Release notes should call out protocol selection changes, migration behavior for ambiguous configs, and the exact set of provider/API modes validated in that release.

## Decisions and assumptions

- “All providers” means all provider IDs Fixora chooses to advertise as supported, across their maintained API modes; it cannot mean guaranteed compatibility with every third-party model endpoint or account.
- OpenAI, Google, and Anthropic first-party APIs are priority integrations; current Fixora gateways/backends are included in the provider inventory and receive explicit support or deprecation decisions.
- Live provider behavior can change independently of Fixora. Tests use stable fixtures, while the CLI provides diagnostics that help users identify access and capability changes.
- The existing uncommitted remediation and EKS work in the checkout is unrelated and must not be included in provider architecture commits.
