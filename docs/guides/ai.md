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

## Ask in natural language (branch-only)

`ask` selects analyzers from the request, then resolves it against current findings and scoped live resource identities. It can answer questions across related findings, but a repair preview remains bound to one selected resource and uses the existing guarded fix flow. Ask never applies changes or delivers a patch. In a terminal, Fixora asks before previewing; in scripts or structured output, add `--continue` to request the preview. If several resources match a repair request, choose one with `--select <number>`; summary and debugging questions can include correlated findings without forcing a repair-target choice.

```bash
kubectl fixora ask "why is the payments API crashing?" -n payments
kubectl fixora ask "summarize unhealthy workloads in payments" -n payments
kubectl fixora ask "what is unhealthy across the cluster?" --all-namespaces
kubectl fixora ask "fix the crashing API" -n payments --continue
kubectl fixora ask "fix the API" -n payments --select 2 --continue
```

Ask starts with analyzers relevant to the question and broadens once when inferred coverage has no findings or skipped checks. Explicit `--filter` and `--quick` scopes are preserved. Skipped checks are reported as coverage limitations while available findings remain usable. Each resource kind in the analyzer registry has an explicit context policy, with bounded summaries for workloads, networking, storage, scheduling, autoscaling, disruption budgets, policy reports, webhooks, and operator lifecycle resources. Optional CRDs that are unavailable remain scan limitations; unknown fields are not forwarded as raw objects. `--check-secret-keys` and `--check-cert-expiry` remain opt-in. Candidate resolution uses bounded resource identities and summaries; the incident answer receives bounded, redacted correlated evidence with source resource provenance. It does not send arbitrary Kubernetes objects, environment values, command arguments, ConfigMap values, annotations, Secret data, webhook CA bundles, or opaque provider parameters to the provider. ConfigMap key names are included without their values; Secret type and key names are included only with `--check-secret-keys`, never Secret values. If the requested detail is outside collected evidence, Ask should report the gap rather than infer it. If no active finding supports a requested repair, Ask will not invent a patch. The guarded preview may gather its normal evidence according to privacy and evidence flags.

Read the provider's data handling terms before enabling AI. Redaction reduces exposure; it cannot guarantee that every sensitive string in arbitrary logs or annotations is removed. Avoid `--unsafe-ai-no-redact` in routine use and use `--paranoid` for a stricter evidence path. Fixora does not send data to a Fixora-hosted backend.

## When AI fails

Check provider, model, key, base URL, network access, and budget/timeout settings. A provider error should not erase the deterministic cluster findings. Capture a redacted local report for review and avoid pasting raw logs or Secret values into an issue.

Profiles and scenario memory are local features; inspect their stored configuration before sharing a workstation or report. See [configuration and output]({{ '/reference/configuration-and-output/' | relative_url }}).
