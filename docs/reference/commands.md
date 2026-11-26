---
layout: default
title: Command reference
description: Released commands, aliases, and important flags with upcoming additions clearly labeled.
permalink: /reference/commands/
section: reference
---

# Command reference

The published release is **v0.8.0**. Rows marked **Upcoming** describe the current development branch and are not in the v0.8.0 installer. Run `kubectl fixora help --advanced` on your installed binary for its exact syntax. Commands that perform a write still require the corresponding Kubernetes or repository permission; see [safety]({{ '/safety/' | relative_url }}).

[Diagnose](#diagnose) · [Subsystems](#inspect-a-particular-subsystem) · [Review and deliver](#review-and-deliver) · [Operate and extend](#operate-and-extend) · [Flags](#global-and-workflow-flags)

## Diagnose

| Command | Purpose and effect |
| --- | --- |
| `scan` / `incidents` | List failing workloads and analyzer findings; reads cluster state and bounded logs by default. `scan` is an alias for `incidents`. |
| `why` | Explain one resource with evidence, proof, and next step; reads cluster state and bounded logs by default, optionally calls AI with `--ai`. |
| `status` | Show access and capability summary; reads cluster state. |
| `doctor` | In v0.8.0, the top-level command runs the **AI configuration check**. The release help also describes a cluster check, but command routing selects AI doctor. |
| `filters` / `analyzers` | Show analyzer catalog and active filter selection; local registry read. `analyzers` is an alias. |
| `health` | Summarize namespace or cluster health; reads cluster state. |
| `watch` | Poll incidents until interrupted; repeated cluster and bounded log reads by default. |
| `ask <request>` (Upcoming) | Investigate a request using active findings or read-only allow-listed scope evidence when no finding matches. Scope answers can resume with `--session ID` and never create a repair preview. Exact-resource repair previews still require an active finding. |
| `ai` | Show AI configuration commands. `ai doctor` checks local provider configuration; `ai doctor --live` checks diagnosis and structured Ask with synthetic requests. The live checks may use billable tokens. |
| `predict` | Show future-risk signals from local evidence; reads cluster state. |
| `cost` | Estimate node or workload costs from available metadata; reads cluster state and is not a billing quote. |

## Inspect a particular subsystem

| Command | Purpose and effect |
| --- | --- |
| `trace` | Read Ingress/HTTPRoute/Service path and backend evidence. |
| `storage` | Read PVC/PV/StorageClass state. |
| `rbac` | Check a service account, verb, and resource. `rbac repair` previews/creates a policy-bound grant with a local receipt; `rbac grants list` lists local receipts; `rbac grants export --grant <id> --out <path>` and `rbac grants import --file <path>` transfer a receipt for the same cluster; `rbac revoke --grant <id> -n <namespace>` previews UID-bound revocation. |
| `dns` | Read Service DNS and CoreDNS signals. |
| `security` | Read security context and policy failure signals. |
| `node-pressure` | Read node readiness, pressure, and eviction signals. |
| `graph` | Build a resource relationship graph; reads cluster state. |
| `changes` | Inspect changes for one resource; reads cluster history. |
| `debug` | Grouped alias for specialist debugging tools; check `help` for its subcommands. |

## Review and deliver

| Command | Purpose and effect |
| --- | --- |
| `fix` / `repair` | Guided plan, concrete patch, optional shadow verification, and chosen delivery. Reads cluster evidence and bounded logs by default; a selected delivery can write locally, to the cluster, or to source/PR. Development builds also support explicit `--telemetry` opt-in to an operator-configured HTTPS collector. `repair` is an alias. |
| `coordinate` / `fix-set` (Upcoming) | Prepare a multi-resource change set. Explicit target lists keep independent verification; `--from <root>` discovers a bounded workload/dependency graph and verifies connected candidates together before any writes. `fix-set` is an alias. |
| `repo` | Detect raw, Helm, or Kustomize source from a local path. |
| `validate` | Render and validate local source. |
| `lint` | Lint a manifest, chart, or overlay. |
| `fix Service/...` with Helm context | Preview and deliver an explicitly mapped, policy-bound Service selector/targetPort change to Helm values; source-only, reconciliation pending. |
| `preflight` | Lint and server-side dry-run a manifest; reads source and cluster validation API. |
| `policy-check` | Check manifests against production policy rules. |
| `source` | Grouped alias for source tools; check `help` for subcommands. |

## Operate and extend

| Command | Purpose and effect |
| --- | --- |
| `ui` / `cluster` / `dashboard` | Terminal dashboards; reads cluster state. `dashboard` aliases `cluster`. Bare invocation opens the older dashboard in v0.8.0. |
| `integrations` | Detect local optional tools and CRDs. |
| `custom-analyzers` | List, add, or run explicitly registered local executables; `run` executes local code. |
| `serve` | Start loopback HTTP API or `--mcp` stdio server; requests read cluster state. Development-only `--mcp-shadow` enables confirmed shadow verification, `--mcp-open-sandbox` adds verified temporary sandbox inspection, and `--mcp-pr-repo` opts into confirmed PR creation from a prepared local branch. |
| `report` / `bundle` | Render Markdown report or local incident archive; reads cluster state and may write a local file. |
| `auth` / `config` | Set provider credentials or inspect/change local settings. `auth login chatgpt` and `auth logout chatgpt` manage ChatGPT plan sign-in. |
| `mcp setup` (Upcoming) | Print or apply host-native Fixora MCP setup for Claude Code or Gemini CLI. Subscription credentials remain with the host CLI. |
| `cache` / `memory` | Manage local cache metadata and scenario memory; `cache add|get|remove` handles K8sGPT-style remote-cache metadata. |
| `profiles` | List available prompt profiles in v0.8.0. |
| `cleanup` (Upcoming) | Preview or delete abandoned Fixora shadow run namespaces using run identity and UID guards. |
| `demo [oom|config|probe]` (Upcoming) | Create one labeled Deployment in a unique namespace, run an eligible proposed repair through isolated shadow verification, then UID-guard delete the namespace. Optional `--ai` allows at most two bounded revisions. Requires confirmation or `--yes`; `--preview` creates nothing. Never delivers or applies a repair to the fixture. |
| `version` | Print installed version. |

## Upcoming commands and changed behavior

The current development branch adds `ai doctor` while top-level `doctor` checks cluster access; `coordinate` and alias `fix-set` gate multi-resource changes; `ask` supports evidence-grounded resource and scope investigations with resumable sessions; and `cleanup` and `demo` manage isolated test resources. It also includes a no-argument terminal chooser, interactive `auth`/`profiles`, opt-in `--mcp-shadow`, `--mcp-open-sandbox`, and `--mcp-pr-repo`, and policy-bound namespaced RBAC repair with receipt-backed revocation and same-cluster receipt transfer. These remain Upcoming until a release containing them is published.

## Global and workflow flags

| Scope | Flags and meaning |
| --- | --- |
| Target | `-n, --namespace` (default `default`), `-A, --all-namespaces`, `--context`, `-l, --selector`, `--filter` (alias `--filters`) |
| Evidence | `--include-logs` (defaults to true for `scan`, `why`, `fix`, and `watch`; use `--include-logs=false` to opt out), `--proof`, `--typed-client`, `--log-tail`, `--max-logs-bytes`, `--timeout` |
| Output | `-o, --output`, `--out`, `--wide`, `--no-color`, `--max-findings`, `--watch-interval` |
| AI and privacy | `--ai`, `--redact`, `--paranoid`, `--unsafe-ai-no-redact`, `--profile`, `--ai-budget-tokens`, `--telemetry` (development-only opt-in), `--no-telemetry` |
| Planning | `--auto-fix`, `--preview`, `--strategy`, `--force-risky`, `--container`, `--image`, `--memory-request`, `--memory-limit`, `--cpu-request`, `--env-name`, `--configmap`, `--config-key` |
| Delivery | `--apply`, `--apply-dry-run`, `--source-patch`, `--repo`, `--gitops`, `--shadow`, `--shadow-timeout`, `--shadow-retries`, `--keep-shadow`, `--shadow-egress`, `--delivery`, `--pvc-policy` (exact StorageClass assignment or bounded expansion), `--pr-base`, `--pr-title`, `--yes` |
| Presets and files | `--quick`, `--safe`, `-f, --filename`, `--tui` |

Some flags only affect relevant commands. `--quick` can skip default shadow verification; an output without shadow is not shadow-verified. PR delivery in v0.8.0 requires `--yes` for its remote Git operation; it may still request shadow review. `--unsafe-ai-no-redact` can expose sensitive evidence to a provider.

**Upcoming flags:** `--no-ai`, `--edit-patch`, `--mcp-shadow`, `--mcp-open-sandbox`, `--mcp-pr-repo`, `--secret-keys`, `--cert-expiry`, `--networkpolicy-policy`, `--rbac-policy`, `--startup-baseline`, `--repair-baseline`, `--repair-policy`, and coordination's `--from` are in the current development branch. It also adds the `-L` selector alias; `--filters` already works in v0.8.0. Prefer canonical `--filter` and `-l`. No v0.8.0 flag removal is claimed here.

## Repair policy flags (Upcoming)

In the current development branch, `fix` and `coordinate` share candidate preparation. Use `--startup-baseline file` to authorize exact command/args restoration. Use `--repair-baseline file --repair-policy placement|security` for approved placement or security strengthening. Baselines must match the workload identity; omitted fields grant no authority. Every enabled policy still needs live causal proof and required shadow verification.

`coordinate` supports Deployment, StatefulSet and DaemonSet repairs. `--preview` shows candidates without creating shadows or applying them. `--from` can certify a connected graph containing supported same-namespace workloads, ConfigMaps, Secrets, and selector-based ClusterIP Services in one default-deny shadow namespace. All graph candidates/endpoints and guarded preflights must pass before apply confirmation. `--yes` authorizes apply, but rollback has separate consent. Failed or unknown live verification stops further applies; field-scoped rollback can refuse concurrent conflicts. Secret-bearing graph namespaces are always cleaned. See [source and coordination](../guides/source-and-coordinate.md) and [controller repair policies](controller-repairs.md).

For a PVC-backed `coordinate --from` graph, `--storage-shadow-policy <local-file>` enables exact CSI snapshot preflight and then asks for separate `SNAPSHOT` consent. `--yes` alone cannot authorize snapshots. Storage preview performs no writes; confirmed PVC runs always clean up and cannot be retained. See [PVC snapshot acceptance and policy format](../testing/graph-pvc-snapshot-acceptance.md).
