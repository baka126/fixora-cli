---
layout: default
title: Command reference
description: Released commands, aliases, and important flags with upcoming additions clearly labeled.
permalink: /reference/commands/
section: reference
---

# Command reference

The following command families are available in **v0.8.0** unless marked Upcoming. Run `kubectl fixora help --advanced` on your installed binary for its exact syntax. Commands that perform a write still require the corresponding Kubernetes or repository permission; see [safety]({{ '/safety/' | relative_url }}).

## Diagnose

| Command | Purpose and effect |
| --- | --- |
| `scan` / `incidents` | List failing workloads and analyzer findings; reads cluster state. `scan` is an alias for `incidents`. |
| `why` | Explain one resource with evidence, proof, and next step; reads cluster state, optionally calls AI with `--ai`. |
| `status` | Show access and capability summary; reads cluster state. |
| `doctor` | In v0.8.0, the top-level command runs the **AI configuration check**. The release help also describes a cluster check, but command routing selects AI doctor. |
| `filters` | Show analyzer catalog and active filter selection; local registry read. |
| `health` | Summarize namespace or cluster health; reads cluster state. |
| `watch` | Poll incidents until interrupted; repeated cluster reads. |
| `predict` | Show future-risk signals from local evidence; reads cluster state. |
| `cost` | Estimate node or workload costs from available metadata; reads cluster state and is not a billing quote. |

## Inspect a particular subsystem

| Command | Purpose and effect |
| --- | --- |
| `trace` | Read Ingress/HTTPRoute/Service path and backend evidence. |
| `storage` | Read PVC/PV/StorageClass state. |
| `rbac` | Check a service account, verb, and resource; reads authorization evidence. |
| `dns` | Read Service DNS and CoreDNS signals. |
| `security` | Read security context and policy failure signals. |
| `node-pressure` | Read node readiness, pressure, and eviction signals. |
| `graph` | Build a resource relationship graph; reads cluster state. |
| `changes` | Inspect changes for one resource; reads cluster history. |
| `debug` | Grouped alias for specialist debugging tools; check `help` for its subcommands. |

## Review and deliver

| Command | Purpose and effect |
| --- | --- |
| `fix` | Guided plan, concrete patch, optional shadow verification, and chosen delivery. Reads first; a selected delivery can write locally, to the cluster, or to source/PR. |
| `repo` | Detect raw, Helm, or Kustomize source from a local path. |
| `validate` | Render and validate local source. |
| `lint` | Lint a manifest, chart, or overlay. |
| `preflight` | Lint and server-side dry-run a manifest; reads source and cluster validation API. |
| `policy-check` | Check manifests against production policy rules. |
| `source` | Grouped alias for source tools; check `help` for subcommands. |

## Operate and extend

| Command | Purpose and effect |
| --- | --- |
| `ui` / `cluster` | Terminal dashboards; reads cluster state. Bare invocation opens the older dashboard in v0.8.0. |
| `integrations` | Detect local optional tools and CRDs. |
| `custom-analyzers` | List, add, or run explicitly registered local executables; `run` executes local code. |
| `serve` | Start loopback HTTP API or `--mcp` stdio server; requests read cluster state. |
| `report` / `bundle` | Render Markdown report or local incident archive; reads cluster state and may write a local file. |
| `auth` / `config` | Set provider credentials or inspect/change local settings. |
| `cache` / `memory` | Manage local cache metadata and scenario memory; `cache add|get|remove` handles K8sGPT-style remote-cache metadata. |
| `profiles` | List available prompt profiles in v0.8.0. |
| `version` | Print installed version. |

## Upcoming commands and changed behavior

The following require [codex/production-hardening](https://github.com/baka126/fixora-cli/tree/codex/production-hardening): the `ai` family adds `ai doctor`, separating AI setup from the cluster `doctor`; `coordinate` and alias `fix-set` plan and gate multi-resource changes. The branch also adds a no-argument terminal chooser, interactive `auth`/`profiles` improvements, and `--mcp-shadow`.

## Global and workflow flags

| Scope | Flags and meaning |
| --- | --- |
| Target | `-n, --namespace` (default `default`), `-A, --all-namespaces`, `--context`, `-l, --selector`, `--filter` |
| Evidence | `--include-logs`, `--proof`, `--typed-client`, `--log-tail`, `--max-logs-bytes`, `--timeout` |
| Output | `-o, --output`, `--out`, `--wide`, `--no-color`, `--max-findings`, `--watch-interval` |
| AI and privacy | `--ai`, `--redact`, `--paranoid`, `--unsafe-ai-no-redact`, `--profile`, `--ai-budget-tokens` |
| Planning | `--auto-fix`, `--preview`, `--strategy`, `--force-risky`, `--container`, `--image`, `--memory-request`, `--memory-limit`, `--cpu-request`, `--env-name`, `--configmap`, `--config-key` |
| Delivery | `--apply`, `--apply-dry-run`, `--source-patch`, `--repo`, `--gitops`, `--shadow`, `--shadow-timeout`, `--shadow-retries`, `--keep-shadow`, `--shadow-egress`, `--delivery`, `--pr-base`, `--pr-title`, `--yes` |
| Presets and files | `--quick`, `--safe`, `-f, --filename`, `--tui` |

Some flags only affect relevant commands. `--quick` can skip default shadow verification; an output without shadow is not shadow-verified. `--yes` confirms a supported noninteractive delivery but does not run a rollback automatically. `--unsafe-ai-no-redact` can expose sensitive evidence to a provider.

**Upcoming flags:** `--no-ai`, `--edit-patch`, `--mcp-shadow`, `--secret-keys`, `--cert-expiry`, and coordination's `--from` are branch-only. The branch accepts compatibility aliases `--filters` and `-L`; prefer canonical `--filter` and `-l`. No v0.8.0 flag removal is claimed here.
