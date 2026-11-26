---
layout: default
title: PVC repairs
description: Diagnose PVC issues and prepare policy-bound class assignment or volume expansion.
permalink: /guides/pvc-repairs/
section: guides
---

# PVC repairs

Fixora diagnoses a PVC through its consumer Pods, PV, StorageClass, topology clues, and related Events. Unreadable inventory is reported as unavailable, not absent. A Bound claim or larger capacity does not establish that the application can read or write its data.

PVC mutations require a strict local `PVCRepairPolicy`. AI explanations can describe evidence and uncertainty but never authorize storage mutations. Preview performs no writes or server-side dry-run. Cluster delivery uses a UID/resourceVersion-guarded JSON Patch and server-side dry-run before confirmation. Fixora never mounts a production PVC in a shadow Pod.

The current Kubernetes identity needs read access to PVCs, Pods, StatefulSets, PVs, and StorageClasses, plus `patch` on PVCs in the target namespace. Keep that write permission separate from Fixora's read-only role and grant it only to operators who use this explicit policy flow.

## Assign an approved StorageClass

Use this only for an unbound Pending claim with no class and no bound PV. Choose the StorageClass from your platform policy; Fixora does not infer or create one.

```yaml
apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata:
  name: data-class
  namespace: prod
spec:
  pvc:
    name: data
  action: setStorageClass
  storageClassName: gp3
```

Review the proposal, then explicitly request cluster delivery.

Effect: the first command previews without writing; the second applies the policy-approved StorageClass assignment to the named PVC.

```bash
kubectl fixora fix pvc/data -n prod --no-ai --pvc-policy ./pvc-repair.yaml --preview
kubectl fixora fix pvc/data -n prod --no-ai --pvc-policy ./pvc-repair.yaml --delivery=cluster --yes
```

After delivery, Fixora reports whether the claim is still provisioning or is Bound to a PV whose claim UID, name, namespace, and class match. Pod readiness is checked only for the original consumer Pod observed during preparation. The check does not test application data or filesystem semantics.

## Expand a bound volume

Expansion is allowed only when the claim is Bound, its PV claimRef matches the exact PVC UID, and its existing StorageClass advertises `allowVolumeExpansion: true`. The requested capacity must exceed both the current request and observed capacity, and stay within the policy maximum.

```yaml
apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata:
  name: data-expansion
  namespace: prod
spec:
  pvc:
    name: data
  action: expand
  requestedStorage: 8Gi
  maxStorage: 10Gi
```

Effect: the first command previews without writing; the second applies the policy-approved capacity expansion. Expansion cannot be rolled back automatically.

```bash
kubectl fixora fix pvc/data -n prod --no-ai --pvc-policy ./pvc-repair.yaml --preview
kubectl fixora fix pvc/data -n prod --no-ai --pvc-policy ./pvc-repair.yaml --delivery=cluster --yes
```

Expansion is non-rollbackable. Fixora warns before delivery and never offers an automatic inverse. It reports provisioning and filesystem-resize conditions separately; a pending resize requires operator follow-up. If a consumer Pod exists, Fixora checks that the same Pod UID is Ready, but does not claim application-level correctness.

## Cases that remain review-only

Lost claims, PV release/reclaim or deletion, rebinding, reclaim-policy changes, shrinking, migration, recovery, StorageClass/provisioner creation, ambiguous consumers or topology, StatefulSet claim templates, GitOps/Helm ownership, and unsupported storage backends are never changed by this flow. Fixora does not treat an AI-suggested class, size, or recovery action as operator intent.

## Disposable cluster test

`hack/e2e-pvc.sh` exercises no-policy preview, exact class assignment, and bounded expansion in a newly-created namespace. It does not create or delete a cluster and never targets `fixora-demo`; it does provision temporary EBS storage and creates a unique `gp3` StorageClass with `Delete` reclaim policy. To test the allowed nil-to-class change, it temporarily disables any existing default StorageClass annotation and restores it on exit. A disposable pod mounts the test claim so Kubernetes can finish filesystem expansion. The script deletes its namespace, waits for the resulting PVs to disappear, and deletes that StorageClass.

Effect: the test temporarily toggles default StorageClass annotations, creates a namespace, StorageClass, two PVCs and a disposable mount pod; it deletes those test resources and restores the annotations when it exits.

```bash
FIXORA_PVC_E2E_CONFIRM=yes \
FIXORA_PVC_E2E_CONTEXT=my-test-context \
bash hack/e2e-pvc.sh
```

The cluster must have the EBS CSI controller and node driver installed and ready. The test builds `./bin/kubectl-fixora` if that binary is missing. It never deletes the cluster.
