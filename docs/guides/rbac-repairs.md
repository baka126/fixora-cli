---
layout: default
title: Policy-bound RBAC repair
description: Preview and create an exact namespaced Role and RoleBinding from local operator-approved permission intent.
permalink: /guides/rbac-repairs/
section: guides
---

# Policy-bound RBAC repair

The read-only `kubectl fixora rbac` command checks whether a ServiceAccount can perform one action. `rbac repair` adds a separate guarded path for a missing namespaced permission. The operator must supply the exact permission set in a local policy; Fixora does not infer grants from AI output, analyzer findings, logs, or an authorization denial.

## Policy

```yaml
apiVersion: fixora.io/v1alpha1
kind: RBACRepairPolicy
metadata:
  namespace: payments
spec:
  serviceAccount: api
  roleName: fixora-api-read-config
  roleBindingName: fixora-api-read-config-binding
  permissions:
    - apiGroup: ""
      resource: configmaps
      verbs: [get, list]
    - apiGroup: ""
      resource: configmaps
      resourceNames: [api-config]
      verbs: [get]
```

The policy must be a regular local file no larger than 1 MiB. It must contain one YAML document and cannot use aliases, duplicate/unknown fields, wildcards, or unsupported verbs. `resourceNames` is omitted or a non-empty list. A named `list` or `watch` permission only applies when the client sends a `metadata.name` field selector matching an approved name. Kubernetes cannot constrain `create` or `deletecollection` by object name, so Fixora refuses those combinations.

Fixora denies all access to Secrets and RBAC resources, ServiceAccount mutation and token creation, Pod execution subresources, and mutations that create or change Pods and workload-producing controllers/jobs. This avoids granting a permission that could indirectly run arbitrary code or consume credentials. Read-only workload access can be included explicitly.

## Preview and apply

```sh
kubectl fixora rbac repair -n payments --rbac-policy ./rbac-repair.yaml
kubectl fixora rbac repair -n payments --rbac-policy ./rbac-repair.yaml --apply
```

Preview performs reads, authorization reviews, and server-side dry-runs; it does not create persistent objects. Apply repeats the checks, requires an interactive confirmation, then creates a dedicated namespaced Role and RoleBinding. For JSON/YAML output, pass `--yes` with `--apply` so a prompt cannot corrupt structured output. `--yes` does not bypass any policy, authorization, admission, freshness, ownership, or collision check.

The caller needs access to create, read, list, and delete namespaced Roles/RoleBindings, and permission to create SubjectAccessReview and SelfSubjectAccessReview objects. Fixora verifies those actions before attempting a write. The target ServiceAccount must exist in the policy namespace. Existing Role/RoleBinding names are never adopted or modified.

Helm, Argo CD, or Flux ownership markers on the namespace or ServiceAccount make the repair review-only; update the source of truth instead. Marker absence cannot prove that an external reconciler is absent. Cluster administrators should still review the local policy and the effective permissions it grants.

Fixora checks the exact requested actions before and after creation. A successful result means the Kubernetes authorization API allows those requests; it does not prove the workload uses the ServiceAccount or that the application recovered. On uncertain create outcomes, inspect the named objects before retrying. Any cleanup is UID-guarded and only targets objects created by that attempt.

## List and revoke a Fixora grant

After a verified apply, Fixora stores a private receipt at `~/.fixora/<cluster-hash>/rbac-grants/<id>.json`. The file is mode `0600` inside a mode `0700` directory and contains only the policy digest, approved permission requests, target identity, and exact Role/RoleBinding specs and UIDs. Keep the receipt on the same machine for as long as you may need to revoke the grant.

```sh
kubectl fixora rbac grants list
kubectl fixora rbac revoke --grant <id> -n payments
kubectl fixora rbac revoke --grant <id> -n payments --apply
```

Listing reads local receipts and kubeconfig context identity only; it does not query the Kubernetes API. If you need to move revocation capability to another workstation, export the receipt and import it there:

```sh
kubectl fixora rbac grants export --grant <id> --out ./grant-receipt.json
kubectl fixora rbac grants import --file ./grant-receipt.json
```

Treat the exported JSON as sensitive authorization metadata: transfer it through a protected channel, keep its mode `0600`, and delete extra copies when they are no longer needed. Export refuses to overwrite an existing path. Import is limited to the same cluster and reads the exact recorded Role and RoleBinding; their UIDs, specs, Fixora markers, and ownership must still match. Import only saves a local receipt and does not change cluster resources. It does not require the original ServiceAccount to still exist.

Revoke requires the same cluster, an explicit matching namespace, and an unchanged receipt-bound Role and RoleBinding. Preview is read-only. Apply asks for confirmation; structured output requires `--yes`.

Fixora removes the recorded RoleBinding first using its UID. It removes the recorded Role only if no other RoleBinding refers to it. If the Role is shared, Fixora keeps the Role and receipt for follow-up. After cleanup, it checks whether the requested actions are still allowed through other grants. An access-remains result does not mean this grant's binding survived, and Fixora does not claim workload recovery. Missing local receipts, changed object identities, or GitOps-managed objects block automatic revocation.
