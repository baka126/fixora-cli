---
layout: default
title: Incident workflow
description: Move from a Kubernetes finding to an evidence-backed, reviewable patch.
permalink: /guides/incident-workflow/
section: guides
---

# Incident workflow

Fixora's main loop is **scan → why → fix**. Keep the context and namespace explicit throughout an incident, and inspect the result of each step before continuing.

## Discover and inspect proof

**Effect:** Reads cluster objects, Events, and bounded container logs by default. No writes. Use `--include-logs=false` on each command to prevent log reads when your data policy requires it.

```bash
kubectl fixora scan -n prod
kubectl fixora why deployment/api -n prod --proof
```

The finding connects a status to supporting signals and a likely cause. `--proof` exposes the evidence trail; it does not turn a hypothesis into a guarantee. Use `-o json` for a machine-readable result, and [analyzer filters]({{ '/reference/analyzers/' | relative_url }}) when narrowing a scan.

## Review the plan before any mutation

`fix` can guide you from explanation through a concrete patch. Specify values such as the target container and pinned image; unresolved placeholders are not an executable fix.

**Effect:** Reads cluster evidence, including bounded logs by default, and produces a preview. Use `--include-logs=false` to skip logs. `--preview` does not apply to the live workload.

```bash
kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/example/api:v1.2.3 --preview
```

Review the target, diff, strategy, blocked reasons, and rollback hint. A low-confidence, risky, or non-concrete plan may remain **review-only**. `--force-risky` changes eligibility after review; it is not proof of safety.

## Verification is a separate result

An enabled shadow check creates isolated resources, applies the patch to a clone, waits for selected health signals, and reports what it observed. A `--quick` path can skip that check. A skipped, failed, or inconclusive check must be treated differently from a verified result. See [delivery and verification]({{ '/guides/delivery/' | relative_url }}).

Fixora also keeps a small local history for the same cluster and workload. After a shadow run it records the outcome and available numeric readiness or memory measurements. Only a verified session can appear as “Seen before” in a later analysis; this history is context, never approval to reuse a patch or bypass current checks. The profile is stored under `~/.fixora/<cluster-hash>/profiles.json` with private file permissions. If the profile cannot be read or written, the current fix continues without history.

## Interactive or scripted

Run `fix` without a resource for a guided terminal flow in v0.8.0. For scripts, name the target and provide explicit values; inspect structured output and require your own approval before a write. Bare `kubectl fixora` opens the older dashboard in v0.8.0; the new no-argument chooser is **Upcoming** on [the development branch](https://github.com/baka126/fixora-cli/tree/codex/production-hardening).

## Ask a follow-up and resume an incident

Use `ask` to investigate Kubernetes resources and analyzer findings in natural language. Fixora chooses relevant analyzers from the question, broadens once if the inferred scan is inconclusive, and includes related evidence in the answer while preserving resource provenance. A repair preview still requires one exact target. Ask sends bounded, redacted evidence to the configured AI provider and saves a short local session summary so you can continue in a later command:

```bash
kubectl fixora ask "why is the API crashing?" -n payments
kubectl fixora ask --session <id> "what changed since my last question?"
kubectl fixora ask sessions list
kubectl fixora ask sessions delete <id>
```

A resumed turn is bound to the current cluster and revalidates the original resource before collecting fresh evidence. Missing, recreated, or re-owned targets are rejected. Session files live under `~/.fixora/<cluster-hash>/ask-sessions.json`, use private local permissions, keep bounded summaries rather than raw logs or Kubernetes objects, and expire after 30 days. Every AI-backed turn sends current redacted incident evidence and bounded prior-turn summaries to the configured provider. Review provider and data handling settings before use.

Text output shows a compact summary with a few cited signals; add `--proof` to display all bounded claims and evidence references, or use `-o json` for the complete structured result. A recovery is reported only when fresh analyzer evidence confirms a healthy target and its saved controller chain. If that identity evidence is missing, Fixora stops rather than claiming recovery.

Ask is preview-only. A repair request may open Fixora's guarded repair preview, but the model cannot authorize apply or delivery.
