# PDB diagnosis and approved repair intent

Fixora distinguishes unavailable selected replicas, missing selections, overlapping budgets, stale controller status and intentional disruption protection. Zero permitted disruptions alone does not prove a configuration error. Full evidence is available through proof output. Diagnosis needs no approval file or AI.

The approved local policy below supplies repair intent. Do not interpret the example as a reason to relax an application's availability requirement.

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

## Preview and delivery

```sh
kubectl fixora fix pdb/application-protection -n application --pdb-policy ./approved-pdb.yaml --preview
kubectl fixora fix pdb/application-protection -n application --pdb-policy ./approved-pdb.yaml --ai --preview
kubectl fixora fix pdb/application-protection -n application --pdb-policy ./approved-pdb.yaml --delivery=cluster
```

Default delivery prints a reviewed patch. Add `--out <file>` to save it, or `--delivery=cluster` for an explicitly confirmed live correction. `--yes` confirms that delivery, not an automatic rollback after failure. `--timeout` bounds the operation; proof output includes detailed evidence. Preview and JSON/YAML output never mutate cluster resources or source files, even with apply/source flags.

Preflight checks admission and current evidence without creating resources. After a confirmed write, Fixora waits for current-generation PDB status to agree with the approved policy. Concurrent multi-resource changes cannot be checked atomically; drift refuses a verified-success claim. A lost write response reports uncertainty and never triggers a retry or automatic rollback. An explicit inverse restores only Fixora-edited fields and refuses conflicting edits; the original policy may not meet the approved repair limits.

Direct repair requires all replicas Ready, complete default rollouts, no active HPA on the target, no in-flight disruptions and no overlapping budget. Selector correction requires a currently empty selection and a replacement covering exactly the intended workload. Partial selections, unsupported controllers and changes to unhealthyPodEvictionPolicy remain review-only.

For raw/Kustomize sources, `--delivery=pr --repo <path> --yes` uses the existing reviewed patch workflow, requiring a clean worktree/index and checking files again before writing. Review raw patch blocks before integrating them. Source writes/PRs do not prove production reconciliation. Helm values translation stays manual and cannot use workload-only render certificates.
