package storagefix

import (
	"context"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type deliveryReader struct {
	*candidateReader
	admitted *corev1.PersistentVolumeClaim
	updated  *corev1.PersistentVolumeClaim
	patches  int
	patch    []byte
}

func (r *deliveryReader) AdmitPVC(_ context.Context, _, _ string, patch []byte) (*corev1.PersistentVolumeClaim, error) {
	r.patch = append([]byte(nil), patch...)
	return r.admitted.DeepCopy(), nil
}
func (r *deliveryReader) PatchPVC(_ context.Context, _, _ string, patch []byte) (*corev1.PersistentVolumeClaim, error) {
	r.patches++
	r.patch = append([]byte(nil), patch...)
	return r.updated.DeepCopy(), nil
}

func TestVerifyCreatesReceiptOnlyForExactAdmittedStorageChange(t *testing.T) {
	p, r := preparedStorageClassPlan(t)
	r.admitted = p.PVC.DeepCopy()
	receipt, err := Verify(context.Background(), r, p)
	if err != nil || receipt == nil {
		t.Fatalf("Verify receipt=%v err=%v", receipt, err)
	}
	if len(r.patch) == 0 {
		t.Fatal("Verify skipped server-side dry-run of the exact patch")
	}
}

func TestVerifyRejectsAdmissionMutationOutsideAuthorizedField(t *testing.T) {
	p, r := preparedStorageClassPlan(t)
	r.admitted = p.PVC.DeepCopy()
	r.admitted.Annotations = map[string]string{"admission.example/changed": "true"}
	if _, err := Verify(context.Background(), r, p); err == nil {
		t.Fatal("Verify accepted admission mutation outside the authorized storage field")
	}
}

func TestVerifyRejectsConsumerGraphDrift(t *testing.T) {
	p, r := preparedStorageClassPlan(t)
	r.admitted = p.PVC.DeepCopy()
	r.pods = []corev1.Pod{consumerPod("new-consumer")}
	if _, err := Verify(context.Background(), r, p); err == nil || r.patches != 0 || len(r.patch) != 0 {
		t.Fatalf("new consumer escaped preflight: patches=%d err=%v", r.patches, err)
	}
}

func TestApplyRechecksConsumerGraphAfterVerification(t *testing.T) {
	p, r := preparedStorageClassPlan(t)
	r.admitted = p.PVC.DeepCopy()
	receipt, err := Verify(context.Background(), r, p)
	if err != nil {
		t.Fatal(err)
	}
	r.pods = []corev1.Pod{consumerPod("new-consumer")}
	r.updated = p.PVC.DeepCopy()
	r.updated.ResourceVersion = "8"
	if _, err = Apply(context.Background(), r, p, receipt); err == nil || r.patches != 0 {
		t.Fatalf("consumer drift escaped Apply precondition: patches=%d err=%v", r.patches, err)
	}
}

func TestVerifyRejectsNewStatefulSetClaimTemplate(t *testing.T) {
	r := baseCandidateReader()
	r.pvc.Name = "data-db-0"
	a := loadStoragePolicy(t, `apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec: {pvc: {name: data-db-0}, action: setStorageClass, storageClassName: gp3}
`)
	f := analyzer.Finding{ResourceKind: "PersistentVolumeClaim", ResourceName: r.pvc.Name, Namespace: "fixora-demo", Status: "MissingStorageClass", ResourceUID: "pvc-uid"}
	p, err := Prepare(context.Background(), r, a, f)
	if err != nil {
		t.Fatal(err)
	}
	r.statefulSets = []appsv1.StatefulSet{{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "fixora-demo", UID: "sts-uid"}, Spec: appsv1.StatefulSetSpec{VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data"}}}}}}
	d := &deliveryReader{candidateReader: r, admitted: p.PVC.DeepCopy()}
	if _, err = Verify(context.Background(), d, p); err == nil || len(d.patch) != 0 {
		t.Fatalf("new StatefulSet claim-template ownership escaped preflight: patch=%s err=%v", d.patch, err)
	}
}

func TestApplyConsumesReceiptAndUsesIdentityGuardedPatch(t *testing.T) {
	p, r := preparedStorageClassPlan(t)
	r.admitted = p.PVC.DeepCopy()
	receipt, err := Verify(context.Background(), r, p)
	if err != nil {
		t.Fatal(err)
	}
	r.updated = p.PVC.DeepCopy()
	r.updated.ResourceVersion = "8"
	applied, err := Apply(context.Background(), r, p, receipt)
	if err != nil || applied == nil || r.patches != 1 {
		t.Fatalf("Apply applied=%v patches=%d err=%v", applied, r.patches, err)
	}
	if _, err = Apply(context.Background(), r, p, receipt); err == nil {
		t.Fatal("Apply replayed a consumed receipt")
	}
}

func TestApplyRejectsStalePVCBeforePatch(t *testing.T) {
	p, r := preparedStorageClassPlan(t)
	r.admitted = p.PVC.DeepCopy()
	receipt, err := Verify(context.Background(), r, p)
	if err != nil {
		t.Fatal(err)
	}
	r.pvc.ResourceVersion = "changed"
	if _, err = Apply(context.Background(), r, p, receipt); err == nil || r.patches != 0 {
		t.Fatalf("Apply stale err=%v patch calls=%d", err, r.patches)
	}
}

func TestObserveExpansionRequiresCapacityAndOriginalConsumerReady(t *testing.T) {
	r := baseCandidateReader()
	r.pvc.Spec.StorageClassName = ptrString("gp3")
	r.pvc.Spec.VolumeName = "pv-data"
	r.pvc.Status.Phase = corev1.ClaimBound
	r.pvc.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("8Gi")
	r.pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("8Gi")}
	r.pv = &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-data", UID: "pv-uid", ResourceVersion: "11"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: "gp3", ClaimRef: &corev1.ObjectReference{Namespace: "fixora-demo", Name: "data", UID: types.UID("pvc-uid")}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}}
	consumer := consumerPod("consumer")
	consumer.UID = "pod-uid"
	consumer.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	r.pods = []corev1.Pod{consumer}
	applied := &Applied{Plan: Plan{Namespace: "fixora-demo", PVCName: "data", PVCUID: "pvc-uid", Action: ActionExpand, RequestedStorage: "8Gi", StorageClass: "gp3", StorageClassUID: "sc-uid", PVName: "pv-data", PVUID: "pv-uid", Consumers: []corev1.ObjectReference{{APIVersion: "v1", Kind: "Pod", Name: "consumer", Namespace: "fixora-demo", UID: "pod-uid"}}}, AppliedResourceVersion: "8"}
	outcome, err := Observe(context.Background(), r, applied)
	if err != nil || outcome.Status != "observed" || !strings.Contains(outcome.Message, "application behavior was not tested") {
		t.Fatalf("Observe outcome=%+v err=%v", outcome, err)
	}
}

func TestObserveReportsFilesystemResizePending(t *testing.T) {
	r := baseCandidateReader()
	r.pvc.Spec.StorageClassName = ptrString("gp3")
	r.pvc.Spec.VolumeName = "pv-data"
	r.pvc.Status.Phase = corev1.ClaimBound
	r.pvc.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("8Gi")
	r.pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("8Gi")}
	r.pvc.Status.Conditions = []corev1.PersistentVolumeClaimCondition{{Type: corev1.PersistentVolumeClaimFileSystemResizePending, Status: corev1.ConditionTrue}}
	r.pv = &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-data", UID: "pv-uid", ResourceVersion: "11"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: "gp3", ClaimRef: &corev1.ObjectReference{Namespace: "fixora-demo", Name: "data", UID: types.UID("pvc-uid")}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}}
	applied := &Applied{Plan: Plan{Namespace: "fixora-demo", PVCName: "data", PVCUID: "pvc-uid", Action: ActionExpand, RequestedStorage: "8Gi", StorageClass: "gp3", StorageClassUID: "sc-uid", PVName: "pv-data", PVUID: "pv-uid"}, AppliedResourceVersion: "8"}
	outcome, err := Observe(context.Background(), r, applied)
	if err != nil || outcome.Status != "pending" || !strings.Contains(outcome.Message, "filesystem resize is still pending") {
		t.Fatalf("Observe outcome=%+v err=%v", outcome, err)
	}
}

func TestObserveDoesNotTreatReplacementConsumerAsVerified(t *testing.T) {
	r := baseCandidateReader()
	r.pvc.Spec.StorageClassName = ptrString("gp3")
	r.pvc.Spec.VolumeName = "pv-data"
	r.pvc.Status.Phase = corev1.ClaimBound
	r.pvc.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("8Gi")
	r.pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("8Gi")}
	r.pv = &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-data", UID: "pv-uid", ResourceVersion: "11"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: "gp3", ClaimRef: &corev1.ObjectReference{Namespace: "fixora-demo", Name: "data", UID: types.UID("pvc-uid")}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}}
	r.pods = []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "consumer", Namespace: "fixora-demo", UID: "replacement"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}}}}
	applied := &Applied{Plan: Plan{Namespace: "fixora-demo", PVCName: "data", PVCUID: "pvc-uid", Action: ActionExpand, RequestedStorage: "8Gi", StorageClass: "gp3", StorageClassUID: "sc-uid", PVName: "pv-data", PVUID: "pv-uid", Consumers: []corev1.ObjectReference{{APIVersion: "v1", Kind: "Pod", Name: "consumer", Namespace: "fixora-demo", UID: "pod-uid"}}}, AppliedResourceVersion: "8"}
	outcome, err := Observe(context.Background(), r, applied)
	if err != nil || outcome.Status != "unknown" {
		t.Fatalf("Observe outcome=%+v err=%v", outcome, err)
	}
}

func TestObserveDoesNotVerifyReplacedPVAfterExpansion(t *testing.T) {
	r := baseCandidateReader()
	r.pvc.Spec.StorageClassName = ptrString("gp3")
	r.pvc.Spec.VolumeName = "pv-data"
	r.pvc.Status.Phase = corev1.ClaimBound
	r.pvc.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("8Gi")
	r.pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("8Gi")}
	r.pv = &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-data", UID: "replacement-pv", ResourceVersion: "12"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: "gp3", ClaimRef: &corev1.ObjectReference{Namespace: "fixora-demo", Name: "data", UID: types.UID("pvc-uid")}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}}
	applied := &Applied{Plan: Plan{Namespace: "fixora-demo", PVCName: "data", PVCUID: "pvc-uid", Action: ActionExpand, RequestedStorage: "8Gi", StorageClass: "gp3", StorageClassUID: "sc-uid", PVName: "pv-data", PVUID: "pv-uid"}, AppliedResourceVersion: "8"}
	outcome, err := Observe(context.Background(), r, applied)
	if err != nil || outcome.Status != "unknown" {
		t.Fatalf("Observe outcome=%+v err=%v", outcome, err)
	}
}

func preparedStorageClassPlan(t *testing.T) (Plan, *deliveryReader) {
	t.Helper()
	a := loadStoragePolicy(t, `apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec: {pvc: {name: data}, action: setStorageClass, storageClassName: gp3}
`)
	base := baseCandidateReader()
	base.pvc.Status.Phase = corev1.ClaimPending
	base.pvc.Spec.StorageClassName = nil
	base.pvc.Spec.VolumeName = ""
	r := &deliveryReader{candidateReader: base}
	p, err := Prepare(context.Background(), r, a, analyzerFinding("MissingStorageClass"))
	if err != nil {
		t.Fatal(err)
	}
	return p, r
}

func analyzerFinding(status string) analyzer.Finding {
	return analyzer.Finding{ResourceKind: "PersistentVolumeClaim", ResourceName: "data", Namespace: "fixora-demo", Status: status, ResourceUID: "pvc-uid"}
}
