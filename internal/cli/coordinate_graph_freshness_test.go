package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/coordinate"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRefreshCoordinateGraphSnapshotDetectsSourceDriftWithoutValues(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "payments", Name: "db", UID: "secret-current", ResourceVersion: "9"}, Data: map[string][]byte{"password": []byte("sensitive-value-sentinel")}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "payments", Name: "api", UID: "api-uid", ResourceVersion: "2"}}
	client := &kube.TypedClient{Clientset: fake.NewSimpleClientset(secret, deployment)}
	graph := shadow.DependencyGraph{Root: "Deployment/payments/api", Nodes: []shadow.GraphNode{{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "payments", Name: "api", UID: "api-uid", ResourceVersion: "1", CandidatePatch: "candidate"}, {APIVersion: "v1", Kind: "Secret", Namespace: "payments", Name: "db", UID: "secret-old", ResourceVersion: "8"}}}
	snapshot, err := refreshCoordinateGraphSnapshot(context.Background(), client, graph)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Nodes[1].UID != "secret-current" || snapshot.Nodes[1].ResourceVersion != "9" {
		t.Fatalf("source identity was not refreshed: %#v", snapshot.Nodes[1])
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sensitive-value-sentinel") {
		t.Fatal("source Secret value leaked into graph snapshot")
	}
}

func TestRefreshCoordinateGraphSnapshotRejectsStalePVCIdentity(t *testing.T) {
	policy := `apiVersion: fixora.io/v1alpha1
kind: GraphStorageShadowPolicy
metadata:
  name: payments-shadow
  namespace: payments
spec:
  maxTotalLogicalCapacity: 8Gi
  pvcs:
  - name: ledger-data
    volumeSnapshotClassName: ebs-snapshots
    restoreStorageClassName: gp3-shadow
    maxLogicalCapacity: 4Gi
    allowCrashConsistent: true
`
	path := filepath.Join(t.TempDir(), "storage-policy.yaml")
	if err := os.WriteFile(path, []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	authority, err := shadow.LoadGraphStoragePolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	pvc := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"namespace": "payments", "name": "ledger-data", "uid": "pvc-uid", "resourceVersion": "2"}}}
	dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), pvc)
	client := &kube.TypedClient{Clientset: fake.NewSimpleClientset(), Dynamic: dynamic}
	graph := shadow.DependencyGraph{Root: "Deployment/payments/api", Nodes: []shadow.GraphNode{
		{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "payments", Name: "api", UID: "api-uid", ResourceVersion: "1", CandidatePatch: "candidate"},
		{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "payments", Name: "ledger-data", UID: "pvc-uid", ResourceVersion: "1", Storage: &shadow.GraphStorageSource{PVName: "pv-a", PVUID: "pv-uid", PVResourceVersion: "1", StorageClassName: "gp3", StorageClassUID: "sc-uid", StorageClassResourceVersion: "1", CSIDriver: "ebs.csi.aws.com", AccessModes: []string{"ReadWriteOnce"}, VolumeMode: "Filesystem", RequestedBytes: 1 << 30, VolumeSnapshotClassName: "ebs-snapshots", VolumeSnapshotClassUID: "vsc-uid", VolumeSnapshotClassResourceVersion: "1", RestoreStorageClassName: "gp3-shadow", RestoreStorageClassUID: "restore-sc-uid", RestoreStorageClassResourceVersion: "1", StoragePolicyDigest: authority.Digest()}},
	}}
	if _, err := refreshCoordinateGraphSnapshot(context.Background(), client, graph, authority); err == nil {
		t.Fatal("PVC resourceVersion drift must block graph freshness certification")
	}
}

func TestBindCoordinateGraphCandidatesRequiresExactWorkloadSet(t *testing.T) {
	graph := shadow.DependencyGraph{Nodes: []shadow.GraphNode{{Kind: "Deployment", Namespace: "payments", Name: "api"}, {Kind: "Deployment", Namespace: "payments", Name: "worker"}}}
	steps := []coordinate.Step{{Kind: "Deployment", Namespace: "payments", Name: "api", Ref: "Deployment/api", Plan: fix.Plan{PatchTemplate: "candidate-api"}}, {Kind: "Deployment", Namespace: "payments", Name: "worker", Ref: "Deployment/worker", Plan: fix.Plan{PatchTemplate: "candidate-worker"}}}
	bound, err := bindCoordinateGraphCandidates(graph, steps)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Nodes[0].CandidatePatch != "candidate-api" || bound.Nodes[1].CandidatePatch != "candidate-worker" {
		t.Fatalf("unexpected candidate binding: %#v", bound.Nodes)
	}
	if _, err := bindCoordinateGraphCandidates(graph, steps[:1]); err == nil {
		t.Fatal("missing graph workload candidate must block")
	}
}
