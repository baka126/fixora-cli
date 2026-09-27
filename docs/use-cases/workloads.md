---
layout: default
title: Workload failures
description: Investigate crash loops, probes, image pulls, OOM, Pending Pods, and batch workloads.
permalink: /use-cases/workloads/
section: use-cases
---

# Workload failures

These investigations read workload status, related Pods/Events, and bounded logs by default. Use `--include-logs=false` on each `scan` or `why` command if log access is not permitted. A suggested patch is a hypothesis until you inspect its diff and verification result.

## Crash loops and probes

**Effect:** Reads deployment, owned Pods, container states, restart counts, probes, Events, and bounded Pod logs. No cluster writes.

```bash
kubectl fixora why deployment/api -n prod --proof --include-logs
```

Look for `CrashLoopBackOff`, failing startup/liveness probes, exit codes, and repeated log signals. Distinguish an application crash from a probe that restarts a healthy process. Logs may be missing after rotation or due to RBAC; Fixora cannot prove application correctness from readiness alone.

## Image pulls and architecture

**Effect:** Reads Pod status, image pull Events, node architecture, and bounded logs by default. No cluster writes.

```bash
kubectl fixora why deployment/api -n prod --proof
```

Review `ImagePullBackOff`, registry authorization, missing tags, and `exec format error` evidence. If a replacement image is known, preview a pinned image change in the [incident workflow]({{ '/guides/incident-workflow/' | relative_url }}). Fixora does not discover a trusted image version for you or validate registry provenance.

## Out of memory and resource pressure

**Effect:** Reads container termination reasons, configured requests/limits, Events, available node signals, and bounded logs by default. No cluster writes.

```bash
kubectl fixora why deployment/api -n prod --proof
```

An `OOMKilled` status supports a memory diagnosis; it does not by itself determine the right limit. Compare historical usage and application behavior before a `--memory-request` or `--memory-limit` change. Node pressure can cause evictions that require a different response.

## Pending and scheduling

**Effect:** Reads Pod scheduling conditions, Events, node readiness/taints, related capacity signals, and bounded logs by default. No cluster writes.

```bash
kubectl fixora scan -n prod --filter Pod,Node,PVC
```

Check insufficient resources, affinity, taints, unavailable volumes, and quota messages. A scheduler Event describes why a placement failed at that time; it may change as the cluster changes. Do not assume every Pending Pod can be fixed by increasing capacity.

## Jobs and CronJobs

**Effect:** Reads Job/CronJob status, owned Pods/Events, and bounded logs by default. No cluster writes.

```bash
kubectl fixora scan -n batch --filter Job,CronJob,Pod
```

Look for failed completions, backoff, suspension, missed schedules, and Pod failures. The `Job` and `CronJob` analyzers are available but may not be enabled in the default analyzer selection; specify them explicitly when investigating batch work. Fixora does not replay a Job automatically.
