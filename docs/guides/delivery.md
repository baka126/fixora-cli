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

**Effect:** Reads cluster evidence and bounded logs by default, then creates a temporary shadow Pod and NetworkPolicy, reads their health, and deletes them afterward unless `--keep-shadow` is set. No production workload is changed by this option alone. Use `--include-logs=false` to skip log reads.

```bash
kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/example/api:v1.2.3 --shadow --delivery patch
```

The clone omits workload identity and can differ from production traffic, dependencies, and policy. Its readiness is useful evidence, not proof of production success. Egress is allowed by default for parity; choose `--shadow-egress deny` when isolation is more important than outbound parity. Inspect the reported status; `--quick` skips default shadow verification.

## Local patch

`--delivery patch` writes a patch for inspection. It does not update the production object. Keep the output with your incident record and review it before using another tool to apply it.

## Cluster apply

**Effect:** Reads cluster evidence and bounded logs by default. After eligibility checks, confirmation, and a server-side dry-run when enabled (the default), writes to the target production workload. It can trigger a rollout. Use `--include-logs=false` to skip log reads.

```bash
kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/example/api:v1.2.3 --shadow --delivery cluster
```

In **v0.8.0**, a successful apply is the end of this command: Fixora does not monitor the live rollout or offer a rollback. Watch the rollout with your normal Kubernetes, Helm, or GitOps tooling and follow your team's rollback runbook if health degrades. Automatic post-apply health checks and a reviewed rollback path are **Upcoming** on the development branch. Ensure your RBAC permits the exact target write and shadow resources.

## Source or pull request

**Effect:** Reads cluster evidence and bounded logs by default, creates and cleans up a temporary shadow Pod and NetworkPolicy, and reads source in `--repo`. A PR delivery writes the source, commits a branch, and may push/open a GitHub PR or GitLab MR. It does not directly apply the production workload. Use `--include-logs=false` to skip log reads.

```bash
kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/example/api:v1.2.3 --repo ./platform --gitops --shadow --delivery pr --yes
```

In v0.8.0, PR delivery requires `--yes` to acknowledge the remote Git operation; other review prompts may still appear. Managed workloads can reject direct cluster apply and route you to source review. Source mapping must be unambiguous; validation failures should stop delivery. Review the rendered result, diff, base branch, repository permissions, and remote before approving a PR. See [source validation]({{ '/guides/source-and-coordinate/' | relative_url }}).
