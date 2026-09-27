---
layout: default
title: Safety and operations
description: Permissions, data flow, shadow lifecycle, delivery gates, and operational limits.
permalink: /safety/
section: safety
---

# Safety and operations

Fixora runs locally with your Kubernetes identity. Grant the narrowest access needed for the workflow and inspect every proposed write. There is no hosted Fixora control plane in this repository.

## Separate read and write permissions

Scanning needs read access to Pods, workloads, Events, and any optional resource you select. Bounded logs require `pods/log`. Use the [read-only Role example](https://github.com/baka126/fixora-cli/blob/v0.8.0/docs/rbac.yaml) as a starting point, then tailor it to your namespace and analyzers. A denied optional read becomes a coverage gap.

Shadow verification additionally needs permission to create and delete temporary Pods and NetworkPolicies. Cluster delivery needs write permission on the exact target workload. Source/PR delivery needs local repository access and, if opening a PR, remote credentials. Keep those roles separate where your organization can.

## Data that can leave the workstation

Cluster reads stay local unless you enable AI, use an external MCP client, or publish a report/bundle. With `--ai`, evidence is sent to the configured provider; redaction is a safeguard, not a guarantee for arbitrary content. Do not pass `--unsafe-ai-no-redact` during routine incidents. Never paste raw Secrets, unredacted logs, or credentials into issue reports.

In **v0.8.0**, MCP `get-resource` and `get-logs` use the caller's Kubernetes permissions and can return sensitive content. Use narrow RBAC and a trusted assistant client. The development branch adds a tighter read allowlist and output redaction; see the [MCP guide]({{ '/guides/mcp/' | relative_url }}).

## What shadow verification does

An enabled shadow run clones a Pod or workload template, removes identity fields, labels it as a sandbox, attaches an ingress-deny NetworkPolicy, applies the candidate patch to the clone, waits for selected health signals, and reports parity/cleanup. Egress is allowed by default for parity and can be denied. `--keep-shadow` leaves resources for inspection and transfers cleanup responsibility to the operator.

Shadow readiness cannot prove production traffic, dependencies, controller behavior, or rollout success. A skipped or inconclusive run must not be described as verified. `--quick` can skip the default shadow check.

## Delivery gates and rollback

A concrete, apply-eligible patch and server-side dry-run are part of the guarded path. Direct cluster apply can be refused for Helm/GitOps-managed objects. After a cluster apply, inspect the health result; a rollback is offered for operator review and is not automatic under `--yes`. PR delivery changes source and depends on the normal review/deployment process.

For emergencies, run `--preview` first, keep the target context explicit, and preserve a copy of the diff and findings. Use [delivery guidance]({{ '/guides/delivery/' | relative_url }}) for each write mode.

## Limits and troubleshooting

- Missing RBAC, CRDs, Events, logs, or metrics can reduce confidence or skip checks.
- AI outputs may be wrong. Compare them to deterministic proof and the actual diff.
- A healthy shadow clone may still fail in a production rollout.
- Optional integration detection does not guarantee a remediation.
- The release `doctor` routing is inconsistent with one help description; in v0.8.0 it runs the AI setup check. Use `status` plus targeted reads for cluster access, or the branch's upcoming cluster `doctor`.
