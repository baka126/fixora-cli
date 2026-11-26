---
layout: default
title: Controller repairs
description: Common workload repair proposals and controller-specific delivery safeguards.
permalink: /reference/controller-repairs/
section: reference
---

# Common repairs across workload types

**Upcoming controller parity checkpoint**, reviewed 2026-10-05. Fixora uses the same evidence-driven image, resource, ConfigMap reference, network probe, ephemeral cache, and approved connection repair families across Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, and CronJob template shapes. AI proposals are validated by their changed fields, independently of the finding category. This extends proposal coverage; it does not establish that every failure is automatically repairable.

Without AI, Fixora reports observed causes and can infer a concrete repair when sufficient evidence establishes the intended change. With `--ai`, a workload-specific model patch must pass live identity/container checks, the strategy allowlist, and shadow verification. A narrative, guessed command, or confidence score alone cannot enable delivery. An intended connection source must be named by the operator on the owning workload; discovering a ready database Service does not establish intent.

| Target | Direct delivery of an eligible proposal | Runtime verification |
| --- | --- | --- |
| Deployment | Existing verified repair behavior | Rollout status |
| StatefulSet | RollingUpdate with no excluded partition; safe shadow source required | Same target UID, observed generation, applied template, all updated replicas Ready and revision agreement |
| DaemonSet | RollingUpdate; resource sizing remains source review until fleet capacity is proven | Same target UID, observed generation, applied template and fleet update/availability counts |
| ReplicaSet | Concrete source-review proposal; no automatic Pod replacement | Existing Pods are not reported repaired by a template edit |
| Job | Concrete source-review proposal; no automatic delete/recreate/replay | Batch shadow completion is retained, but does not authorize replaying a production Job |
| CronJob | Verified future Job template update | Applied template readback; future scheduled-run verification remains pending. Past Jobs remain unchanged |
| Standalone Pod | Image changes only, subject to server compatibility and shadow verification | Same Pod UID, requested spec image, canonical runtime image/digest, Ready condition and readiness gates |

ConfigMap connection and ephemeral cache builders now generate the appropriate envelope for every target in this table. Job, ReplicaSet, and unsupported Pod mutations retain their concrete patches for source review. Existing arbitrary startup-command proposals, security relaxation, secret values, PVC/external storage shadows, and unsupported fields remain blocked or review-only. Startup restoration across workload envelopes requires an explicit matching operator baseline and causal evidence from the selected failing container; guessed commands remain review-only.

Ownership follows the single `controller: true` reference and verifies namespace, kind, API version, name, and UID at every supported edge. This resolves Pod → ReplicaSet → Deployment and Pod → Job → CronJob without label matching or generated-name guesses. Non-controlling references are ignored. Missing/replaced/ambiguous/custom controlling owners preserve diagnostic evidence and block automatic delivery. Source ownership hints are collected from the resolved workload as well as the Pod.

New direct-delivery targets require live preflight and server dry-run. OnDelete strategies, nonzero StatefulSet partitions, stale container/probe/filesystem configuration, and replacement target/Pod identities require review. A private apply file carries target UID and resourceVersion preconditions through dry-run and apply; the portable source patch remains unchanged. Concurrent updates can reject delivery and require fresh evidence. `--quick`, `--yes`, and `--force-risky` do not remove these requirements. Coordinated apply now shares single-workload preparation and requires a fresh proof and successful shadow verification for every selected controller repair.

Example:

```sh
kubectl fixora fix StatefulSet/worker -n application --ai --preview
kubectl fixora fix StatefulSet/worker -n application --ai --delivery=cluster
kubectl fixora fix CronJob/nightly -n application --ai --preview
```

Structured shadow delivery results distinguish shadow verification from live verification with `liveVerification` and `liveSummary`. A CronJob update reports `cronjob-future-verification-pending`; acceptance is not a successful future run. Runtime image identities that cannot be tied to the proposed reference remain unverified, including some runtime-specific digest representations.

## Verification and limits

Unit coverage includes seven template envelopes, malformed/ambiguous shape rejection, dynamic AI candidates for five common field families, approved connection inference across all seven kinds, immutable-target/source-review policies, owner identity checks, Pod readiness/runtime images, controller readback, CronJob future verification, and coordinated-delivery refusal. Existing Deployment demo regressions remain part of the suite.

Full unit and race tests, vet, the CLI build, and E2E compilation passed for this checkpoint. Cluster E2E execution and live EKS delivery are separate validation steps; compiling E2E tests does not establish that they ran on a cluster. Helm values/source mapping, combined dependency sandboxes and automatic batch replay remain subsequent roadmap work.

## Step 3: proof-bound repair policies

The `codex/verification-policies` checkpoint adds three opt-in policies. A matching baseline expresses operator intent; Fixora also requires current causal evidence, a bound candidate, shadow verification and guarded live delivery. `--quick`, `--yes` and `--force-risky` cannot bypass these requirements. Serialized plans and AI output contain no usable proof or verification receipt.

| Policy | Authority and evidence | Limits |
| --- | --- | --- |
| Startup (`--startup-baseline`) | Exact command/args for the same workload, container and image; current failing container and runtime evidence | AI cannot revise the approved command. Unrelated sidecar/init failures or guessed healthy behavior require review. |
| Placement (`--repair-baseline`, `--repair-policy placement`) | Explicit selector, required node affinity or exact NoSchedule toleration; current predicate failure, readable nodes, compatible image platforms and capacity | No wildcard tolerations, unsafe taints, topology spread, custom schedulers, node pinning or Pod affinity. One shadow node does not prove every replica's placement. |
| Security (`--repair-baseline`, `--repair-policy security`) | Explicit permitted strengthening fields, enforced Pod Security Admission and current revision-linked admission denial | Preserve UID/GID and application identity. No privilege relaxation, guessed identities, persistent data ownership changes or unsupported policy versions. |

Eligible Deployment, StatefulSet and DaemonSet repairs use their guarded rollout checks. CronJob delivery changes future templates and remains pending future-run verification. Job, ReplicaSet and unsupported standalone Pod edits stay source-review proposals.

## Coordinated delivery

`coordinate Deployment/first StatefulSet/second -n application --ai` preserves the given order for independently verifiable controller repairs. It prepares candidates through the same evidence, inference and AI pipeline as `fix`, verifies every shadow, captures field inverses, and passes all guarded preflights before confirming production apply. Each apply rechecks the live target and uses the exact certified bytes. Unknown or pending live outcomes stop the set.

Rollback requires separate consent, including when `--yes` authorizes apply. It restores only the fields changed by each applied step, in reverse order, under the original UID and a current resourceVersion. A concurrent change to those fields refuses rollback; unrelated edits are preserved. A restored original configuration may still have the original incident. This is an ordered saga, not an atomic Kubernetes transaction.

`--from` derives relationships from actual ConfigMap/Secret/PVC references and Service selectors. These dependency resources are outside this checkpoint's verified controller mutation scope; unsupported multi-resource sets require source review. Combined verification of dependent candidates, automatic Pod/ReplicaSet/Job replay and coordinated CronJob delivery remain unsupported.
