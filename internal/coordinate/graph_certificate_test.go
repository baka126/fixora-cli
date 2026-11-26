package coordinate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

func validGraphCertFixture() (shadow.DependencyGraph, shadow.GraphResult) {
	root := shadow.GraphNode{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "payments", Name: "api", UID: "api-uid", ResourceVersion: "11", CandidatePatch: "candidate-api"}
	worker := shadow.GraphNode{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "payments", Name: "worker", UID: "worker-uid", ResourceVersion: "12", CandidatePatch: "candidate-worker"}
	secret := shadow.GraphNode{APIVersion: "v1", Kind: "Secret", Namespace: "payments", Name: "db", UID: "secret-uid", ResourceVersion: "13"}
	graph := shadow.DependencyGraph{Root: root.ID(), Nodes: []shadow.GraphNode{root, worker, secret}, Edges: []shadow.GraphEdge{{From: root.ID(), To: secret.ID(), Type: shadow.GraphSecretEdge}, {From: worker.ID(), To: secret.ID(), Type: shadow.GraphSecretEdge}}}
	result := shadow.GraphResult{Verified: true, Status: shadow.VerificationVerified, Namespace: "fixora-shadow-run", NamespaceUID: "namespace-uid", NamespaceResourceVersion: "21", CleanupStatus: "deleted", HasSecrets: true,
		Workloads:          []shadow.GraphWorkloadResult{{Source: root.ID(), Pod: "api-clone", PodUID: "api-pod-uid", PodResourceVersion: "31", Verified: true, Status: shadow.VerificationVerified}, {Source: worker.ID(), Pod: "worker-clone", PodUID: "worker-pod-uid", PodResourceVersion: "32", Verified: true, Status: shadow.VerificationVerified}},
		SourceDependencies: []shadow.GraphNode{secret},
		Dependencies:       []shadow.GraphNode{{APIVersion: "v1", Kind: "Secret", Namespace: "fixora-shadow-run", Name: "db", UID: "shadow-secret-uid", ResourceVersion: "1"}},
		NetworkPolicies:    []shadow.GraphNode{{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy", Namespace: "fixora-shadow-run", Name: "fixora-default-deny", UID: "deny-uid", ResourceVersion: "1"}, {APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy", Namespace: "fixora-shadow-run", Name: "fixora-graph-access", UID: "allow-uid", ResourceVersion: "2"}},
	}
	snapshot := GraphSnapshot{Root: graph.Root, Edges: append([]shadow.GraphEdge(nil), graph.Edges...)}
	for _, node := range graph.Nodes {
		snapshot.Nodes = append(snapshot.Nodes, GraphSourceIdentity{ID: node.ID(), APIVersion: node.APIVersion, Kind: node.Kind, Namespace: node.Namespace, Name: node.Name, UID: node.UID, ResourceVersion: node.ResourceVersion, Candidate: node.CandidatePatch})
	}
	canonicalizeGraphSnapshot(&snapshot)
	result.GraphDigest = snapshot.Digest
	return graph, result
}

func TestGraphCertificateRejectsCandidateMutation(t *testing.T) {
	graph, result := validGraphCertFixture()
	cert, err := NewGraphCertificate(graph, result)
	if err != nil {
		t.Fatal(err)
	}
	current := cert.Snapshot
	current.Nodes = append([]GraphSourceIdentity(nil), current.Nodes...)
	current.Nodes[0].Candidate = "mutated candidate"
	if err := cert.Check(current); err == nil {
		t.Fatal("candidate mutation must invalidate graph certificate")
	}
}

func TestGraphCertificateRejectsMutationOfItsStoredCandidate(t *testing.T) {
	graph, result := validGraphCertFixture()
	cert, err := NewGraphCertificate(graph, result)
	if err != nil {
		t.Fatal(err)
	}
	current := cert.Snapshot
	current.Nodes = append([]GraphSourceIdentity(nil), current.Nodes...)
	cert.Snapshot.Nodes = append([]GraphSourceIdentity(nil), cert.Snapshot.Nodes...)
	cert.Snapshot.Nodes[0].Candidate = "mutated stored candidate"
	if err := cert.Check(current); err == nil {
		t.Fatal("mutating the certificate's stored candidate must invalidate its seal")
	}
}

func TestGraphCertificateJSONOmitsCandidateBytes(t *testing.T) {
	graph, result := validGraphCertFixture()
	graph.Nodes[0].CandidatePatch = "candidate-secret-sentinel"
	graph.Nodes[1].CandidatePatch = "candidate-secret-sentinel"
	snapshot, err := GraphSourceSnapshot(graph)
	if err != nil {
		t.Fatal(err)
	}
	result.GraphDigest = snapshot.Digest
	cert, err := NewGraphCertificate(graph, result)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(cert)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "candidate-secret-sentinel") {
		t.Fatal("candidate bytes leaked from graph certificate JSON")
	}
}

func TestGraphCertificateRejectsStaleDependencySnapshot(t *testing.T) {
	graph, result := validGraphCertFixture()
	cert, err := NewGraphCertificate(graph, result)
	if err != nil {
		t.Fatal(err)
	}
	current := cert.Snapshot
	current.Nodes = append([]GraphSourceIdentity(nil), current.Nodes...)
	for i := range current.Nodes {
		if current.Nodes[i].Kind == "Secret" {
			current.Nodes[i].ResourceVersion = "14"
		}
	}
	if err := cert.Check(current); err == nil {
		t.Fatal("dependency version drift must invalidate graph certificate")
	}
}

func TestGraphCertificateRejectsDuplicateNodes(t *testing.T) {
	graph, result := validGraphCertFixture()
	graph.Nodes = append(graph.Nodes, graph.Nodes[0])
	if _, err := NewGraphCertificate(graph, result); err == nil {
		t.Fatal("duplicate graph node must be rejected")
	}
}

func TestGraphCertificateRejectsMissingWorkloadResult(t *testing.T) {
	graph, result := validGraphCertFixture()
	result.Workloads = result.Workloads[:1]
	if _, err := NewGraphCertificate(graph, result); err == nil {
		t.Fatal("partial workload results must not produce a graph certificate")
	}
}

func TestGraphCertificateAllowsConnectedGraphWithoutIndependentStepCheck(t *testing.T) {
	graph, result := validGraphCertFixture()
	cert, err := NewGraphCertificate(graph, result)
	if err != nil {
		t.Fatalf("connected graph should be certified by graph evidence: %v", err)
	}
	if err := cert.Check(cert.Snapshot); err != nil {
		t.Fatalf("unchanged graph should remain certified: %v", err)
	}
}

func TestGraphCertificatePreservesIndependentCoordinatePath(t *testing.T) {
	steps := []CertifiedStep{
		{Step: Step{Kind: "Deployment", Name: "api", Namespace: "payments"}, Before: repairpolicy.Snapshot{Object: map[string]any{"metadata": map[string]any{"ownerReferences": []any{}}}}},
		{Step: Step{Kind: "Deployment", Name: "worker", Namespace: "payments"}, Before: repairpolicy.Snapshot{Object: map[string]any{"metadata": map[string]any{"ownerReferences": []any{}}}}},
	}
	if err := CheckIndependentSteps(steps); err != nil {
		t.Fatalf("legacy independent coordinate check changed: %v", err)
	}
}

func validStorageGraphCertFixture() (shadow.DependencyGraph, shadow.GraphResult) {
	graph, result := validGraphCertFixture()
	pvc := shadow.GraphNode{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "payments", Name: "data", UID: "source-pvc", ResourceVersion: "41", Storage: &shadow.GraphStorageSource{
		PVName: "source-pv", PVUID: "source-pv-uid", PVResourceVersion: "42", StorageClassName: "source-sc", StorageClassUID: "source-sc-uid", StorageClassResourceVersion: "43", CSIDriver: "csi.example.test", AccessModes: []string{"ReadWriteOnce"}, VolumeMode: "Filesystem", RequestedBytes: 1 << 20,
		VolumeSnapshotClassName: "snapshot-class", VolumeSnapshotClassUID: "snapshot-class-uid", VolumeSnapshotClassResourceVersion: "44", RestoreStorageClassName: "restore-sc", RestoreStorageClassUID: "restore-sc-uid", RestoreStorageClassResourceVersion: "45", StoragePolicyDigest: "policy-digest",
	}}
	graph.Nodes = append(graph.Nodes, pvc)
	graph.Edges = append(graph.Edges, shadow.GraphEdge{From: "Deployment/payments/api", To: pvc.ID(), Type: shadow.GraphPVCEdge, SourceVolumeName: "data-volume"})
	result.HasPersistentData = true
	result.StoragePolicyDigest = "policy-digest"
	result.StorageSources = []shadow.GraphNode{pvc}
	result.StorageSnapshots = []shadow.GraphNode{{APIVersion: "snapshot.storage.k8s.io/v1", Kind: "VolumeSnapshot", Namespace: "payments", Name: "run-data", UID: "snapshot-uid", ResourceVersion: "1"}}
	result.StorageSnapshotReceipts = []shadow.GraphStorageSnapshotReceipt{{SourcePVC: pvc.ID(), Snapshot: result.StorageSnapshots[0], Content: shadow.GraphNode{APIVersion: "snapshot.storage.k8s.io/v1", Kind: "VolumeSnapshotContent", Name: "content-data", UID: "content-uid", ResourceVersion: "2"}}}
	result.StorageGrant = &shadow.GraphNode{APIVersion: "gateway.networking.k8s.io/v1beta1", Kind: "ReferenceGrant", Namespace: "payments", Name: "run-grant", UID: "grant-uid", ResourceVersion: "3"}
	result.RestoredClaims = []shadow.GraphNode{{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: result.Namespace, Name: "restored-data", UID: "restored-pvc-uid", ResourceVersion: "4"}}
	result.ClaimReceipts = []shadow.GraphClaimReceipt{{SourcePVC: pvc.ID(), SourcePVCUID: pvc.UID, SourcePVCResourceVersion: pvc.ResourceVersion, RestoredPVC: "restored-data", RestoredPVCUID: "restored-pvc-uid", PV: "restored-pv", PVUID: "restored-pv-uid", CSIDriver: pvc.Storage.CSIDriver, Node: "worker-a", NodeUID: "node-a"}}
	result.StorageCleanup = &shadow.GraphStorageCleanup{Complete: true, Deleted: []shadow.GraphNode{*result.StorageGrant, result.StorageSnapshots[0]}}
	snapshot, err := GraphSourceSnapshot(graph)
	if err != nil {
		panic(err)
	}
	result.GraphDigest = snapshot.Digest
	return graph, result
}

func TestGraphCertificateRequiresCompleteStorageReceipts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*shadow.GraphResult)
	}{
		{"missing snapshot", func(r *shadow.GraphResult) { r.StorageSnapshots = nil }},
		{"missing grant", func(r *shadow.GraphResult) { r.StorageGrant = nil }},
		{"missing restored claim", func(r *shadow.GraphResult) { r.RestoredClaims = nil }},
		{"missing delayed binding topology", func(r *shadow.GraphResult) { r.ClaimReceipts[0].NodeUID = "" }},
		{"duplicate claim", func(r *shadow.GraphResult) { r.RestoredClaims = append(r.RestoredClaims, r.RestoredClaims[0]) }},
		{"cleanup incomplete", func(r *shadow.GraphResult) { r.StorageCleanup.Complete = false }},
		{"snapshot not deleted", func(r *shadow.GraphResult) { r.StorageCleanup.Deleted = r.StorageCleanup.Deleted[:1] }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			graph, result := validStorageGraphCertFixture()
			test.mutate(&result)
			if _, err := NewGraphCertificate(graph, result); err == nil {
				t.Fatal("incomplete storage evidence must not produce a certificate")
			}
		})
	}
}

func TestGraphCertificateBindsStoragePolicyAndSourceIdentity(t *testing.T) {
	for _, mutate := range []struct {
		name string
		fn   func(*GraphStorageSourceIdentity)
	}{
		{"policy digest", func(s *GraphStorageSourceIdentity) { s.StoragePolicyDigest = "stale-policy" }},
		{"source PV UID", func(s *GraphStorageSourceIdentity) { s.PVUID = "replacement-pv" }},
		{"source StorageClass RV", func(s *GraphStorageSourceIdentity) { s.StorageClassResourceVersion = "stale" }},
		{"snapshot class UID", func(s *GraphStorageSourceIdentity) { s.VolumeSnapshotClassUID = "replacement-class" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			graph, result := validStorageGraphCertFixture()
			cert, err := NewGraphCertificate(graph, result)
			if err != nil {
				t.Fatal(err)
			}
			current := cert.Snapshot
			current.Nodes = append([]GraphSourceIdentity(nil), cert.Snapshot.Nodes...)
			for i := range current.Nodes {
				if current.Nodes[i].Storage != nil {
					storage := *current.Nodes[i].Storage
					storage.AccessModes = append([]string(nil), storage.AccessModes...)
					mutate.fn(&storage)
					current.Nodes[i].Storage = &storage
				}
			}
			if err := cert.Check(current); err == nil {
				t.Fatal("storage identity mutation must invalidate certificate")
			}
		})
	}
}

func TestGraphCertificateStorageJSONOmitsBackendHandles(t *testing.T) {
	graph, result := validStorageGraphCertFixture()
	result.StorageSnapshotReceipts[0].Content.Object = map[string]any{"spec": map[string]any{"source": map[string]any{"snapshotHandle": "backend-handle-secret-sentinel"}}}
	cert, err := NewGraphCertificate(graph, result)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(cert)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "backend-handle-secret-sentinel") || strings.Contains(string(encoded), "snapshotHandle") {
		t.Fatal("storage backend identity or data leaked into certificate JSON")
	}
}
