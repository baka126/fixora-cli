package storagefix

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestTypedClientUsesServerDryRunForPVCAdmission(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "prod", UID: "pvc-uid", ResourceVersion: "5"}}
	clientset := k8sfake.NewSimpleClientset(pvc)
	runtime, err := NewClient(&kube.TypedClient{Clientset: clientset})
	if err != nil {
		t.Fatal(err)
	}
	patch, _ := json.Marshal([]map[string]any{{"op": "test", "path": "/metadata/uid", "value": "pvc-uid"}, {"op": "test", "path": "/metadata/resourceVersion", "value": "5"}, {"op": "add", "path": "/spec/storageClassName", "value": "gp3"}})
	if _, err = runtime.AdmitPVC(context.Background(), "prod", "data", patch); err != nil {
		t.Fatal(err)
	}
	actions := clientset.Actions()
	if len(actions) != 1 {
		t.Fatalf("patch actions=%d", len(actions))
	}
	if actions[0].GetVerb() != "patch" || actions[0].GetResource().Resource != "persistentvolumeclaims" || actions[0].GetSubresource() != "" {
		t.Fatalf("unexpected API action: %+v", actions[0])
	}
	options, ok := actions[0].(interface{ GetPatchOptions() metav1.PatchOptions })
	if !ok {
		t.Fatalf("action has no patch options: %T", actions[0])
	}
	patchOptions := options.GetPatchOptions()
	if len(patchOptions.DryRun) != 1 || patchOptions.DryRun[0] != metav1.DryRunAll || patchOptions.FieldManager != "fixora-storage-verification" {
		t.Fatalf("PVC verification omitted server-side dry-run: %+v", patchOptions)
	}
	patchAction, ok := actions[0].(k8stesting.PatchAction)
	if !ok || patchAction.GetPatchType() != types.JSONPatchType {
		t.Fatalf("PVC verification did not use JSON Patch: %T", actions[0])
	}
}

func TestNewClientRejectsMissingTypedKubernetesClient(t *testing.T) {
	if _, err := NewClient(nil); err == nil {
		t.Fatal("NewClient accepted a missing Kubernetes client")
	}
	if _, err := NewClient(&kube.TypedClient{}); err == nil {
		t.Fatal("NewClient accepted a typed wrapper without a clientset")
	}
}
