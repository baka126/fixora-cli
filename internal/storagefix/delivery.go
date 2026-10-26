package storagefix

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func Apply(ctx context.Context, r Runtime, p Plan, receipt *Receipt) (*Applied, error) {
	if err := p.Check(); err != nil {
		return nil, err
	}
	if receipt == nil {
		return nil, errors.New("fresh unused PVC verification receipt required")
	}
	current, err := validateSnapshot(ctx, r, p)
	if err != nil {
		return nil, err
	}
	patch, err := exactPatch(p, current)
	if err != nil {
		return nil, err
	}
	receipt.mu.Lock()
	defer receipt.mu.Unlock()
	if receipt.consumed || receipt.planDigest != p.planDigest || receipt.pvcUID != p.PVCUID || receipt.pvcRV != p.PVCResourceVersion {
		return nil, errors.New("fresh unused PVC verification receipt required")
	}
	receipt.consumed = true
	updated, err := r.PatchPVC(ctx, p.Namespace, p.PVCName, patch)
	if err != nil {
		return nil, fmt.Errorf("guarded PVC patch failed: %w", err)
	}
	if updated == nil || string(updated.UID) != p.PVCUID || updated.ResourceVersion == "" || updated.ResourceVersion == p.PVCResourceVersion || !samePVCExceptServerMetadata(updated, p.PVC) {
		return nil, errors.New("API returned a PVC that differs from the exact approved storage change")
	}
	return &Applied{Plan: p, AppliedResourceVersion: updated.ResourceVersion, AppliedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
}

func Observe(ctx context.Context, r Reader, applied *Applied) (Outcome, error) {
	if applied == nil || applied.PVCUID == "" || applied.AppliedResourceVersion == "" {
		return Outcome{}, errors.New("applied PVC identity required")
	}
	pvc, err := r.GetPVC(ctx, applied.Namespace, applied.PVCName)
	if err != nil {
		return Outcome{}, err
	}
	if pvc == nil || string(pvc.UID) != applied.PVCUID {
		return Outcome{Status: "replaced", Message: "PVC was replaced; Fixora cannot verify this repair."}, nil
	}
	consumers, consumerErr := inspectConsumerState(ctx, r, pvc)
	if consumerErr != nil || !sameConsumers(consumers, applied.Consumers) {
		return Outcome{Status: "unknown", Message: "PVC state was read, but its original consumer ownership could not be confirmed."}, nil
	}
	class, classErr := r.GetStorageClass(ctx, applied.StorageClass)
	if classErr != nil || class == nil || string(class.UID) != applied.StorageClassUID {
		return Outcome{Status: "unknown", Message: "PVC state was read, but the approved StorageClass identity could not be verified."}, nil
	}
	switch applied.Action {
	case ActionSetStorageClass:
		if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != applied.StorageClass {
			return Outcome{Status: "conflict", Message: "PVC StorageClass no longer matches the approved value."}, nil
		}
		if pvc.Status.Phase != corev1.ClaimBound || pvc.Spec.VolumeName == "" {
			return Outcome{Status: "pending", Message: "Approved StorageClass is set; PVC provisioning or binding is still pending."}, nil
		}
		pv, getErr := r.GetPV(ctx, pvc.Spec.VolumeName)
		if getErr != nil || pv == nil || pv.Status.Phase != corev1.VolumeBound || pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.UID != pvc.UID || pv.Spec.ClaimRef.Namespace != pvc.Namespace || pv.Spec.ClaimRef.Name != pvc.Name || pv.Spec.StorageClassName != applied.StorageClass {
			return Outcome{Status: "pending", Message: "PVC is Bound but its PV claim identity is not yet verified."}, nil
		}
	case ActionExpand:
		pv, getErr := r.GetPV(ctx, applied.PVName)
		if getErr != nil || pv == nil || string(pv.UID) != applied.PVUID || pv.Status.Phase != corev1.VolumeBound || pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.UID != pvc.UID || pv.Spec.ClaimRef.Namespace != pvc.Namespace || pv.Spec.ClaimRef.Name != pvc.Name {
			return Outcome{Status: "unknown", Message: "PVC capacity was read, but the original PV claim identity could not be verified."}, nil
		}
		if !requestedStorageMatches(pvc, applied.RequestedStorage) {
			return Outcome{Status: "conflict", Message: "PVC storage request no longer matches the approved value."}, nil
		}
		capacity, ok := pvc.Status.Capacity[corev1.ResourceStorage]
		if !ok || !requestedStorageMatchesCapacity(capacity, applied.RequestedStorage) {
			return Outcome{Status: "pending", Message: "Storage expansion was requested; the provisioner has not reported the approved capacity yet."}, nil
		}
		for _, condition := range pvc.Status.Conditions {
			if condition.Status == corev1.ConditionTrue && (condition.Type == corev1.PersistentVolumeClaimFileSystemResizePending || condition.Type == corev1.PersistentVolumeClaimResizing) {
				return Outcome{Status: "pending", Message: "Volume capacity increased; filesystem resize is still pending."}, nil
			}
		}
	default:
		return Outcome{}, errors.New("unsupported observed PVC action")
	}
	if len(applied.Consumers) > 0 {
		pods, listErr := r.ListPods(ctx, applied.Namespace)
		if listErr != nil {
			return Outcome{Status: "unknown", Message: "PVC storage state is observed, but consumer readiness could not be checked."}, nil
		}
		for _, expected := range applied.Consumers {
			found := false
			for _, pod := range pods {
				if pod.Name == expected.Name && pod.UID == expected.UID {
					found = true
					if !podReady(&pod) {
						return Outcome{Status: "pending", Message: "PVC storage state is observed; the original consuming Pod is not Ready."}, nil
					}
				}
			}
			if !found {
				return Outcome{Status: "unknown", Message: "PVC storage state is observed, but the original consumer Pod was replaced or is absent."}, nil
			}
		}
		return Outcome{Status: "observed", Message: "PVC storage change is observed and its original consumer Pod is Ready; application behavior was not tested."}, nil
	}
	if applied.Action == ActionExpand {
		return Outcome{Status: "observed", Message: "Requested volume capacity is observed; no application consumer was present to verify."}, nil
	}
	return Outcome{Status: "observed", Message: "PVC is Bound to the approved StorageClass; application behavior was not tested."}, nil
}

func requestedStorageMatches(pvc *corev1.PersistentVolumeClaim, value string) bool {
	want, err := resource.ParseQuantity(value)
	if err != nil || pvc == nil {
		return false
	}
	got, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	return ok && got.Cmp(want) == 0
}

func requestedStorageMatchesCapacity(got resource.Quantity, want string) bool {
	target, err := resource.ParseQuantity(want)
	return err == nil && got.Cmp(target) >= 0
}

func podReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
