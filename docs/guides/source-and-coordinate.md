---
layout: default
title: Source validation and coordination
description: Inspect raw manifests, Helm charts, and Kustomize overlays; learn about upcoming coordinated fixes.
permalink: /guides/source-and-coordinate/
section: guides
---

# Source validation and coordination

Fixora can inspect local source before a delivery. Use the same repository revision and kube context that your deployment process will use.

## Inspect and validate source

**Effect:** Reads local files and may perform a server-side dry-run when requested; it does not apply a production workload.

```bash
kubectl fixora repo ./platform
kubectl fixora validate ./platform
kubectl fixora lint -f ./platform
kubectl fixora preflight -f ./platform
```

`repo` identifies raw, Helm, or Kustomize source. `validate` renders and validates; `lint` checks the manifests or chart; `preflight` includes policy and Kubernetes dry-run checks. Rendered validity does not guarantee rollout health or parity with another environment. Use `policy-check -f` for the production policy checks separately.

## Coordinated fixes — Upcoming

`coordinate` and its alias `fix-set` are in the development branch, not v0.8.0. Explicit target lists keep the independent-controller workflow. `--from` discovers a bounded connected graph; when at least two supported workloads are connected, Fixora verifies all workload candidates and supported dependencies together in one isolated namespace. Unsupported or incomplete graph edges stop the whole graph before production changes.

**Effect:** `--preview` reads the connected resources and candidate plans only. A non-preview graph run creates a temporary namespace, copies only referenced ConfigMaps and Secrets, clones the candidate workloads and selector-based Services, and tests the graph behind default-deny NetworkPolicies. After a complete graph certificate, Fixora dry-runs every workload update, asks once before applying, checks source identities before each write, observes each rollout, and offers separate consent to restore the applied prefix if a later step fails.

```bash
kubectl fixora coordinate --from deployment/api -n prod --preview
```

Graph discovery is limited to same-namespace Deployments, StatefulSets, DaemonSets, ConfigMaps, Secrets, selector-based ClusterIP Services with TCP ports, and explicitly authorized PVC snapshots. PVC support is opt-in through `--storage-shadow-policy <local-file>` and a separate exact `SNAPSHOT` confirmation; `--yes` cannot satisfy storage consent. Preview remains read-only. The policy binds exact PVC names and capacity limits to explicit VolumeSnapshotClass and restore StorageClass names. Fixora snapshots source claims, restores them as distinct claims in the run namespace, and verifies CSI identity, capacity, scheduling, and topology before issuing a certificate. PVC-backed graph runs always delete their shadow namespace, temporary ReferenceGrant, source snapshots, and snapshot contents; they cannot use `--keep-shadow`.

PVC snapshots are crash-consistent and may incur CSI provider charges. The operator must enable `AnyVolumeDataSource` and `CrossNamespaceVolumeDataSource` on the API server and kube-controller-manager, enable `CrossNamespaceVolumeDataSource` on the CSI provisioner, and install the VolumeSnapshot CRDs/controller and Gateway API ReferenceGrant support. `--storage-shadow-policy` is a local authorization file, not a Kubernetes resource; see [PVC snapshot acceptance](../testing/graph-pvc-snapshot-acceptance.md) for its schema, permission requirements, and disposable-cluster procedure. Host paths, external/shared data, cross-namespace workload dependencies, unsupported Service modes/ports, image-pull Secrets, custom resources, and ambiguous graph edges remain review-only. A graph must contain at least two connected workload candidates; otherwise Fixora uses the existing independent-target path when available.

The graph shadow requires an enforcing standard NetworkPolicy CNI and a verifiable `kube-system/kube-dns` Service with Ready endpoints. It allows only same-run graph traffic plus DNS to those verified Pods; probes must show in-graph connectivity and blocked external ingress/egress. Failure to establish policy enforcement, DNS identity, or any candidate's readiness blocks the whole graph.

Referenced Secret bytes are copied into same-name Secret objects in the temporary namespace so candidates can start with their configured inputs. They are never written to output, diffs, logs, AI context, or the certificate. A graph containing any Secret is always deleted, even with `--keep-shadow`; cleanup uses the namespace UID and is attempted after cancellation. The operator identity can read these copies while they exist, so scope RBAC accordingly.

Graph preview JSON reports `shadowVerified=false`, `liveVerified=false`, `externalDependenciesTested=false`, and cleanup as not started. A successful graph shadow certifies candidate Pod readiness/stability and exact shadow Service endpoint identities only. It does not test production traffic, external databases/APIs, persistent data, authentication to external systems, or the final production rollout. The feature remains Upcoming until disposable-cluster acceptance is recorded for a CNI that enforces NetworkPolicy.
