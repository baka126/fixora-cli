---
layout: default
title: HPA request repairs
description: Diagnose autoscaler issues and safely apply explicitly approved workload requests.
permalink: /guides/hpa-repairs/
section: guides
---

# HPA request repairs

Fixora diagnoses HorizontalPodAutoscaler target, condition and metric failures without guessing what a workload needs. For CPU or memory utilization, a separate local policy can approve exact missing container requests. The generic planner and AI output cannot authorize those values.

Create a policy for one HPA and its same-namespace Deployment or StatefulSet:

```yaml
apiVersion: fixora.io/v1alpha1
kind: HPARepairPolicy
metadata:
  name: api-hpa-repair
  namespace: prod
spec:
  hpa:
    name: api-scale
  target:
    kind: Deployment
    name: api
  requests:
    - container: api
      cpu: 250m
      memory: 256Mi
```

Choose request values from the application's resource requirements, a known-good deployment, or an operator decision. Current usage samples can explain the symptom, but they do not establish a safe sustained request. Each entry names a regular container and at least one positive Kubernetes CPU or memory quantity. Duplicate, unknown or unused policy fields are rejected.

Preview the exact approved additions:

```sh
kubectl fixora fix hpa/api-scale -n prod --hpa-policy ./api-hpa-repair.yaml --preview
```

For an unmanaged target, apply only after selecting cluster delivery and confirming the action:

```sh
kubectl fixora fix hpa/api-scale -n prod --hpa-policy ./api-hpa-repair.yaml --delivery=cluster --yes
```

The preflight requires a current HPA generation, one supported scale target, a stable rollout, all desired owned Pods Ready, complete owner and PodMetrics evidence, and no competing HPA or active replica transition. Fixora server-side dry-runs an atomic patch that adds only the approved missing request fields. A stale HPA or target, denied metrics read, admission rewrite, identity replacement, or changed snapshot blocks the write.

After a confirmed update, Fixora checks the workload rollout and HPA separately. Success requires both the workload to converge and the HPA to observe current supported metrics. A partial result is reported without automatic rollback. In an interactive session, Fixora asks before removing its added request fields; the inverse checks the original target UID and the exact values it wrote, then preserves unrelated edits.

Source-managed targets remain review-only. Update the owning manifest, Helm values or Kustomize overlay and allow its reconciler to apply the change. Fixora does not write a generic source patch for HPA request policies.

This repair path does not change the HPA's metric target, replica bounds, scaling behavior, container limits, image, command, probes, or security settings. It does not prove future scale-out capacity, sustainable load, or application SLOs. Other metric types, cross-namespace targets, multiple HPAs, DaemonSets, KEDA and custom metrics need review.

The isolated local acceptance test is opt-in and uses its own kind cluster and kubeconfig:

```sh
FIXORA_E2E_HPA=1 ./hack/e2e-hpa.sh
```

The harness creates and deletes only a uniquely named local cluster. It installs a pinned metrics-server release because HPA acceptance needs live PodMetrics.
