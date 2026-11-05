package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/storagefix"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPVCFixWithoutPolicyExplainsPrerequisiteWithoutMutation(t *testing.T) {
	var out strings.Builder
	var errout strings.Builder
	f := pvcCLIFinding()
	if code := runPVCFixRuntime(context.Background(), &out, &errout, options{}, nil, f); code != 0 {
		t.Fatalf("runPVCFixRuntime exit=%d stderr=%s", code, errout.String())
	}
	if !strings.Contains(out.String(), "--pvc-policy") || !strings.Contains(out.String(), "No changes made") {
		t.Fatalf("missing concise policy prerequisite: %q", out.String())
	}
}

func TestPVCFixPreviewDoesNotDryRunOrWrite(t *testing.T) {
	o, r, f := pvcCLISetup(t)
	o.preview = true
	var out strings.Builder
	var errout strings.Builder
	if code := runPVCFixRuntime(context.Background(), &out, &errout, o, r, f); code != 0 || r.admitCalls != 0 || r.patchCalls != 0 {
		t.Fatalf("preview exit=%d admission=%d patches=%d stderr=%s out=%s", code, r.admitCalls, r.patchCalls, errout.String(), out.String())
	}
	if !strings.Contains(out.String(), "Preview only") || !strings.Contains(out.String(), "No changes made") {
		t.Fatalf("preview output unclear: %q", out.String())
	}
}

func TestPVCFixAcceptsKubernetesPVCShortKindForHealthyTarget(t *testing.T) {
	o, r, f := pvcCLISetup(t)
	f.ResourceKind = "Pvc"
	o.preview = true
	var out strings.Builder
	var errout strings.Builder
	if code := runPVCFixRuntime(context.Background(), &out, &errout, o, r, f); code != 0 || !strings.Contains(out.String(), "Preview only") {
		t.Fatalf("PVC alias preview exit=%d stderr=%s out=%s", code, errout.String(), out.String())
	}
}

func TestPVCFixApplyRequiresPolicyAndReportsNonRollbackableExpansion(t *testing.T) {
	o, r, f := pvcCLISetup(t)
	o.pvcPolicy = writePVCPolicy(t, `apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec: {pvc: {name: data}, action: expand, requestedStorage: 8Gi, maxStorage: 10Gi}
`)
	o.apply = true
	o.yes = true
	var out strings.Builder
	var errout strings.Builder
	if code := runPVCFixRuntime(context.Background(), &out, &errout, o, r, f); code != 0 {
		t.Fatalf("apply exit=%d stderr=%s out=%s", code, errout.String(), out.String())
	}
	if r.admitCalls != 1 || r.patchCalls != 1 || !strings.Contains(out.String(), "cannot be rolled back") {
		t.Fatalf("apply evidence missing: admission=%d patches=%d output=%s", r.admitCalls, r.patchCalls, out.String())
	}
}

func TestPVCPolicyFlagParses(t *testing.T) {
	parsed, rest, err := parseFlags([]string{"pvc/data", "-n", "fixora-demo", "--pvc-policy", "approved.yaml", "--preview"})
	if err != nil || parsed.pvcPolicy != "approved.yaml" || !parsed.preview || len(rest) != 1 || rest[0] != "pvc/data" {
		t.Fatalf("parsed=%+v rest=%v err=%v", parsed, rest, err)
	}
}

func TestPVCNextStepUsesDedicatedPolicyCommand(t *testing.T) {
	want := "kubectl fixora fix pvc/data -n prod --pvc-policy <approved-local-file> --preview"
	if got := nextConcreteFixHint("pvc/data", fix.Plan{Resource: "PersistentVolumeClaim/data", Namespace: "prod", Strategy: "pvc"}); got != want {
		t.Fatalf("next step=%q, want %q", got, want)
	}
}

func TestPVCFixRejectsConflictingPatchAndApplyModes(t *testing.T) {
	o, r, f := pvcCLISetup(t)
	o.apply = true
	o.visited = map[string]bool{"delivery": true}
	o.delivery = "patch"
	var out strings.Builder
	var errout strings.Builder
	if code := runPVCFixRuntime(context.Background(), &out, &errout, o, r, f); code != 2 || r.admitCalls != 0 || r.patchCalls != 0 {
		t.Fatalf("conflicting mode exit=%d admissions=%d patches=%d stderr=%s", code, r.admitCalls, r.patchCalls, errout.String())
	}
}

type pvcCLIRuntime struct {
	*storagefixReader
	admitCalls int
	patchCalls int
}

var _ storagefix.Runtime = (*pvcCLIRuntime)(nil)

type storagefixReader struct {
	pvc     *corev1.PersistentVolumeClaim
	sc      *storagev1.StorageClass
	pv      *corev1.PersistentVolume
	pods    []corev1.Pod
	updated *corev1.PersistentVolumeClaim
}

func (r *pvcCLIRuntime) GetPVC(context.Context, string, string) (*corev1.PersistentVolumeClaim, error) {
	return r.pvc.DeepCopy(), nil
}
func (r *pvcCLIRuntime) GetStorageClass(context.Context, string) (*storagev1.StorageClass, error) {
	return r.sc.DeepCopy(), nil
}
func (r *pvcCLIRuntime) GetPV(context.Context, string) (*corev1.PersistentVolume, error) {
	return r.pv.DeepCopy(), nil
}
func (r *pvcCLIRuntime) ListPods(context.Context, string) ([]corev1.Pod, error) {
	return append([]corev1.Pod(nil), r.pods...), nil
}
func (r *pvcCLIRuntime) ListStatefulSets(context.Context, string) ([]appsv1.StatefulSet, error) {
	return nil, nil
}
func (r *pvcCLIRuntime) ResolveOwner(_ context.Context, pod corev1.Pod) (kube.WorkloadOwner, error) {
	return kube.WorkloadOwner{Kind: "Pod", Name: pod.Name, UID: string(pod.UID)}, nil
}
func (r *pvcCLIRuntime) AdmitPVC(context.Context, string, string, []byte) (*corev1.PersistentVolumeClaim, error) {
	r.admitCalls++
	return r.updated.DeepCopy(), nil
}
func (r *pvcCLIRuntime) PatchPVC(context.Context, string, string, []byte) (*corev1.PersistentVolumeClaim, error) {
	r.patchCalls++
	r.pvc = r.updated.DeepCopy()
	return r.updated.DeepCopy(), nil
}

func pvcCLISetup(t *testing.T) (options, *pvcCLIRuntime, analyzer.Finding) {
	t.Helper()
	r := &pvcCLIRuntime{storagefixReader: &storagefixReader{
		pvc: &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "fixora-demo", UID: "pvc-uid", ResourceVersion: "7"}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: ptrPVCString("gp3"), VolumeName: "pv-data", Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("4Gi")}}}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("4Gi")}}},
		sc:  &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "gp3", UID: "sc-uid", ResourceVersion: "9"}, Provisioner: "ebs.csi.aws.com", AllowVolumeExpansion: ptrPVCBool(true)},
		pv:  &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-data", UID: "pv-uid", ResourceVersion: "11"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: "gp3", ClaimRef: &corev1.ObjectReference{Namespace: "fixora-demo", Name: "data", UID: "pvc-uid"}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}},
	}}
	updated := r.pvc.DeepCopy()
	updated.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("8Gi")
	updated.ResourceVersion = "8"
	r.updated = updated
	return options{output: "text", pvcPolicy: writePVCPolicy(t, `apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec: {pvc: {name: data}, action: expand, requestedStorage: 8Gi, maxStorage: 10Gi}
`)}, r, analyzer.Finding{ResourceKind: "PersistentVolumeClaim", ResourceName: "data", Namespace: "fixora-demo", Status: "SmallStorageRequest", ResourceUID: "pvc-uid"}
}

func pvcCLIFinding() analyzer.Finding {
	return analyzer.Finding{ResourceKind: "PersistentVolumeClaim", ResourceName: "data", Namespace: "fixora-demo", Status: "SmallStorageRequest", ResourceUID: "pvc-uid", Summary: "PVC storage request is small"}
}
func writePVCPolicy(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pvc-policy.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
func ptrPVCString(s string) *string { return &s }
func ptrPVCBool(b bool) *bool       { return &b }
