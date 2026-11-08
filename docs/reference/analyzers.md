---
layout: default
title: Analyzer catalog
description: Analyzer defaults, optional APIs, and the limits of Kubernetes evidence.
permalink: /reference/analyzers/
section: reference
---

# Analyzer catalog

In v0.8.0, `filters -o json` reports each analyzer's kind, resource, scope, description, and `enabled` default. An explicit `--filter` selects only the named checks. Missing permissions or CRDs can produce skipped checks; no finding is not proof of health.

**Effect:** The first command reads the local analyzer registry. The second reads selected Kubernetes resources and bounded logs by default; add `--include-logs=false` to skip logs. No cluster write.

```bash
kubectl fixora filters -o json
kubectl fixora scan -n prod --filter Pod,Service,PVC
```

The selected filters narrow analyzer coverage; they do not disable incident log collection.

## Enabled by default in v0.8.0

| Area | Analyzers |
| --- | --- |
| Workload and network | `Pod`, `Service`, `Ingress`, `GatewayClass`, `Gateway`, `HTTPRoute` |
| Scaling and disruption | `HPA`, `PDB`, `KedaScaledObject` |
| Storage and infrastructure | `PVC`, `Node` |
| Admission and security reports | `MutatingWebhook`, `ValidatingWebhook`, `KyvernoPolicyReport`, `TrivyVulnerabilityReport`, `TrivyConfigAuditReport` |
| Operator lifecycle | `OLMClusterServiceVersion`, `OLMSubscription`, `OLMInstallPlan`, `OLMCatalogSource`, `OLMOperatorGroup`, `OLMClusterCatalog`, `OLMClusterExtension` |

## Available as explicit filters

`Deployment`, `ReplicaSet`, `StatefulSet`, `DaemonSet`, `Job`, `CronJob`, `ConfigMap`, `NetworkPolicy`, and `StorageClass` are in the v0.8.0 catalog but disabled by default. Select them for a focused investigation. Workload diagnosis through `why` and owned-Pod evidence is separate from whether a registry filter is enabled by default.

## Upcoming catalog additions

`Secret` (key-presence checks without printing values) and `ResourceClaim` (missing DeviceClass references) appear on [codex/production-hardening](https://github.com/baka126/fixora-cli/tree/codex/production-hardening), not in v0.8.0. `--secret-keys` and `--cert-expiry` are branch-only options. Check `filters -o json` on your installed binary before copying branch examples.

## Interpretation

The catalog maps resource status to potential findings; it does not perform a complete security audit, network simulation, cost reconciliation, or production rollout proof. Optional OLM, Gateway API, KEDA, Kyverno, and Trivy checks require their APIs and readable resources. Review `skippedChecks` in structured output when coverage matters.
