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

## Interactive or scripted

Run `fix` without a resource for a guided terminal flow in v0.8.0. For scripts, name the target and provide explicit values; inspect structured output and require your own approval before a write. Bare `kubectl fixora` opens the older dashboard in v0.8.0; the new no-argument chooser is **Upcoming** on [the development branch](https://github.com/baka126/fixora-cli/tree/codex/production-hardening).
