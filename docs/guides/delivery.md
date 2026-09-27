---
layout: default
title: Verification and delivery
description: Understand shadow resources, local patches, cluster apply, and GitOps pull request delivery.
permalink: /guides/delivery/
section: guides
---

# Verification and delivery

The delivery mode decides where a reviewed change goes. It does not replace patch review, Kubernetes authorization, or rollout observation.

## Shadow verification

**Effect:** Creates a temporary shadow Pod and NetworkPolicy in the cluster, reads their health, and deletes them afterward unless `--keep-shadow` is set. No production workload is changed by this option alone.

```bash
kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/example/api:v1.2.3 --shadow --delivery patch
```

The clone omits workload identity and can differ from production traffic, dependencies, and policy. Its readiness is useful evidence, not proof of production success. Egress is allowed by default for parity; choose `--shadow-egress deny` when isolation is more important than outbound parity. Inspect the reported status; `--quick` skips default shadow verification.

## Local patch

`--delivery patch` writes a patch for inspection. It does not update the production object. Keep the output with your incident record and review it before using another tool to apply it.

## Cluster apply

**Effect:** After eligibility checks, confirmation, and server-side dry-run, writes to the target production workload. It can trigger a rollout.

```bash
kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/example/api:v1.2.3 --shadow --delivery cluster
```

Fixora observes post-apply health and may offer a structured rollback command if the rollout fails. Rollback is not automatic under `--yes`; an operator must review and approve it. Ensure your RBAC permits the exact target write and shadow resources.

## Source or pull request

**Effect:** Reads a local manifest, Helm chart, or Kustomize source in `--repo`; a PR delivery writes the source, commits a branch, and may push/open a GitHub PR or GitLab MR. It does not directly apply the production workload.

```bash
kubectl fixora fix deployment/api -n prod --repo ./platform --gitops --shadow --delivery pr
```

Managed workloads can reject direct cluster apply and route you to source review. Source mapping must be unambiguous; validation failures should stop delivery. Review the rendered result, diff, base branch, repository permissions, and remote before approving a PR. See [source validation]({{ '/guides/source-and-coordinate/' | relative_url }}).
