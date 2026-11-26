package shadow

import (
	"reflect"
	"strings"
	"testing"
)

func graphDeployment(name string, config, secret string) GraphNode {
	template := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": name}},
		"spec": map[string]any{
			"containers": []any{map[string]any{
				"name": "app", "image": "example/app:1",
				"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": config}}},
				"env": []any{map[string]any{"name": "TOKEN", "valueFrom": map[string]any{
					"secretKeyRef": map[string]any{"name": secret, "key": "token"},
				}}},
			}},
			"volumes": []any{},
		},
	}
	return GraphNode{
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: "payments", Name: name,
		UID: "uid-" + name, ResourceVersion: "1",
		Object: map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": name, "namespace": "payments"},
			"spec":     map[string]any{"template": template},
		},
	}
}

func TestBuildDependencyGraphDeduplicatesSharedDependencies(t *testing.T) {
	root := graphDeployment("api", "shared-config", "shared-secret")
	worker := graphDeployment("worker", "shared-config", "shared-secret")
	config := GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: "payments", Name: "shared-config", UID: "cm-uid", ResourceVersion: "4"}
	secret := GraphNode{APIVersion: "v1", Kind: "Secret", Namespace: "payments", Name: "shared-secret", UID: "secret-uid", ResourceVersion: "5"}

	graph, err := BuildDependencyGraph("Deployment/payments/api", []GraphNode{worker, secret, root, config})
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Nodes) != 4 {
		t.Fatalf("expected shared dependencies once, got %d nodes: %#v", len(graph.Nodes), graph.Nodes)
	}
	if len(graph.Edges) != 4 {
		t.Fatalf("expected two workload-to-dependency edges per workload, got %#v", graph.Edges)
	}
	if len(graph.Blockers) != 0 {
		t.Fatalf("unexpected blockers: %v", graph.Blockers)
	}
}

func TestBuildDependencyGraphHasStableOrdering(t *testing.T) {
	root := graphDeployment("api", "shared-config", "shared-secret")
	worker := graphDeployment("worker", "shared-config", "shared-secret")
	config := GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: "payments", Name: "shared-config", UID: "cm-uid", ResourceVersion: "4"}
	secret := GraphNode{APIVersion: "v1", Kind: "Secret", Namespace: "payments", Name: "shared-secret", UID: "secret-uid", ResourceVersion: "5"}
	first, err := BuildDependencyGraph("Deployment/payments/api", []GraphNode{root, worker, config, secret})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildDependencyGraph("Deployment/payments/api", []GraphNode{secret, config, worker, root})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("graph ordering changed with input order:\nfirst=%#v\nsecond=%#v", first, second)
	}
}

func TestBuildDependencyGraphExtractsSupportedReferences(t *testing.T) {
	root := graphDeployment("api", "cfg", "credentials")
	worker := graphDeployment("worker", "cfg", "credentials")
	root.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["volumes"] = []any{
		map[string]any{"name": "certs", "secret": map[string]any{"secretName": "credentials"}},
	}
	config := GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: "payments", Name: "cfg", UID: "cm-uid", ResourceVersion: "4"}
	secret := GraphNode{APIVersion: "v1", Kind: "Secret", Namespace: "payments", Name: "credentials", UID: "secret-uid", ResourceVersion: "5"}
	service := GraphNode{
		APIVersion: "v1", Kind: "Service", Namespace: "payments", Name: "api", UID: "svc-uid", ResourceVersion: "7",
		Object: map[string]any{"spec": map[string]any{
			"selector": map[string]any{"app": "api"},
			"ports":    []any{map[string]any{"port": int64(8080), "targetPort": int64(8080), "protocol": "TCP"}},
		}},
	}
	graph, err := BuildDependencyGraph("Deployment/payments/api", []GraphNode{root, worker, config, secret, service})
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Blockers) != 0 {
		t.Fatalf("unexpected blockers: %v", graph.Blockers)
	}
	var kinds []string
	for _, edge := range graph.Edges {
		kinds = append(kinds, string(edge.Type))
	}
	joined := strings.Join(kinds, ",")
	for _, want := range []string{"configmap", "secret", "service"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s edge in %v", want, kinds)
		}
	}
}

func TestBuildDependencyGraphRefusesUnsupportedPersistentStorage(t *testing.T) {
	root := graphDeployment("api", "", "")
	spec := root.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	spec["volumes"] = []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "data"}}}
	pvc := GraphNode{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "payments", Name: "data", UID: "pvc-uid", ResourceVersion: "2"}
	graph, err := BuildDependencyGraph("Deployment/payments/api", []GraphNode{root, pvc})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(graph.Blockers, ";"), "PersistentVolumeClaim") {
		t.Fatalf("PVC must block the whole graph, got %v", graph.Blockers)
	}
}

func TestBuildDependencyGraphIncludesOnlyAuthorizedBoundRWOFilesystemPVC(t *testing.T) {
	root := graphDeployment("api", "", "")
	worker := graphDeployment("worker", "", "")
	for _, workload := range []*GraphNode{&root, &worker} {
		spec := workload.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		spec["volumes"] = []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "ledger-data"}}}
	}
	pvc := GraphNode{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "payments", Name: "ledger-data", UID: "pvc-uid", ResourceVersion: "2", Object: map[string]any{}, Storage: &GraphStorageSource{PVName: "pv-ledger", PVUID: "pv-uid", PVResourceVersion: "4", StorageClassName: "gp3", StorageClassUID: "sc-uid", StorageClassResourceVersion: "3", CSIDriver: "ebs.csi.aws.com", AccessModes: []string{"ReadWriteOnce"}, VolumeMode: "Filesystem", RequestedBytes: 2 << 30, Bound: true}}
	policy := loadGraphStoragePolicyForTest(t, validGraphStoragePolicy)
	graph, err := BuildDependencyGraphWithOptions(root.ID(), []GraphNode{root, worker, pvc}, GraphBuildOptions{StoragePolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Blockers) != 0 {
		t.Fatalf("authorized CSI PVC unexpectedly blocked: %v", graph.Blockers)
	}
	foundEdge, foundPVC := false, false
	for _, edge := range graph.Edges {
		if edge.Type == GraphPVCEdge && edge.To == pvc.ID() {
			if edge.SourceVolumeName != "data" {
				t.Fatalf("PVC edge lost source volume name: %#v", edge)
			}
			foundEdge = true
		}
	}
	for _, node := range graph.Nodes {
		if node.Kind == "PersistentVolumeClaim" {
			foundPVC = true
		}
	}
	if !foundEdge || !foundPVC {
		t.Fatalf("authorized PVC not connected into graph: %#v", graph)
	}
	defaultGraph, err := BuildDependencyGraph(root.ID(), []GraphNode{root, worker, pvc})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(defaultGraph.Blockers, ";"), "PersistentVolumeClaim") {
		t.Fatalf("default graph must still block PVCs: %v", defaultGraph.Blockers)
	}
}

func TestBuildDependencyGraphBlocksIneligibleStorageEvenWithPolicy(t *testing.T) {
	root, worker := graphDeployment("api", "", ""), graphDeployment("worker", "", "")
	spec := root.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	spec["volumes"] = []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "ledger-data"}}}
	pvc := GraphNode{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "payments", Name: "ledger-data", UID: "pvc-uid", ResourceVersion: "2", Storage: &GraphStorageSource{PVName: "pv", PVUID: "pv-uid", PVResourceVersion: "1", StorageClassName: "gp3", StorageClassUID: "sc", StorageClassResourceVersion: "1", CSIDriver: "csi", AccessModes: []string{"ReadWriteMany"}, VolumeMode: "Block", RequestedBytes: 1 << 30, Bound: true}}
	graph, err := BuildDependencyGraphWithOptions(root.ID(), []GraphNode{root, worker, pvc}, GraphBuildOptions{StoragePolicy: loadGraphStoragePolicyForTest(t, validGraphStoragePolicy)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(graph.Blockers, ";"), "not a bound CSI RWO filesystem") {
		t.Fatalf("ineligible storage must block: %v", graph.Blockers)
	}
}

func TestBuildDependencyGraphRejectsRWOCombinedWithOtherAccessModes(t *testing.T) {
	for _, modes := range [][]string{{"ReadWriteOnce", "ReadWriteMany"}, {"ReadWriteOnce", "ReadOnlyMany"}} {
		t.Run(strings.Join(modes, "+"), func(t *testing.T) {
			root, worker := graphDeployment("api", "", ""), graphDeployment("worker", "", "")
			for _, workload := range []*GraphNode{&root, &worker} {
				spec := workload.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
				spec["volumes"] = []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "ledger-data"}}}
			}
			pvc := GraphNode{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "payments", Name: "ledger-data", UID: "pvc-uid", ResourceVersion: "2", Storage: &GraphStorageSource{PVName: "pv", PVUID: "pv-uid", PVResourceVersion: "1", StorageClassName: "gp3", StorageClassUID: "sc", StorageClassResourceVersion: "1", CSIDriver: "csi", AccessModes: modes, VolumeMode: "Filesystem", RequestedBytes: 1 << 30, Bound: true}}
			graph, err := BuildDependencyGraphWithOptions(root.ID(), []GraphNode{root, worker, pvc}, GraphBuildOptions{StoragePolicy: loadGraphStoragePolicyForTest(t, validGraphStoragePolicy)})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(graph.Blockers, ";"), "not a bound CSI RWO filesystem") {
				t.Fatalf("mixed access modes must block before snapshot preflight: %v", graph.Blockers)
			}
		})
	}
}

func TestBuildDependencyGraphRejectsDuplicateIdentity(t *testing.T) {
	root := graphDeployment("api", "", "")
	if _, err := BuildDependencyGraph("Deployment/payments/api", []GraphNode{root, root}); err == nil {
		t.Fatal("duplicate graph node identity must be rejected")
	}
}

func TestBuildDependencyGraphIgnoresUnrelatedUnsupportedWorkload(t *testing.T) {
	root := graphDeployment("api", "shared-config", "shared-secret")
	worker := graphDeployment("worker", "shared-config", "shared-secret")
	unrelated := graphDeployment("unrelated", "", "")
	unrelatedSpec := unrelated.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	unrelatedSpec["volumes"] = []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "unrelated-pvc"}}}
	config := GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: "payments", Name: "shared-config", UID: "cm-uid", ResourceVersion: "4"}
	secret := GraphNode{APIVersion: "v1", Kind: "Secret", Namespace: "payments", Name: "shared-secret", UID: "secret-uid", ResourceVersion: "5"}
	pvc := GraphNode{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "payments", Name: "unrelated-pvc", UID: "pvc-uid", ResourceVersion: "2"}
	graph, err := BuildDependencyGraph("Deployment/payments/api", []GraphNode{root, worker, unrelated, config, secret, pvc})
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Blockers) != 0 {
		t.Fatalf("unrelated unsupported workload must not block root graph, got %v", graph.Blockers)
	}
	for _, node := range graph.Nodes {
		if node.Name == "unrelated" || node.Kind == "PersistentVolumeClaim" {
			t.Fatalf("unrelated nodes leaked into selected graph: %#v", graph.Nodes)
		}
	}
}

func TestBuildDependencyGraphRejectsCrossNamespaceReference(t *testing.T) {
	root := graphDeployment("api", "shared-config", "shared-secret")
	worker := graphDeployment("worker", "shared-config", "shared-secret")
	config := GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: "other", Name: "shared-config", UID: "cm-uid", ResourceVersion: "4"}
	secret := GraphNode{APIVersion: "v1", Kind: "Secret", Namespace: "payments", Name: "shared-secret", UID: "secret-uid", ResourceVersion: "5"}
	graph, err := BuildDependencyGraph("Deployment/payments/api", []GraphNode{root, worker, config, secret})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(graph.Blockers, ";"), "cross-namespace") {
		t.Fatalf("cross-namespace reference must be explicit blocker, got %v", graph.Blockers)
	}
}

func TestBuildDependencyGraphEnforcesNodeLimit(t *testing.T) {
	nodes := []GraphNode{graphDeployment("api", "common", "")}
	for i := 0; i < GraphNodeLimit; i++ {
		name := "worker-" + strings.Repeat("x", i+1)
		nodes = append(nodes, graphDeployment(name, "common", ""))
	}
	nodes = append(nodes, GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: "payments", Name: "common", UID: "cm-uid", ResourceVersion: "1"})
	graph, err := BuildDependencyGraph("Deployment/payments/api", nodes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(graph.Blockers, ";"), "maximum of") {
		t.Fatalf("expected node bound blocker, got %v", graph.Blockers)
	}
}

func TestBuildDependencyGraphEnforcesDepthLimit(t *testing.T) {
	root := graphDeployment("api", "shared-0", "")
	nodes := []GraphNode{root}
	for i := 1; i <= 5; i++ {
		previous := "shared-" + strings.Repeat("x", i-1)
		if i == 1 {
			previous = "shared-0"
		}
		workload := graphDeployment("worker-"+strings.Repeat("x", i), previous, "")
		container := workload.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		container["envFrom"] = append(container["envFrom"].([]any), map[string]any{"configMapRef": map[string]any{"name": "shared-" + strings.Repeat("x", i)}})
		nodes = append(nodes, workload)
	}
	for i := 0; i <= 5; i++ {
		name := "shared-" + strings.Repeat("x", i)
		if i == 0 {
			name = "shared-0"
		}
		nodes = append(nodes, GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: "payments", Name: name, UID: "cm-" + name, ResourceVersion: "1"})
	}
	graph, err := BuildDependencyGraph("Deployment/payments/api", nodes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(graph.Blockers, ";"), "maximum depth") {
		t.Fatalf("expected depth bound blocker, got %v", graph.Blockers)
	}
}

func TestBuildDependencyGraphEnforcesSecretByteLimit(t *testing.T) {
	root := graphDeployment("api", "shared-config", "shared-secret")
	worker := graphDeployment("worker", "shared-config", "shared-secret")
	config := GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: "payments", Name: "shared-config", UID: "cm-uid", ResourceVersion: "4"}
	secret := GraphNode{APIVersion: "v1", Kind: "Secret", Namespace: "payments", Name: "shared-secret", UID: "secret-uid", ResourceVersion: "5", Object: map[string]any{"data": map[string]any{"token": strings.Repeat("x", 64*1024+1)}}}
	graph, err := BuildDependencyGraph("Deployment/payments/api", []GraphNode{root, worker, config, secret})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(graph.Blockers, ";"), "Secret data exceeds") {
		t.Fatalf("expected Secret size blocker, got %v", graph.Blockers)
	}
}

func TestBuildDependencyGraphEnforcesConfigMapKeyLimit(t *testing.T) {
	root := graphDeployment("api", "shared-config", "")
	worker := graphDeployment("worker", "shared-config", "")
	data := map[string]any{}
	for i := 0; i <= graphConfigMapKeyLimit; i++ {
		data[strings.Repeat("k", i+1)] = "v"
	}
	config := GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: "payments", Name: "shared-config", UID: "cm-uid", ResourceVersion: "4", Object: map[string]any{"data": data}}
	graph, err := BuildDependencyGraph("Deployment/payments/api", []GraphNode{root, worker, config})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(graph.Blockers, ";"), "ConfigMap keys") {
		t.Fatalf("expected ConfigMap key limit blocker, got %v", graph.Blockers)
	}
}

func TestBuildDependencyGraphEnforcesRootEdgeLimit(t *testing.T) {
	root := graphDeployment("api", "cfg-0", "")
	worker := graphDeployment("worker", "cfg-0", "")
	nodes := []GraphNode{root, worker, {APIVersion: "v1", Kind: "ConfigMap", Namespace: "payments", Name: "cfg-0", UID: "cfg-0", ResourceVersion: "1"}}
	for i := 0; i < graphRootEdgeLimit; i++ {
		name := "cfg-" + strings.Repeat("x", i+1)
		container := root.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		container["envFrom"] = append(container["envFrom"].([]any), map[string]any{"configMapRef": map[string]any{"name": name}})
		nodes = append(nodes, GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: "payments", Name: name, UID: name, ResourceVersion: "1"})
	}
	graph, err := BuildDependencyGraph("Deployment/payments/api", nodes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(graph.Blockers, ";"), "root edges") {
		t.Fatalf("expected root edge limit blocker, got %v", graph.Blockers)
	}
}

func TestBuildDependencyGraphEnforcesConfigMapByteLimit(t *testing.T) {
	root := graphDeployment("api", "shared-config", "")
	worker := graphDeployment("worker", "shared-config", "")
	config := GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: "payments", Name: "shared-config", UID: "cm-uid", ResourceVersion: "4", Object: map[string]any{"data": map[string]any{"application": strings.Repeat("x", 64*1024+1)}}}
	graph, err := BuildDependencyGraph("Deployment/payments/api", []GraphNode{root, worker, config})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(graph.Blockers, ";"), "ConfigMap data exceeds") {
		t.Fatalf("expected ConfigMap data size blocker, got %v", graph.Blockers)
	}
}
