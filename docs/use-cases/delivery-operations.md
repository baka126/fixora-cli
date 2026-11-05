---
layout: default
title: Delivery and operations failures
description: Investigate node pressure and source-managed rollouts without bypassing deployment controls.
permalink: /use-cases/delivery-operations/
section: use-cases
---

# Delivery and operations failures

Some symptoms are rooted in shared infrastructure or deployment source. Read the owner and scope before choosing a fix; a local workload patch may be overwritten by a controller.

## Node pressure

**Effect:** Reads Node conditions, taints, capacity, and related eviction signals. The `scan` command also reads bounded logs by default; use `--include-logs=false` to skip them. No cluster writes.

```bash
kubectl fixora node-pressure
kubectl fixora scan -A --filter Node,Pod
```

Compare memory, disk, PID pressure, NotReady state, and Pod placement. Node data can be stale or incomplete when RBAC blocks cluster-scoped reads. Drain, autoscaling, and capacity purchases remain separate operator decisions.

## Helm and GitOps-managed workloads

**Effect:** Reads ownership hints and local source. The commands below do not apply to the cluster.

```bash
kubectl fixora repo ./platform
kubectl fixora validate ./platform
```

When Helm or GitOps controls a workload, direct cluster apply can drift from source or be refused. Use [source validation and delivery]({{ '/guides/source-and-coordinate/' | relative_url }}) to review a manifest/chart/overlay change, then let the normal deployment path reconcile it. A successful render cannot prove the live environment will be healthy.

## Optional ecosystem signals

The analyzer catalog can include HPA, PDB, KEDA, OLM, Kyverno policy reports, Trivy Operator reports, and Gateway API resources. They are relevant only when their CRDs or APIs exist and the caller can read them. Run `filters -o json` to see the catalog and enabled state for your binary; [the analyzer reference]({{ '/reference/analyzers/' | relative_url }}) explains coverage and limits.
