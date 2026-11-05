package storagefix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
)

type patchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func Verify(ctx context.Context, r Runtime, p Plan) (*Receipt, error) {
	if err := p.Check(); err != nil {
		return nil, err
	}
	current, err := validateSnapshot(ctx, r, p)
	if err != nil {
		return nil, err
	}
	patch, err := exactPatch(p, current)
	if err != nil {
		return nil, err
	}
	admitted, err := r.AdmitPVC(ctx, p.Namespace, p.PVCName, patch)
	if err != nil {
		return nil, fmt.Errorf("PVC server-side dry-run failed: %w", err)
	}
	if admitted == nil || string(admitted.UID) != p.PVCUID || !samePVCExceptServerMetadata(admitted, p.PVC) {
		return nil, errors.New("admission changed the PVC outside the approved storage field")
	}
	return &Receipt{planDigest: p.planDigest, pvcUID: p.PVCUID, pvcRV: p.PVCResourceVersion}, nil
}

func validateSnapshot(ctx context.Context, r Reader, p Plan) (*corev1.PersistentVolumeClaim, error) {
	current, err := r.GetPVC(ctx, p.Namespace, p.PVCName)
	if err != nil {
		return nil, fmt.Errorf("re-read PVC before storage operation: %w", err)
	}
	if current == nil || string(current.UID) != p.PVCUID || current.ResourceVersion != p.PVCResourceVersion {
		return nil, errors.New("PVC changed after repair preparation; prepare again")
	}
	if sourceManagedPVC(current) {
		return nil, errors.New("PVC became source-managed after preparation; review its source of truth")
	}
	consumers, err := inspectConsumerState(ctx, r, current)
	if err != nil {
		return nil, err
	}
	if !sameConsumers(consumers, p.Consumers) {
		return nil, errors.New("PVC consumer set changed after preparation; prepare again")
	}
	sc, err := r.GetStorageClass(ctx, p.StorageClass)
	if err != nil || sc == nil || string(sc.UID) != p.StorageClassUID || sc.ResourceVersion != p.StorageClassRV {
		return nil, errors.New("StorageClass changed after repair preparation; prepare again")
	}
	if p.Action == ActionExpand {
		pv, pvErr := r.GetPV(ctx, p.PVName)
		if pvErr != nil || pv == nil || string(pv.UID) != p.PVUID || pv.ResourceVersion != p.PVResourceVersion || pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.UID != current.UID || pv.Spec.ClaimRef.Namespace != p.Namespace || pv.Spec.ClaimRef.Name != p.PVCName {
			return nil, errors.New("PersistentVolume changed after repair preparation; prepare again")
		}
	}
	return current, nil
}

func exactPatch(p Plan, current *corev1.PersistentVolumeClaim) ([]byte, error) {
	ops := []patchOperation{{Op: "test", Path: "/metadata/uid", Value: p.PVCUID}, {Op: "test", Path: "/metadata/resourceVersion", Value: p.PVCResourceVersion}}
	switch p.Action {
	case ActionSetStorageClass:
		if current.Spec.StorageClassName == nil {
			ops = append(ops, patchOperation{Op: "add", Path: "/spec/storageClassName", Value: p.StorageClass})
		} else {
			ops = append(ops, patchOperation{Op: "test", Path: "/spec/storageClassName", Value: *current.Spec.StorageClassName}, patchOperation{Op: "replace", Path: "/spec/storageClassName", Value: p.StorageClass})
		}
	case ActionExpand:
		old := current.Spec.Resources.Requests[corev1.ResourceStorage]
		ops = append(ops, patchOperation{Op: "test", Path: "/spec/resources/requests/storage", Value: old.String()}, patchOperation{Op: "replace", Path: "/spec/resources/requests/storage", Value: p.RequestedStorage})
	default:
		return nil, errors.New("unsupported PVC repair action")
	}
	return json.Marshal(ops)
}

func samePVCExceptServerMetadata(got, expected *corev1.PersistentVolumeClaim) bool {
	if got == nil || expected == nil {
		return false
	}
	a := got.DeepCopy()
	b := expected.DeepCopy()
	a.ResourceVersion, b.ResourceVersion = "", ""
	a.Generation, b.Generation = 0, 0
	a.ManagedFields, b.ManagedFields = nil, nil
	return reflect.DeepEqual(a, b)
}
