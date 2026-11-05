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

Scanning needs read access to Pods, workloads, Events, and any optional resource you select. Incident commands read bounded logs by default and require `pods/log`; use `--include-logs=false` to prevent these reads. The [RBAC example](https://github.com/baka126/fixora-cli/blob/v0.8.0/docs/rbac.yaml) includes a read-only ClusterRole plus a separate optional shadow Role. Tailor them to your namespace and analyzers. A denied read becomes a coverage gap.

Shadow verification additionally needs permission to create and delete temporary Pods and NetworkPolicies. Cluster delivery needs write permission on the exact target workload. Source/PR delivery needs local repository access and, if opening a PR, remote credentials. Keep those roles separate where your organization can.

## Data that can leave the workstation

Cluster reads stay local unless you enable AI, use an external MCP client, or publish a report/bundle. With `--ai`, evidence is sent to the configured provider; redaction is a safeguard, not a guarantee for arbitrary content. Do not pass `--unsafe-ai-no-redact` during routine incidents. Never paste raw Secrets, unredacted logs, or credentials into issue reports.

In **v0.8.0**, MCP `get-resource` and `get-logs` use the caller's Kubernetes permissions and can return sensitive content. Use narrow RBAC and a trusted assistant client. The development branch adds a tighter read allowlist and output redaction; see the [MCP guide]({{ '/guides/mcp/' | relative_url }}).

## What shadow verification does

An enabled shadow run clones a Pod or workload template, removes identity fields, labels it as a sandbox, attaches an ingress-deny NetworkPolicy, applies the candidate patch to the clone, waits for selected health signals, and reports parity/cleanup. Egress is allowed by default for parity and can be denied. `--keep-shadow` leaves resources for inspection and transfers cleanup responsibility to the operator.

Shadow readiness cannot prove production traffic, dependencies, controller behavior, or rollout success. A skipped or inconclusive run must not be described as verified. `--quick` can skip the default shadow check.

## Delivery gates and rollback

A concrete, apply-eligible patch is required for cluster delivery. Server-side dry-run is enabled by default and can be disabled with `--apply-dry-run=false`; leave it enabled for production use. Direct cluster apply can be refused for Helm/GitOps-managed objects. In **v0.8.0**, Fixora does not monitor the live rollout or offer a rollback after a successful apply. Observe the rollout and use your established rollback procedure if needed. Post-apply health checks and a reviewed rollback path are **Upcoming** on the development branch. PR delivery changes source and depends on the normal review/deployment process.

For emergencies, run `--preview` first, keep the target context explicit, and preserve a copy of the diff and findings. Use [delivery guidance]({{ '/guides/delivery/' | relative_url }}) for each write mode.

## Limits and troubleshooting

- Missing RBAC, CRDs, Events, logs, or metrics can reduce confidence or skip checks.
- AI outputs may be wrong. Compare them to deterministic proof and the actual diff.
- A healthy shadow clone may still fail in a production rollout.
- Optional integration detection does not guarantee a remediation.
- The release `doctor` routing is inconsistent with one help description; in v0.8.0 it runs the AI setup check. Use `status` plus targeted reads for cluster access, or the branch's upcoming cluster `doctor`.
