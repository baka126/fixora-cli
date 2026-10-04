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

| Provider ID | API protocol | Authentication | Endpoint/model notes |
| --- | --- | --- | --- |
| `openai` | OpenAI Chat Completions | Bearer API key | Defaults to `https://api.openai.com/v1`; model is configurable. |
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

OpenAI also supports the Responses API by explicit protocol selection. `FIXORA_AI_PROTOCOL` accepts `openai-chat-completions` or `openai-responses` for `openai`; Fixora does not guess based on a model name. `FIXORA_AI_OUTPUT_MODE` accepts `prompt-json`, `json`, or `json-schema`. The default is JSON mode for OpenAI-compatible APIs and Gemini, and prompt-only JSON for providers whose structured-output support varies by model. Fixora validates every response against its remediation contract before use and never retries a rejected output mode as a different mode.

Validated adapters include OpenAI Chat and Responses, Azure OpenAI Chat, Anthropic Messages, Gemini `generateContent`, Vertex AI Gemini with application-default credentials, Bedrock InvokeModel/Converse using the AWS credential chain, Groq/LocalAI/custom REST only when their endpoint implements OpenAI Chat Completions, Ollama, Cohere v2 Chat, and Hugging Face text-generation inference. These integrations have offline request/response tests; account access, model availability, endpoint-specific features, and credentials still depend on the upstream service.

`amazonsagemaker`, `oci`, `watsonxai`, and `ibmwatsonxai` remain accepted by configuration validation for compatibility, but `ai doctor` reports that their native protocol/auth adapters are not implemented. Fixora does not send these through an unrelated API. Use `customrest` only when the endpoint actually supports OpenAI Chat Completions; otherwise select a provider with a tested native adapter.

## Configure a provider

Use an environment variable for a production key rather than storing a long-lived key in the local config file. Set the provider, model, and optional base URL for your chosen endpoint.

**Effect:** Writes shell environment only. The subsequent doctor command checks local AI configuration and may contact the configured provider; it does not change Kubernetes resources.

```bash
export FIXORA_AI_PROVIDER=openai
export FIXORA_AI_API_KEY='your-key-from-a-secret-manager'
export FIXORA_AI_MODEL=gpt-4o-mini
kubectl fixora ai doctor
```

`ai doctor` reports the provider, model, selected protocol, and endpoint host without printing endpoint credentials or query values. OpenAI Responses can be selected with `export FIXORA_AI_PROTOCOL=openai-responses`; for broader compatibility use `export FIXORA_AI_OUTPUT_MODE=json`. Google Vertex requires `FIXORA_AI_PROJECT` and `FIXORA_AI_LOCATION`; Bedrock requires `AWS_REGION` (or `AWS_DEFAULT_REGION`) and the normal AWS SDK credential chain. `auth set` can store credentials locally, but review how that machine is protected. `config view` and `config export` are intended to avoid printing the API key.

## Request an explanation

**Effect:** Reads cluster evidence and bounded logs by default, then sends a bounded, redacted prompt to the configured AI endpoint. Use `--include-logs=false` to prevent log reads and exclude that evidence from the prompt. No cluster writes.

```bash
kubectl fixora why deployment/api -n prod --proof --ai
```

Read the provider's data handling terms before enabling AI. Redaction reduces exposure; it cannot guarantee that every sensitive string in arbitrary logs or annotations is removed. Avoid `--unsafe-ai-no-redact` in routine use and use `--paranoid` for a stricter evidence path. Fixora does not send data to a Fixora-hosted backend.

## When AI fails

Check provider, model, key, base URL, network access, and budget/timeout settings. A provider error should not erase the deterministic cluster findings. Capture a redacted local report for review and avoid pasting raw logs or Secret values into an issue.

Profiles and scenario memory are local features; inspect their stored configuration before sharing a workstation or report. See [configuration and output]({{ '/reference/configuration-and-output/' | relative_url }}).
