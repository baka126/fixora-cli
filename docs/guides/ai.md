---
layout: default
title: AI providers and evidence
description: Configure optional AI analysis and understand what leaves your workstation.
permalink: /guides/ai/
section: guides
---

# AI providers and evidence

Fixora's deterministic checks work without an AI key. AI can add an explanation and patch suggestion, but the answer must be checked against proof, the diff, and policy gates. Provider availability and model quality vary.

## Provider presets

Provider IDs select an API protocol and default authentication style. Model access and optional structured-output features still depend on the provider account and model. A custom base URL must implement the selected protocol.

There are two ways to use an AI account with Fixora. The Fixora CLI can call a provider directly with that provider's API credentials (or Fixora's ChatGPT sign-in). Alternatively, Claude Code or Gemini CLI can run as the AI host and call Fixora's local MCP tools using the host's own sign-in. MCP setup does not configure Fixora's direct AI provider.

| Workflow | Where the model runs | Credentials used | What Fixora does |
| --- | --- | --- | --- |
| `FIXORA_AI_PROVIDER=openai`, `google`, or `anthropic` | Fixora CLI calls the provider API | Provider API key or configured cloud identity | Sends the selected redacted diagnosis/Ask prompt to that API. |
| `FIXORA_AI_PROVIDER=chatgpt` | Fixora CLI calls the OpenAI Responses API | ChatGPT sign-in stored by Fixora | Uses eligible ChatGPT plan access for Fixora diagnosis/Ask; does not silently switch to API billing. |
| Claude Code or Gemini CLI with Fixora MCP | The signed-in assistant host | Host's own login/subscription | Exposes Fixora's local MCP tools to the host; Fixora does not receive the host's subscription token. |

| Provider ID | API protocol | Authentication | Endpoint/model notes |
| --- | --- | --- | --- |
| `openai` | OpenAI Chat Completions | Bearer API key | Defaults to `https://api.openai.com/v1`; model is configurable. |
| `chatgpt` | OpenAI Responses API | Sign in with ChatGPT | Uses eligible ChatGPT plan usage; sign in with `kubectl fixora auth login chatgpt`. |
| `google`, `gemini` | Gemini `generateContent` | Google API key | Defaults to the Gemini API v1beta endpoint. |
| `anthropic` | Anthropic Messages | Anthropic API key | Defaults to `https://api.anthropic.com`; model is configurable. |
| `azureopenai` | Azure OpenAI Chat Completions | Azure API key | Configure the Azure endpoint, deployment, and API version. |
| `googlevertexai` | Vertex AI Gemini generation | Google credentials | Configure project and location. |
| `amazonbedrock`, `amazonbedrockconverse` | Bedrock InvokeModel / Converse | AWS credential chain | Configure region and model ID. |
| `groq`, `localai`, `customrest` | OpenAI-compatible Chat Completions | Bearer key or optional key | Configure the provider's OpenAI-compatible base URL. |
| `ollama` | Ollama chat | None by default | Defaults to `http://localhost:11434`; configure the model. |
| `cohere` | Cohere v2 Chat | Bearer API key | Defaults to `https://api.cohere.com`; Fixora validates text response blocks. |
| `huggingface` | Hugging Face Inference Providers Chat Completions | Bearer token | Defaults to `https://router.huggingface.co/v1`; the endpoint implements OpenAI Chat Completions. |
| `amazonsagemaker`, `oci`, `watsonxai`, `ibmwatsonxai` | Not implemented | Provider-specific | Accepted by config validation for migration compatibility; `ai doctor` gives the current limitation and safe alternative. |
| `noop` | No network API | None | Returns deterministic-only guidance. |

OpenAI also supports the Responses API by explicit protocol selection. `FIXORA_AI_PROTOCOL` accepts `openai-chat-completions` or `openai-responses` for `openai`; Fixora does not guess based on a model name. The `chatgpt` provider always uses the constrained streamed Responses protocol required for ChatGPT plan use. `FIXORA_AI_OUTPUT_MODE` accepts `prompt-json`, `json`, or `json-schema`. The default is JSON mode for OpenAI-compatible APIs and Gemini, and prompt-only JSON for providers whose structured-output support varies by model. Fixora validates every response against its remediation contract before use and never retries a rejected output mode as a different mode.

### Use your ChatGPT plan

The browser sign-in uses eligible ChatGPT plan usage for Fixora AI requests and does not need an API key:

**Effect:** Opens the system browser, receives the local OAuth callback, and saves the signed-in session and provider choice. `ai doctor` reads local sign-in state. The preview reads Kubernetes evidence and sends the normal redacted AI prompt; logout revokes the session and clears local credentials.

```bash
kubectl fixora auth login chatgpt
kubectl fixora ai doctor
kubectl fixora fix deployment/api -n prod --ai --preview
kubectl fixora auth logout chatgpt
```

Fixora stores the OAuth session in a private local file and refreshes it when needed. This first version supports one active ChatGPT account; account switching is not supported yet. The signed-in account’s plan and app limits apply. Fixora will not silently switch to API billing when subscription use is unavailable. This is the path for Fixora's own `--ai` diagnosis and Ask requests when `chatgpt` is the selected provider; it is separate from using ChatGPT as an MCP host.

### Use Claude Code or Gemini CLI as the AI host

Claude Code and Gemini CLI can use Fixora as a local MCP server. The assistant host keeps and uses its own sign-in; Fixora does not read or store its subscription credentials. Install and sign in to the host first, then have Fixora print the host-native registration command:

**Effect:** Starts no cluster operation. The setup command prints a local MCP registration command; `--apply` asks the selected AI CLI to update its own configuration. Sign-in happens in that AI CLI and is not saved in Fixora.

```bash
kubectl fixora mcp setup claude-code
kubectl fixora mcp setup gemini-cli
```

The command defaults to user scope. Add `--scope project` for a project-scoped entry, `--context NAME` or `-n NAMESPACE` to pass a Kubernetes context or namespace to the MCP server, or `--apply` to let the installed host CLI update its own configuration. Without `--apply`, Fixora only prints a command. Applying may replace the host's existing MCP entry named `fixora`; inspect that entry first if you have one. Fixora does not install either host CLI or sign you into it.

After setup, use the host CLI to check its own MCP connection (`claude mcp list` or `gemini mcp list`). To remove the registration, use the host CLI's MCP remove command for the `fixora` entry in the same scope. These checks confirm MCP connectivity, not Fixora's direct API adapter. Gemini CLI's Google-account sign-in is handled by Gemini CLI; Fixora's direct `google`/`gemini` API adapters still use a Google API key. For host setup, see [Claude Code MCP setup](https://code.claude.com/docs/en/mcp), [Gemini CLI MCP setup](https://github.com/google-gemini/gemini-cli/blob/main/docs/tools/mcp-server.md), and [Gemini CLI authentication](https://github.com/google-gemini/gemini-cli/blob/main/docs/get-started/authentication.mdx).

Validated adapters include OpenAI Chat and Responses, Azure OpenAI Chat, Anthropic Messages, Gemini `generateContent`, Vertex AI Gemini with application-default credentials, Bedrock InvokeModel/Converse using the AWS credential chain, Groq/LocalAI/custom REST only when their endpoint implements OpenAI Chat Completions, Ollama, Cohere v2 Chat, and Hugging Face text-generation inference. These integrations have offline request/response tests; account access, model availability, endpoint-specific features, and credentials still depend on the upstream service.

`amazonsagemaker`, `oci`, `watsonxai`, and `ibmwatsonxai` remain accepted by configuration validation for compatibility, but `ai doctor` reports that their native protocol/auth adapters are not implemented. Fixora does not send these through an unrelated API. Use `customrest` only when the endpoint actually supports OpenAI Chat Completions; otherwise select a provider with a tested native adapter.

## Configure a provider

Use an environment variable for a production key rather than storing a long-lived key in the local config file. Set the provider, model, and optional base URL for your chosen endpoint.

**Effect:** Writes shell environment only. `ai doctor` checks local configuration without contacting the provider. `ai doctor --live` sends two small synthetic requests to check diagnosis and Ask's structured response path; it does not connect to Kubernetes or use cluster evidence.

```bash
export FIXORA_AI_PROVIDER=openai
export FIXORA_AI_API_KEY='your-key-from-a-secret-manager'
export FIXORA_AI_MODEL=gpt-4o-mini
kubectl fixora ai doctor
kubectl fixora ai doctor --live
```

`ai doctor` reports status `configured` plus the selected provider, model, protocol, and endpoint host without printing endpoint credentials or URL query values. It makes no provider request. `ai doctor --live` sends two small synthetic requests through the configured Fixora provider: one validates the diagnosis response contract and one validates the structured JSON path used by Ask. Its JSON includes separate `diagnosis` and `ask` checks. Overall status is `ok` only if both pass; if diagnosis fails, Ask is marked `skipped`. For example, success includes `"status":"ok"` and checks named `diagnosis` and `ask`, each with status `ok`. The checks share a 30-second deadline and may use billable tokens. They do not connect to Kubernetes and do not verify Claude Code or Gemini CLI host sign-in; check those hosts with their own MCP status commands. A pass confirms only that these requests worked at that moment. For failures, first confirm the provider, model, protocol, endpoint, and credentials; if diagnosis passes but Ask fails, verify the selected endpoint/model supports the structured JSON request mode. OpenAI Responses can be selected with `export FIXORA_AI_PROTOCOL=openai-responses`; for broader compatibility use `export FIXORA_AI_OUTPUT_MODE=json`. Google Vertex requires `FIXORA_AI_PROJECT` and `FIXORA_AI_LOCATION`; Bedrock requires `AWS_REGION` (or `AWS_DEFAULT_REGION`) and the normal AWS SDK credential chain. `auth set` can store credentials locally, but review how that machine is protected. `config view` and `config export` are intended to avoid printing the API key.

### Troubleshooting provider setup

| Symptom | What it means | Next step |
| --- | --- | --- |
| `ai doctor` says `configured` | Local settings are present; no network request was made. | Run `ai doctor --live` to check the selected provider's diagnosis and Ask response paths. |
| Diagnosis check fails | Fixora could not complete or validate its synthetic diagnosis request. | Check the selected provider's auth, model access, endpoint/protocol, and network path. The Ask check is skipped. |
| Diagnosis passes, Ask fails | Basic generation works, but the structured JSON request or response was rejected. | Check model support for the selected protocol and structured output; compare the configured model and `FIXORA_AI_OUTPUT_MODE` with the provider's supported options. |
| Host CLI cannot connect to Fixora | This is an MCP process/configuration issue, not an API-key diagnosis. | Run the host's MCP list command, confirm Fixora is installed and on the configured path, and verify the chosen context/namespace. |
| `mcp setup --apply` cannot find `claude` or `gemini` | The host CLI is not installed or is missing from `PATH`. | Install the host CLI or copy the printed registration command and run it in a shell where that CLI is available. |

## Request an explanation

**Effect:** Reads cluster evidence and bounded logs by default, then sends a bounded, redacted prompt to the configured AI endpoint. Use `--include-logs=false` to prevent log reads and exclude that evidence from the prompt. No cluster writes.

```bash
kubectl fixora why deployment/api -n prod --proof --ai
```

## Find a pod by name or description

You can pass a pod name directly, or describe the problem in plain language:

**Effect:** Reads Pod names and status, and for a natural-language target lookup may send a bounded candidate list to the configured AI provider. It asks for target confirmation before the normal diagnosis flow; no change is applied.

```bash
kubectl fixora payments-api-7d9f -n payments
kubectl fixora "payments pod keeps crashing" -n payments
```

An exact live Pod name is matched locally and uses the regular fix flow; AI is used for diagnosis only if you add `--ai`. For a description, Fixora sends the request and a bounded list of pod names and status summaries to the configured AI provider. It shows the suggested Pod and asks you to confirm before running the full diagnosis or rendering a repair preview. If several Pods match, select one with `--select <number>` first. In a non-interactive run, use both `--select <number>` when needed and `--confirm-target`; without confirmation, Fixora only reports the candidate. Target matching never applies a change. The subsequent repair remains subject to the normal patch review and shadow-verification gates.

## Ask in natural language (upcoming)

`ask` is implemented in the current development branch and is not included in v0.8.0. It selects analyzers from the request, then resolves it against current findings and scoped live resource identities. It can answer questions across related findings, but a repair preview remains bound to one selected resource and uses the existing guarded fix flow. Ask never applies changes or delivers a patch. In a terminal, Fixora asks before previewing; in scripts or structured output, add `--continue` to request the preview. If several resources match a repair request, choose one with `--select <number>`; summary and debugging questions can include correlated findings without forcing a repair-target choice. Sessions can be resumed one turn at a time with `--session ID`.

**Effect:** Reads scoped cluster evidence and may read bounded logs, sends bounded redacted context to the configured AI provider, and stores a local session summary. These questions do not apply cluster changes.

```bash
kubectl fixora ask "why is the payments API crashing?" -n payments
kubectl fixora ask "summarize unhealthy workloads in payments" -n payments
kubectl fixora ask "what is unhealthy across the cluster?" --all-namespaces
kubectl fixora ask "what is configured in payments?" -n payments
kubectl fixora ask "are any workloads healthy?" --all-namespaces
kubectl fixora ask "fix the crashing API" -n payments --continue
kubectl fixora ask "fix the API" -n payments --select 2 --continue
```

Ask starts with analyzers relevant to the question and broadens once when inferred coverage has no findings or skipped checks. Explicit `--filter` and `--quick` scopes are preserved. Skipped checks are reported as coverage limitations while available findings remain usable. Each resource kind in the analyzer registry has an explicit context policy, with bounded summaries for workloads, networking, storage, scheduling, autoscaling, disruption budgets, policy reports, webhooks, and operator lifecycle resources. Optional CRDs that are unavailable remain scan limitations; unknown fields are not forwarded as raw objects. `--secret-keys` and `--cert-expiry` remain opt-in. Candidate resolution uses bounded resource identities and summaries; the incident answer receives bounded, redacted correlated evidence with source resource provenance. It does not send arbitrary Kubernetes objects, environment values, command arguments, ConfigMap values, annotations, Secret data, webhook CA bundles, or opaque provider parameters to the provider. ConfigMap key names are included without their values; Secret type and key names are included only with `--secret-keys`, never Secret values. If the requested detail is outside collected evidence, Ask should report the gap rather than infer it. If no active finding supports a requested repair, Ask will not invent a patch. The guarded preview may gather its normal evidence according to privacy and evidence flags.

Broad questions such as “what is configured in this namespace?” or “are any workloads healthy?” can be answered from allow-listed live summaries even when no active finding matches. Scope answers cite current evidence and report skipped checks; they do not invent a target or offer a repair preview. Scope sessions can be resumed with `--session ID`; each turn recollects evidence for the saved namespace or cluster scope. The label selector is retained only in the private local session file and is not sent to AI. A repair request still needs an exact active finding before Fixora can render a guarded preview.

Read the provider's data handling terms before enabling AI. Redaction reduces exposure; it cannot guarantee that every sensitive string in arbitrary logs or annotations is removed. Avoid `--unsafe-ai-no-redact` in routine use and use `--paranoid` for a stricter evidence path. Fixora does not send data to a Fixora-hosted backend.

## When AI fails

Check provider, model, key, base URL, network access, and budget/timeout settings. A provider error should not erase the deterministic cluster findings. Capture a redacted local report for review and avoid pasting raw logs or Secret values into an issue.

Profiles and scenario memory are local features; inspect their stored configuration before sharing a workstation or report. See [configuration and output]({{ '/reference/configuration-and-output/' | relative_url }}).
