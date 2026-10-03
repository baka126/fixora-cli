---
layout: default
title: AI providers and evidence
description: Configure optional AI analysis and understand what leaves your workstation.
permalink: /guides/ai/
section: guides
---

# AI providers and evidence

Fixora's deterministic checks work without an AI key. AI can add an explanation and patch suggestion, but the answer must be checked against proof, the diff, and policy gates. Provider availability and model quality vary.

## Configure a provider

Use an environment variable for a production key rather than storing a long-lived key in the local config file. Set the provider, model, and optional base URL for your chosen endpoint.

**Effect:** Writes shell environment only. The subsequent doctor command checks local AI configuration and may contact the configured provider; it does not change Kubernetes resources.

```bash
export FIXORA_AI_PROVIDER=openai
export FIXORA_AI_API_KEY='your-key-from-a-secret-manager'
kubectl fixora doctor
```

In v0.8.0, `doctor` is the AI setup check. The branch introduces `ai doctor` and a separate cluster `doctor` (**Upcoming**). `auth set` can store credentials locally, but review how that machine is protected. `config view` and `config export` are intended to avoid printing the API key.

## Request an explanation

**Effect:** Reads cluster evidence and bounded logs by default, then sends a bounded, redacted prompt to the configured AI endpoint. Use `--include-logs=false` to prevent log reads and exclude that evidence from the prompt. No cluster writes.

```bash
kubectl fixora why deployment/api -n prod --proof --ai
```

Read the provider's data handling terms before enabling AI. Redaction reduces exposure; it cannot guarantee that every sensitive string in arbitrary logs or annotations is removed. Avoid `--unsafe-ai-no-redact` in routine use and use `--paranoid` for a stricter evidence path. Fixora does not send data to a Fixora-hosted backend.

## When AI fails

Check provider, model, key, base URL, network access, and budget/timeout settings. A provider error should not erase the deterministic cluster findings. Capture a redacted local report for review and avoid pasting raw logs or Secret values into an issue.

Profiles and scenario memory are local features; inspect their stored configuration before sharing a workstation or report. See [configuration and output]({{ '/reference/configuration-and-output/' | relative_url }}).
