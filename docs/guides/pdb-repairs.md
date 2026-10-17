# PDB diagnosis and approved repair intent

Fixora distinguishes unavailable selected replicas, missing selections, overlapping budgets, stale controller status and intentional disruption protection. Zero permitted disruptions alone does not prove a configuration error. Full evidence is available through proof output. Diagnosis needs no approval file or AI.

PDB repair implementation is in progress. This document defines the approved local policy schema; do not interpret the example as a reason to relax an application's availability requirement.

```yaml
apiVersion: fixora.io/v1alpha1
kind: PDBRepairPolicy
metadata:
  name: reviewed-availability
  namespace: application
spec:
  pdb: {name: application-protection}
  workload: {kind: Deployment, name: application}
  selector:
    matchLabels: {component: application}
  budget: {minAvailable: 2}
  availability:
    minimumHealthyReplicas: 2
    maximumConcurrentDisruptions: 1
```

Use actual application identities and independently approved availability limits. Only Deployment and StatefulSet targets are supported for repairs. Set exactly one of `minAvailable` or `maxUnavailable`, using an integer or quoted percentage. Numeric strings such as `"2"` are invalid. Percentage budgets round upward; for three healthy replicas, minAvailable `"50%"` requires two healthy replicas, whereas maxUnavailable `"50%"` permits two disruptions. The approved absolute floor and ceiling still constrain those effects.

A policy is one regular local YAML file, at most 1 MiB, with no duplicate/unknown keys, aliases or extra documents. Both availability limits are required; the healthy floor must be positive and the disruption ceiling may be zero. The approval is checked again if the file changes. AI may suggest a policy for review; it cannot approve it.

No repair evicts/deletes Pods, drains nodes, scales a controller or creates a temporary PDB. A healthy but fully protected workload may require no change. Actual eviction success and application quorum are separate from policy verification.
