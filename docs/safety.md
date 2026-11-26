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

Fixora sends no product telemetry by default. On a `fix` command, `--telemetry` opts in to one small report sent to the HTTPS collector configured by `FIXORA_TELEMETRY_URL`; `--no-telemetry` overrides the opt-in. Fixora does not provide or select a collector. The endpoint operator controls access and retention. Optional `FIXORA_TELEMETRY_AUTH_TOKEN` is sent only as a bearer token to that HTTPS endpoint, and redirects are not followed. If the endpoint is missing, rejects the report, or is unavailable, Fixora prints a warning and leaves the diagnosis, verification, and delivery result unchanged.

The versioned report contains only the Fixora version, a coarse allow-listed failure category, the furthest workflow stage, verification verdict, retry count, optional parity score, and whether a cluster change was applied. It excludes pod/resource names, namespaces, image references, patches, logs, Events, AI prompts/responses, and configuration or Secret values. The report has no Fixora-generated session identifier or timestamp. The collector can still observe normal network metadata such as source IP; review its privacy and retention policy before opting in. See the [telemetry guide]({{ '/guides/telemetry/' | relative_url }}) for setup and the exact fields.

Fixora's optional per-workload history is local at `~/.fixora/<cluster-hash>/profiles.json`. The containing directory is mode `0700`, the file is mode `0600`, and updates replace the file atomically. It stores opaque cluster/workload fingerprints, bounded outcome/strategy/parity fields, and numeric readiness or memory measurements. A validated memory limit is retained only from a verified patch. It does not store API endpoints, resource names, image references, full patches, AI text, logs, Events, prompts, or Secret/ConfigMap values. Only a verified prior session can be summarized as historical evidence for a later AI analysis. It cannot authorize a repair or bypass current evidence, policy, shadow, or delivery checks. Missing, corrupt, or unwritable history is ignored; a save failure emits a warning and leaves the current shadow result unchanged.

In **v0.8.0**, MCP `get-resource` and `get-logs` use the caller's Kubernetes permissions and can return sensitive content. Use narrow RBAC and a trusted assistant client. The development branch adds a tighter read allowlist and output redaction; see the [MCP guide]({{ '/guides/mcp/' | relative_url }}).

## What shadow verification does

An enabled shadow run clones a Pod or workload template into a unique namespace, strips workload credentials, and refuses to create the candidate until positive and negative controls prove standard NetworkPolicy ingress and egress enforcement. The candidate runs under default-deny ingress and egress. NetworkPolicy API uncertainty, unsupported global/CNI policy APIs, missing probe prerequisites, or omitted namespaced dependencies block or make verification inconclusive; no candidate delivery follows those outcomes. The run namespace is deleted with UID preconditions and deletion is awaited. `--keep-shadow` leaves the isolated namespace and transfers cleanup responsibility to the operator.

`--shadow-shell` is a separate interactive opt-in available only after a verified shadow verdict. Fixora rechecks the exact generated namespace and Pod UIDs and their run labels, confirms `create` on `pods/exec`, and asks before invoking a shell. The identity requires this additional execution permission in the dynamic shadow namespace; grant it only to operators authorized to run commands in candidate workloads. Shell actions are confined to the retained shadow Pod under default-deny ingress and egress and never alter the approved patch artifact. After exit, Fixora deletes the run namespace unless `--keep-shadow` was selected.

Shadow readiness cannot prove production traffic, dependencies, controller behavior, or rollout success. A skipped or inconclusive run must not be described as verified. `--quick` can skip the default shadow check.

The development branch's combined `coordinate --from` shadow has a separate, bounded dependency-copy path. It copies only exact same-namespace ConfigMap and Secret references, runs all selected workload candidates and selector-based ClusterIP Services together, and permits only verified same-run traffic plus DNS to discovered Ready cluster DNS Pods. Every required candidate and Service endpoint must pass, with no unexpected objects, before Fixora creates a graph certificate. PVCs, host paths, cross-namespace and external dependencies, image-pull Secrets, unsupported resources or Service modes, and unavailable/unproven NetworkPolicy enforcement block certification.

PVC-backed combined graph verification is opt-in with a strict local `--storage-shadow-policy` and a separate typed `SNAPSHOT` confirmation; `--yes` is not storage authorization. It makes a crash-consistent snapshot of each exact authorized source PVC, temporarily grants only the run namespace's restored PVCs access to those snapshots, and creates distinct restored claims. The production PVC is never mounted by a shadow Pod. Fixora verifies the CSI provisioner by completing a runtime restore, then checks the restored PV, driver, capacity, scheduled Pod, and topology. Snapshot backend handles and PVC contents are excluded from evidence and certificates. Any PVC-backed run is non-retainable; certification requires successful deletion of the shadow namespace, ReferenceGrant, snapshots, and snapshot contents. Snapshotting may charge for temporary provider storage. See [PVC snapshot acceptance](testing/graph-pvc-snapshot-acceptance.md) for prerequisites and cleanup limits.

Secret values are copied only into ephemeral same-name Secrets inside the run namespace. They are never placed in a Fixora receipt, output, diff, AI prompt, or log. A graph with any copied Secret is deleted regardless of `--keep-shadow`; cleanup checks namespace ownership and UID and uses an independent cleanup context after cancellation. Cluster principals authorized to read that namespace can still read Secret copies before deletion. Grant those permissions only to the operator identity that runs the graph shadow.

## Delivery gates and rollback

A concrete, apply-eligible patch is required for cluster delivery. Server-side dry-run is enabled by default and can be disabled with `--apply-dry-run=false`; leave it enabled for production use. Direct cluster apply can be refused for Helm/GitOps-managed objects. In **v0.8.0**, Fixora does not monitor the live rollout or offer a rollback after a successful apply. The current development branch checks post-apply readiness/completion for supported Pods, Deployments, StatefulSets, DaemonSets, Jobs, and CronJobs. Unsupported kinds, inconclusive reads, and timeouts are reported as skipped or unknown; these checks do not establish application SLOs or dependency health. On a detected rollout failure, Fixora shows evidence and a resource-aware rollback option. Rollback requires separate interactive confirmation; `--yes` never triggers it and prints the command for an operator to run. GitOps and PR deliveries still rely on source reconciliation and the normal review/deployment process.

For emergencies, run `--preview` first, keep the target context explicit, and preserve a copy of the diff and findings. Graph shadow is not production verification: external dependency behavior, persistent data, production traffic, and rollout success still require their own checks. Use [delivery guidance]({{ '/guides/delivery/' | relative_url }}) for each write mode.

## Limits and troubleshooting

- Missing RBAC, CRDs, Events, logs, or metrics can reduce confidence or skip checks.
- AI outputs may be wrong. Compare them to deterministic proof and the actual diff.
- A healthy shadow clone may still fail in a production rollout.
- Optional integration detection does not guarantee a remediation.
- The release `doctor` routing is inconsistent with one help description; in v0.8.0 it runs the AI setup check. Use `status` plus targeted reads for cluster access, or the branch's upcoming cluster `doctor`.
