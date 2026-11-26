---
layout: default
title: NetworkPolicy repair
description: Prepare and verify exact NetworkPolicy replacements from operator authority.
permalink: /guides/networkpolicy-repairs/
section: guides
---

# NetworkPolicy repair

Fixora can prepare an exact replacement for one standard, namespaced `networking.k8s.io/v1` NetworkPolicy from a local operator authority file. The policy file, not AI, is the source of every selector, rule, direction, and test case. Preview and diagnosis are read-only.

```sh
kubectl fixora fix NetworkPolicy/database-policy -n production \\
  --networkpolicy-policy ./database-policy-repair.yaml --preview --proof
```

An authority document uses `apiVersion: fixora.io/v1alpha1`, `kind: NetworkPolicyRepairPolicy`, and specifies the exact target, one Deployment/StatefulSet/DaemonSet, the complete `networkPolicySpec`, and verification intent. Consult the [authority example](examples/networkpolicy-authority.yaml) for the schema; replace its placeholder image digest with the digest of an approved probe image. The supported policy subset requires a non-empty Pod selector, explicit policy types and ingress/egress rule presence, selector-only peers, and explicit numeric TCP ports. IP blocks, named ports, and ambiguous/unsupported Kubernetes semantics are rejected for this workflow.

Applying requires `--apply` and interactive confirmation (or explicit `--yes`):

```sh
kubectl fixora fix NetworkPolicy/database-policy -n production \\
  --networkpolicy-policy ./database-policy-repair.yaml --apply
```

Before a write, Fixora requires complete current policy, workload, namespace, Pod, and owner evidence; refuses GitOps/Helm-managed objects, overlapping standard policies, unready or ambiguous workloads, and state drift; and performs isolated TCP tests in per-run disposable namespaces. The verifier first checks an unpolicied allow baseline and a default-deny control, then runs every declared allow/deny case twice. Temporary resources carry unique annotations and are removed only with matching UID and run marker. Incomplete cleanup or uncertain writes are reported as such and never produce a verification receipt.

The result certifies only the admitted policy spec and the declared TCP cases under the active CNI. It does not establish application health, all traffic paths, future CNI behavior, or correctness of undeclared traffic. Fixora does not alter the workload or create/delete the production policy. For source-managed resources, update the GitOps/Helm source of truth instead of applying a live edit. The library can restore the original spec only from its in-memory apply receipt with separate consent; the CLI does not persist rollback state. To restore later with Fixora, prepare a fresh authority file containing the reviewed prior spec and use the same isolated verification and explicit apply flow, or restore through the source of truth.

The disposable probe image must be digest-pinned in the local authority. The CNI must enforce Kubernetes NetworkPolicy semantics; if the baseline or deny control fails, Fixora stops before delivery.

The tagged real-CNI acceptance test is opt-in and creates a uniquely named local Kind cluster plus private registry, installs Calico, and deletes only those resources on exit. Run it with `FIXORA_RUN_NETWORKPOLICY_KIND_E2E=1 ./hack/e2e-networkpolicy.sh`; Docker, Kind, kubectl, Go, and registry image pulls are required. The harness refuses to reuse cluster, registry, and network names.
