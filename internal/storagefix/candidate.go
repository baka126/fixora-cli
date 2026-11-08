package storagefix

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func Prepare(ctx context.Context, r Reader, authority *Authority, finding analyzer.Finding) (Plan, error) {
	if err := authority.check(); err != nil {
		return Plan{}, err
	}
	intent := authority.intent
	if finding.ResourceKind != "PersistentVolumeClaim" || finding.Namespace != intent.Metadata.Namespace || finding.ResourceName != intent.Spec.PVC.Name {
		return Plan{}, errors.New("finding must identify the exact policy PVC")
	}
	pvc, err := r.GetPVC(ctx, intent.Metadata.Namespace, intent.Spec.PVC.Name)
	if err != nil {
		return Plan{}, fmt.Errorf("read policy PVC: %w", err)
	}
	if pvc == nil || pvc.Namespace != intent.Metadata.Namespace || pvc.Name != intent.Spec.PVC.Name || pvc.UID == "" || pvc.ResourceVersion == "" || string(pvc.UID) != finding.ResourceUID {
		return Plan{}, errors.New("live PVC identity does not match exact finding and policy")
	}
	if sourceManagedPVC(pvc) || finding.GitOps.ManagedBy != "" || finding.GitOps.TargetAdvice != "" || finding.GitOps.HelmRelease != "" || finding.GitOps.HelmChart != "" || finding.GitOps.FluxHint != "" || finding.GitOps.ArgoHint != "" {
		return Plan{}, errors.New("source-managed PVC requires reviewed source delivery")
	}
	consumers, err := inspectConsumerState(ctx, r, pvc)
	if err != nil {
		return Plan{}, err
	}

	original := pvc.DeepCopy()
	candidate := pvc.DeepCopy()
	plan := Plan{Namespace: pvc.Namespace, PVCName: pvc.Name, PVCUID: string(pvc.UID), PVCResourceVersion: pvc.ResourceVersion, Action: intent.Spec.Action, Consumers: consumers, original: original, PVC: candidate, policy: authority, policyDigest: authority.digest}
	switch intent.Spec.Action {
	case ActionSetStorageClass:
		// An omitted class can be filled while the claim is Pending. An explicit
		// empty string opts out of StorageClasses and is immutable after creation.
		if pvc.Status.Phase != corev1.ClaimPending || pvc.Spec.StorageClassName != nil || pvc.Spec.VolumeName != "" {
			return Plan{}, errors.New("StorageClass can only be assigned to an unbound PVC with no existing class")
		}
		sc, getErr := r.GetStorageClass(ctx, intent.Spec.StorageClassName)
		if getErr != nil {
			return Plan{}, fmt.Errorf("read policy StorageClass: %w", getErr)
		}
		if sc == nil || sc.Name != intent.Spec.StorageClassName || sc.UID == "" || sc.ResourceVersion == "" || strings.TrimSpace(sc.Provisioner) == "" {
			return Plan{}, errors.New("approved StorageClass is missing or has incomplete identity")
		}
		if len(sc.AllowedTopologies) > 0 {
			return Plan{}, errors.New("StorageClass topology constraints require reviewed placement evidence")
		}
		plan.StorageClass, plan.StorageClassUID, plan.StorageClassRV = sc.Name, string(sc.UID), sc.ResourceVersion
		candidate.Spec.StorageClassName = &plan.StorageClass
		plan.ChangedFields = []string{"/spec/storageClassName"}
	case ActionExpand:
		if pvc.Status.Phase != corev1.ClaimBound || pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName == "" || pvc.Spec.VolumeName == "" {
			return Plan{}, errors.New("expansion requires a Bound PVC with an exact StorageClass and PV")
		}
		sc, getErr := r.GetStorageClass(ctx, *pvc.Spec.StorageClassName)
		if getErr != nil {
			return Plan{}, fmt.Errorf("read PVC StorageClass: %w", getErr)
		}
		if sc == nil || sc.Name != *pvc.Spec.StorageClassName || sc.UID == "" || sc.ResourceVersion == "" || sc.AllowVolumeExpansion == nil || !*sc.AllowVolumeExpansion {
			return Plan{}, errors.New("PVC StorageClass does not prove volume expansion support")
		}
		pv, getErr := r.GetPV(ctx, pvc.Spec.VolumeName)
		if getErr != nil {
			return Plan{}, fmt.Errorf("read bound PersistentVolume: %w", getErr)
		}
		if pv == nil || pv.Name != pvc.Spec.VolumeName || pv.UID == "" || pv.ResourceVersion == "" || pv.Status.Phase != corev1.VolumeBound || pv.Spec.StorageClassName != sc.Name || pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.Namespace != pvc.Namespace || pv.Spec.ClaimRef.Name != pvc.Name || pv.Spec.ClaimRef.UID != pvc.UID {
			return Plan{}, errors.New("bound PV identity does not match the exact PVC and StorageClass")
		}
		requested, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		if !ok {
			return Plan{}, errors.New("PVC storage request is unavailable")
		}
		capacity, ok := pvc.Status.Capacity[corev1.ResourceStorage]
		if !ok {
			return Plan{}, errors.New("PVC observed capacity is unavailable")
		}
		target, targetErr := resource.ParseQuantity(intent.Spec.RequestedStorage)
		maximum, maxErr := resource.ParseQuantity(intent.Spec.MaximumStorage)
		if targetErr != nil || maxErr != nil || target.Cmp(requested) <= 0 || target.Cmp(capacity) <= 0 || target.Cmp(maximum) > 0 {
			return Plan{}, errors.New("approved expansion must exceed request and observed capacity and remain within policy maxStorage")
		}
		if candidate.Spec.Resources.Requests == nil {
			return Plan{}, errors.New("PVC resource requests are unavailable")
		}
		candidate.Spec.Resources.Requests[corev1.ResourceStorage] = target
		plan.StorageClass, plan.StorageClassUID, plan.StorageClassRV = sc.Name, string(sc.UID), sc.ResourceVersion
		plan.PVName, plan.PVUID, plan.PVResourceVersion = pv.Name, string(pv.UID), pv.ResourceVersion
		plan.RequestedStorage = target.String()
		plan.MaximumStorage = maximum.String()
		plan.ChangedFields = []string{"/spec/resources/requests/storage"}
	default:
		return Plan{}, errors.New("unsupported PVC repair action")
	}
	plan.planDigest, err = candidateDigest(plan)
	if err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func inspectConsumerState(ctx context.Context, r Reader, pvc *corev1.PersistentVolumeClaim) ([]corev1.ObjectReference, error) {
	for _, owner := range pvc.OwnerReferences {
		if owner.Kind == "StatefulSet" {
			return nil, errors.New("StatefulSet-owned PVC requires review")
		}
	}
	statefulSets, err := r.ListStatefulSets(ctx, pvc.Namespace)
	if err != nil {
		return nil, fmt.Errorf("list StatefulSets to exclude claim-template storage: %w", err)
	}
	if statefulSetClaimTemplate(pvc.Name, pvc.Namespace, statefulSets) {
		return nil, errors.New("StatefulSet claim-template storage requires review")
	}
	pods, err := r.ListPods(ctx, pvc.Namespace)
	if err != nil {
		return nil, fmt.Errorf("list PVC consumers: %w", err)
	}
	consumers := []corev1.ObjectReference{}
	for _, pod := range pods {
		if !podReferencesPVC(pod, pvc.Name) {
			continue
		}
		if pod.Namespace != pvc.Namespace || pod.Name == "" || pod.UID == "" {
			return nil, errors.New("PVC consumer identity is incomplete")
		}
		owner, err := r.ResolveOwner(ctx, pod)
		if err != nil {
			return nil, fmt.Errorf("resolve PVC consumer %s owner: %w", pod.Name, err)
		}
		if owner.Kind == "StatefulSet" {
			return nil, errors.New("StatefulSet claim-template storage requires review")
		}
		if sourceManagedPod(&pod) || sourceManagedOwner(owner.Object) {
			return nil, errors.New("source-managed PVC consumer requires reviewed source delivery")
		}
		consumers = append(consumers, corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID})
	}
	if len(consumers) > 1 {
		return nil, errors.New("PVC has multiple consumers; storage repair requires review")
	}
	return consumers, nil
}

func sourceManagedOwner(owner map[string]any) bool {
	if owner == nil {
		return false
	}
	metadata, _ := owner["metadata"].(map[string]any)
	labels, _ := metadata["labels"].(map[string]any)
	annotations, _ := metadata["annotations"].(map[string]any)
	stringsFrom := func(values map[string]any) map[string]string {
		out := make(map[string]string, len(values))
		for key, value := range values {
			out[key] = fmt.Sprint(value)
		}
		return out
	}
	return sourceManagedMetadata(stringsFrom(labels), stringsFrom(annotations))
}

func sameConsumers(a, b []corev1.ObjectReference) bool {
	key := func(items []corev1.ObjectReference) []string {
		out := make([]string, 0, len(items))
		for _, item := range items {
			out = append(out, item.APIVersion+"/"+item.Kind+"/"+item.Namespace+"/"+item.Name+"/"+string(item.UID))
		}
		sort.Strings(out)
		return out
	}
	left, right := key(a), key(b)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func statefulSetClaimTemplate(pvcName, namespace string, statefulSets []appsv1.StatefulSet) bool {
	for _, statefulSet := range statefulSets {
		if statefulSet.Namespace != namespace || statefulSet.Name == "" {
			continue
		}
		for _, claimTemplate := range statefulSet.Spec.VolumeClaimTemplates {
			if claimTemplate.Name == "" {
				continue
			}
			prefix := claimTemplate.Name + "-" + statefulSet.Name + "-"
			if !strings.HasPrefix(pvcName, prefix) {
				continue
			}
			ordinal := strings.TrimPrefix(pvcName, prefix)
			if value, err := strconv.Atoi(ordinal); err == nil && value >= 0 {
				return true
			}
		}
	}
	return false
}

func (p Plan) Check() error {
	if err := p.policy.check(); err != nil {
		return err
	}
	if p.policy.digest != p.policyDigest {
		return errors.New("PVC policy authority changed")
	}
	d, err := candidateDigest(p)
	if err != nil || d != p.planDigest {
		return errors.New("PVC repair plan changed after preparation")
	}
	return nil
}

func candidateDigest(p Plan) ([32]byte, error) {
	b, err := json.Marshal(struct {
		Namespace, Name, UID, ResourceVersion, Action, StorageClass, StorageClassUID, StorageClassRV, PVName, PVUID, PVResourceVersion, RequestedStorage, MaximumStorage string
		Candidate                                                                                                                                                        *corev1.PersistentVolumeClaim
		Original                                                                                                                                                         *corev1.PersistentVolumeClaim
		Consumers                                                                                                                                                        []corev1.ObjectReference
		ChangedFields                                                                                                                                                    []string
		PolicyDigest                                                                                                                                                     [32]byte
	}{p.Namespace, p.PVCName, p.PVCUID, p.PVCResourceVersion, p.Action, p.StorageClass, p.StorageClassUID, p.StorageClassRV, p.PVName, p.PVUID, p.PVResourceVersion, p.RequestedStorage, p.MaximumStorage, p.PVC, p.original, p.Consumers, p.ChangedFields, p.policyDigest})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(b), nil
}

func sourceManagedPVC(pvc *corev1.PersistentVolumeClaim) bool {
	return sourceManagedMetadata(pvc.Labels, pvc.Annotations)
}

func sourceManagedPod(pod *corev1.Pod) bool {
	return sourceManagedMetadata(pod.Labels, pod.Annotations)
}

func sourceManagedMetadata(labels, annotations map[string]string) bool {
	for key, value := range mapMerge(labels, annotations) {
		lower := strings.ToLower(key)
		if value != "" && (lower == "app.kubernetes.io/managed-by" || lower == "helm.sh/chart" || strings.Contains(lower, "helm") || strings.Contains(lower, "argocd.argoproj.io") || strings.Contains(lower, "toolkit.fluxcd.io")) {
			return true
		}
	}
	return false
}

func mapMerge(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func podReferencesPVC(pod corev1.Pod, claim string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == claim {
			return true
		}
	}
	return false
}
