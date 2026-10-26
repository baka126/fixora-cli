package storagefix

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type candidateReader struct {
	pvc          *corev1.PersistentVolumeClaim
	sc           *storagev1.StorageClass
	pv           *corev1.PersistentVolume
	pods         []corev1.Pod
	statefulSets []appsv1.StatefulSet
	owners       map[string]kube.WorkloadOwner
}

func (r *candidateReader) GetPVC(context.Context, string, string) (*corev1.PersistentVolumeClaim, error) {
	return r.pvc.DeepCopy(), nil
}
func (r *candidateReader) GetStorageClass(context.Context, string) (*storagev1.StorageClass, error) {
	return r.sc.DeepCopy(), nil
}
func (r *candidateReader) GetPV(context.Context, string) (*corev1.PersistentVolume, error) {
	return r.pv.DeepCopy(), nil
}
func (r *candidateReader) ListPods(context.Context, string) ([]corev1.Pod, error) {
	return append([]corev1.Pod(nil), r.pods...), nil
}
func (r *candidateReader) ListStatefulSets(context.Context, string) ([]appsv1.StatefulSet, error) {
	return append([]appsv1.StatefulSet(nil), r.statefulSets...), nil
}
func (r *candidateReader) ResolveOwner(_ context.Context, pod corev1.Pod) (kube.WorkloadOwner, error) {
	if owner, ok := r.owners[string(pod.UID)]; ok {
		return owner, nil
	}
	if len(pod.OwnerReferences) > 0 {
		return kube.WorkloadOwner{}, errors.New("unresolved test owner")
	}
	return kube.WorkloadOwner{Kind: "Pod", Name: pod.Name, UID: string(pod.UID)}, nil
}

func TestPrepareSetsOnlyPolicyApprovedClassOnUnboundClaim(t *testing.T) {
	a := loadStoragePolicy(t, `apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec: {pvc: {name: data}, action: setStorageClass, storageClassName: gp3}
`)
	reader := baseCandidateReader()
	reader.pvc.Status.Phase = corev1.ClaimPending
	reader.pvc.Spec.StorageClassName = nil
	reader.pvc.Spec.VolumeName = ""
	p, err := Prepare(context.Background(), reader, a, analyzer.Finding{ResourceKind: "PersistentVolumeClaim", ResourceName: "data", Namespace: "fixora-demo", Status: "MissingStorageClass", ResourceUID: "pvc-uid"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionSetStorageClass || p.StorageClass != "gp3" || p.PVCUID != "pvc-uid" || p.PVCResourceVersion != "7" {
		t.Fatalf("unexpected plan: %+v", p)
	}
	if p.PVC.Spec.StorageClassName == nil || *p.PVC.Spec.StorageClassName != "gp3" || len(p.ChangedFields) != 1 || p.ChangedFields[0] != "/spec/storageClassName" {
		t.Fatalf("candidate changed unauthorized PVC fields: %+v", p)
	}
}

func TestPrepareExpandsOnlyWithinPolicyAndClassCapabilities(t *testing.T) {
	a := loadStoragePolicy(t, `apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec: {pvc: {name: data}, action: expand, requestedStorage: 8Gi, maxStorage: 10Gi}
`)
	reader := baseCandidateReader()
	reader.pvc.Spec.StorageClassName = ptrString("gp3")
	reader.pvc.Spec.VolumeName = "pv-data"
	reader.pvc.Status.Phase = corev1.ClaimBound
	reader.pvc.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("4Gi")
	reader.pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("4Gi")}
	reader.pv = &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-data", UID: "pv-uid", ResourceVersion: "11"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: "gp3", ClaimRef: &corev1.ObjectReference{Namespace: "fixora-demo", Name: "data", UID: types.UID("pvc-uid")}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}}
	reader.sc.AllowVolumeExpansion = ptrBool(true)
	p, err := Prepare(context.Background(), reader, a, analyzer.Finding{ResourceKind: "PersistentVolumeClaim", ResourceName: "data", Namespace: "fixora-demo", Status: "VolumeResizePending", ResourceUID: "pvc-uid"})
	if err != nil {
		t.Fatal(err)
	}
	requested := p.PVC.Spec.Resources.Requests[corev1.ResourceStorage]
	if p.Action != ActionExpand || p.RequestedStorage != "8Gi" || requested.Cmp(resource.MustParse("8Gi")) != 0 {
		t.Fatalf("unexpected expansion plan: %+v", p)
	}
}

func TestPrepareRejectsAmbiguousAndUnsafeStorageChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*candidateReader)
		policy string
	}{
		{name: "PVC identity mismatch", mutate: func(r *candidateReader) { r.pvc.Name = "other" }, policy: "set"},
		{name: "bound claim class assignment", mutate: func(r *candidateReader) { r.pvc.Spec.VolumeName = "pv-data" }, policy: "set"},
		{name: "explicit empty class cannot be reassigned", mutate: func(r *candidateReader) { r.pvc.Spec.StorageClassName = ptrString("") }, policy: "set"},
		{name: "source managed claim", mutate: func(r *candidateReader) {
			r.pvc.Annotations = map[string]string{"meta.helm.sh/release-name": "release"}
		}, policy: "set"},
		{name: "class has unverified topology constraints", mutate: func(r *candidateReader) {
			r.sc.AllowedTopologies = []corev1.TopologySelectorTerm{{MatchLabelExpressions: []corev1.TopologySelectorLabelRequirement{{Key: "topology.kubernetes.io/zone", Values: []string{"us-east-1a"}}}}}
		}, policy: "set"},
		{name: "expansion unsupported", mutate: func(r *candidateReader) { r.sc.AllowVolumeExpansion = ptrBool(false) }, policy: "expand"},
		{name: "PV claim UID mismatch", mutate: func(r *candidateReader) { r.pv.Spec.ClaimRef.UID = "replacement" }, policy: "expand"},
		{name: "ambiguous consumers", mutate: func(r *candidateReader) { r.pods = []corev1.Pod{consumerPod("a"), consumerPod("b")} }, policy: "expand"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := baseCandidateReader()
			f := analyzer.Finding{ResourceKind: "PersistentVolumeClaim", ResourceName: "data", Namespace: "fixora-demo", Status: "MissingStorageClass", ResourceUID: "pvc-uid"}
			policy := `apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec: {pvc: {name: data}, action: setStorageClass, storageClassName: gp3}
`
			if tt.policy == "expand" {
				policy = `apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec: {pvc: {name: data}, action: expand, requestedStorage: 8Gi, maxStorage: 10Gi}
`
				r.pvc.Spec.StorageClassName = ptrString("gp3")
				r.pvc.Spec.VolumeName = "pv-data"
				r.pvc.Status.Phase = corev1.ClaimBound
				r.pvc.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("4Gi")
				r.pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("4Gi")}
				r.pv = &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-data", UID: "pv-uid", ResourceVersion: "11"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: "gp3", ClaimRef: &corev1.ObjectReference{Namespace: "fixora-demo", Name: "data", UID: "pvc-uid"}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}}
				r.sc.AllowVolumeExpansion = ptrBool(true)
				f.Status = "VolumeResizePending"
			}
			tt.mutate(r)
			if _, err := Prepare(context.Background(), r, loadStoragePolicy(t, policy), f); err == nil {
				t.Fatal("Prepare accepted unsafe or ambiguous mutation")
			}
		})
	}
}

func TestPrepareRejectsStatefulSetClaimTemplateEvenWithoutConsumerPod(t *testing.T) {
	r := baseCandidateReader()
	r.pvc.Name = "data-db-0"
	r.pvc.Status.Phase = corev1.ClaimPending
	r.pvc.Spec.StorageClassName = nil
	r.pvc.Spec.VolumeName = ""
	r.statefulSets = []appsv1.StatefulSet{{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "fixora-demo", UID: "sts-uid"}, Spec: appsv1.StatefulSetSpec{VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data"}}}}}}
	a := loadStoragePolicy(t, `apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec: {pvc: {name: data-db-0}, action: setStorageClass, storageClassName: gp3}
`)
	f := analyzer.Finding{ResourceKind: "PersistentVolumeClaim", ResourceName: "data-db-0", Namespace: "fixora-demo", Status: "Pending", ResourceUID: "pvc-uid"}
	if _, err := Prepare(context.Background(), r, a, f); err == nil {
		t.Fatal("Prepare accepted StatefulSet-generated claim without a consumer Pod")
	}
}

func TestPrepareRejectsControllerManagedConsumerWithoutPodMarkers(t *testing.T) {
	r := baseCandidateReader()
	pod := consumerPod("consumer")
	r.pods = []corev1.Pod{pod}
	r.owners = map[string]kube.WorkloadOwner{string(pod.UID): {Kind: "Deployment", Name: "db", UID: "deployment-uid", Object: map[string]any{"metadata": map[string]any{"annotations": map[string]any{"argocd.argoproj.io/tracking-id": "app"}}}}}
	a := loadStoragePolicy(t, `apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec: {pvc: {name: data}, action: setStorageClass, storageClassName: gp3}
`)
	f := analyzer.Finding{ResourceKind: "PersistentVolumeClaim", ResourceName: "data", Namespace: "fixora-demo", Status: "MissingStorageClass", ResourceUID: "pvc-uid"}
	if _, err := Prepare(context.Background(), r, a, f); err == nil {
		t.Fatal("controller-managed PVC consumer was allowed without Pod metadata markers")
	}
}

func baseCandidateReader() *candidateReader {
	return &candidateReader{
		pvc: &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "fixora-demo", UID: "pvc-uid", ResourceVersion: "7"}, Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("4Gi")}}}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}},
		sc:  &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "gp3", UID: "sc-uid", ResourceVersion: "9"}, Provisioner: "ebs.csi.aws.com"},
	}
}

func loadStoragePolicy(t *testing.T, body string) *Authority {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func ptrString(s string) *string { return &s }
func ptrBool(b bool) *bool       { return &b }

func consumerPod(name string) corev1.Pod {
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fixora-demo", UID: types.UID(name)}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}}}
}
