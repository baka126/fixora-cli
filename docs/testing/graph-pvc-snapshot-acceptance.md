---
layout: default
title: Combined graph PVC snapshot acceptance
description: Prepare and run the opt-in CSI snapshot and restore acceptance against a disposable Kubernetes cluster.
permalink: /testing/graph-pvc-snapshot-acceptance/
section: testing
---

# Combined graph PVC snapshot acceptance

This test performs real CSI snapshots and restores. Run it only against a disposable cluster and storage classes whose temporary volumes and snapshots may be deleted. The test creates a unique source namespace, writes a known sentinel file to a source PVC, runs a connected graph shadow, verifies a distinct restored PV and scheduled node, checks that the candidate becomes Ready only when the sentinel restored from the snapshot exists, confirms the source sentinel remains unchanged, and checks that snapshot contents were removed. The test's cleanup removes the source namespace.

## Cluster requirements

- Use a disposable Kubernetes context with an enforcing NetworkPolicy CNI. The graph runner's isolation probes must pass.
- The cluster must serve `snapshot.storage.k8s.io/v1` VolumeSnapshot, VolumeSnapshotClass, and VolumeSnapshotContent APIs and have a working CSI snapshot controller and CSI driver.
- Kubernetes' alpha [cross-namespace volume data source flow](https://kubernetes.io/docs/concepts/storage/volume-populators-and-data-sources/) requires `AnyVolumeDataSource` and `CrossNamespaceVolumeDataSource` on the API server and kube-controller-manager. The CSI external provisioner must enable `CrossNamespaceVolumeDataSource`. A compatible Gateway API `ReferenceGrant` CRD must be installed. The cluster administrator owns these settings; many managed clusters do not expose control-plane feature gates. Check the current [Kubernetes feature gate reference](https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/) for the release deployed in the test cluster.
- The snapshot class must use the configured CSI driver and `deletionPolicy: Delete`. The restore StorageClass must use the same CSI provisioner and support filesystem, `ReadWriteOnce` claims. `WaitForFirstConsumer` is supported and is exercised when configured.
- Use small temporary volumes. Snapshot operations can incur provider charges; deleting a Kubernetes object does not guarantee immediate cloud-provider billing cessation.
- The test identity needs the graph runner's namespace, Pod, ConfigMap, NetworkPolicy, and discovery permissions plus PVC/PV/StorageClass reads, snapshot class/content reads, and create/get/delete access for VolumeSnapshots and ReferenceGrants. The CSI provisioner also needs its own snapshot and cross-namespace data source permissions.

## Environment and command

The context must already be selected as the current kube context. Required values:

**Effect:** Sets explicit gates and cluster/class/image selections for the opt-in acceptance; it makes no Kubernetes changes.

```bash
export FIXORA_GRAPH_STORAGE_E2E=1
export FIXORA_GRAPH_STORAGE_CONTEXT='disposable-context'
export FIXORA_GRAPH_STORAGE_SNAPSHOT_CLASS='approved-delete-snapshot-class'
export FIXORA_GRAPH_STORAGE_RESTORE_CLASS='approved-disposable-storage-class'
export FIXORA_GRAPH_STORAGE_PROBE_IMAGE='registry.example/fixora/service-probe@sha256:<digest>'
```

`FIXORA_GRAPH_STORAGE_PROBE_IMAGE` must be the approved digest-pinned Fixora service probe image. The workload image used for the sentinel test is the public BusyBox 1.36 image. Ensure the disposable cluster can pull both images.

Compile without running against a cluster:

**Effect:** Compiles the tagged acceptance package only; it does not contact the configured cluster or execute CSI operations.

```bash
GOCACHE=/private/tmp/fixora-gocache go test -c -tags=e2e -o /private/tmp/fixora-graph-storage-e2e.test ./test/e2e_graphstorage
```

Run the one acceptance test against the selected cluster:

**Effect:** Creates a temporary namespace, PVC, sentinel test data, workload graph, source snapshots, temporary grant, and restored shadow claims; the test deletes its resources and fails if run-owned CSI cleanup cannot be confirmed.

```bash
GOCACHE=/private/tmp/fixora-gocache go test -tags=e2e ./test/e2e_graphstorage -run '^TestCombinedGraphPVCSnapshotRestoreAndCleanup$' -count=1 -v
```

Compilation and fake-client tests do not establish that a CSI provisioner supports cross-namespace restore. Only a successful runtime restore on the configured cluster proves that path. Keep combined PVC graph shadow marked **Upcoming** until this tagged acceptance passes against the target CSI and enforcing CNI combination.

## Local policy example

The CLI requires this local policy and a separate typed `SNAPSHOT` confirmation. `--yes` cannot replace that confirmation. The acceptance test writes an equivalent temporary policy for its unique namespace.

```yaml
apiVersion: fixora.io/v1alpha1
kind: GraphStorageShadowPolicy
metadata:
  name: payments-shadow
  namespace: payments
spec:
  maxTotalLogicalCapacity: 8Gi
  pvcs:
    - name: ledger-data
      volumeSnapshotClassName: approved-delete-snapshot-class
      restoreStorageClassName: approved-disposable-storage-class
      maxLogicalCapacity: 4Gi
      allowCrashConsistent: true
```

For example, inspect the exact plan without creating a shadow namespace, grant, or snapshot:

**Effect:** Performs read-only graph and candidate discovery and prints the bounded snapshot plan; it does not create a namespace, grant, or snapshot.

```bash
kubectl fixora coordinate --from deployment/api -n payments \
  --storage-shadow-policy ./storage-shadow-policy.yaml --preview
```

A non-preview PVC graph first performs read-only storage preflight and presents source PVC names/UIDs, requested bytes and policy maximum, all selected classes, crash-consistency behavior, the exact temporary ReferenceGrant scope, and a provider-cost reminder. Type `SNAPSHOT` to authorize storage side effects. Then the ordinary candidate review and apply confirmation still apply.

## Scope and recovery limits

Snapshots are crash-consistent; Fixora does not freeze filesystems, pause writes, or coordinate database transactions. An application may have multiple PVCs that require a quiesce procedure to produce an application-consistent point in time. Fixora does not certify that application-level consistency.

The policy maximum and consent display bound logical capacity. They do not predict provider snapshot billing, changed-block growth, retained cloud backups, or deletion timing. The feature creates only run-unique Kubernetes VolumeSnapshots, one exact temporary ReferenceGrant in the source namespace, restored claims in the per-run namespace, and candidate Pods. It does not change source PVCs, PVs, StorageClasses, or VolumeSnapshotClasses.

PVC-backed graph runs cannot be retained. Fixora deletes the run namespace first, then the temporary ReferenceGrant and source snapshots, and waits for the corresponding VolumeSnapshotContents to disappear. If cleanup cannot be confirmed, no certificate is issued. Inspect the run's cleanup identities and the source namespace's `fixora.io/graph-run`-labeled VolumeSnapshots; check `VolumeSnapshotContent` objects and CSI/provider inventory before manually deleting anything. Do not remove a content object or cloud snapshot that is not proven to belong to the failed run. A stale VolumeSnapshotContent may require CSI administrator cleanup; deletion policy and provider-specific finalizers can affect timing.

No production PVC is mounted into a shadow Pod. Restore is proven by creating a new claim from the snapshot and observing its consuming Pod become scheduled, the restored PV bind to the expected CSI driver, and the tested sentinel workload become Ready. This does not prove application consistency, production traffic behavior, or full production rollout health.
