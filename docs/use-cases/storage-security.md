---
layout: default
title: Storage and security failures
description: Investigate PVC binding, RBAC denials, admission policies, and Secret references.
permalink: /use-cases/storage-security/
section: use-cases
---

# Storage and security failures

The commands here begin with reads. Secret values are sensitive: Fixora's optional key check inspects names and validity without printing values. Follow your organization's access rules when collecting evidence.

## PVC and storage

**Effect:** Reads PVC, PV, StorageClass, related Pod, and Event state. The `scan` command also reads bounded logs by default; use `--include-logs=false` to skip them. No cluster writes.

```bash
kubectl fixora storage -n prod
kubectl fixora scan -n prod --filter PVC,StorageClass,StatefulSet
```

Check Pending/Lost claims, provisioning Events, class existence, capacity, and attachment constraints. A bound claim does not prove the filesystem is healthy inside the container. The `StorageClass` and `StatefulSet` analyzers can be selected explicitly when they are outside the default set.

Policy-bound repair is available for a narrowly approved class assignment or monotonic volume expansion. See the [PVC repair guide]({{ '/guides/pvc-repairs/' | relative_url }}); Lost claims, reclaim/rebind changes, data recovery, and source-managed storage remain review-only.

## RBAC and policy

**Effect:** Reads service-account authorization and security/policy signals. It does not grant permissions or change policy.

```bash
kubectl fixora rbac -n prod
kubectl fixora security -n prod
```

Use the exact service account, verb, and resource when narrowing an RBAC question. Admission failures can involve built-in security context, webhook policy, or a third-party controller. A denied read may hide policy details; do not interpret missing evidence as permission to proceed.

## Secret references

The `--secret-keys` switch is **Upcoming** on [codex/production-hardening](https://github.com/baka126/fixora-cli/tree/codex/production-hardening); it is not available in v0.8.0.

**Effect:** When enabled, reads Secret key names and types, live Pods, and Pod templates from Deployments, StatefulSets, DaemonSets, ReplicaSets, ReplicationControllers, Jobs, and CronJobs, plus referenced ServiceAccounts. It checks required `secretKeyRef` keys, Secret and projected-volume item keys, whole-Secret references, and Pod/ServiceAccount image-pull Secret type and required key names. It never reports encoded or decoded Secret values and makes no writes. The `scan` command also reads bounded logs by default; use `--include-logs=false` to skip them.

```bash
kubectl fixora scan -n prod --filter Secret,Pod --secret-keys
```

This can find missing references in a workload template before its Pod is created. It does not test whether a credential works against an external service. The check needs read access to Secrets, supported workload resources, and referenced ServiceAccounts; see `skippedChecks` because missing permissions make coverage inconclusive. Avoid sharing unredacted logs or `--unsafe-ai-no-redact` output during an incident.
