package shadow

import (
	"context"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	corefake "k8s.io/client-go/kubernetes/fake"
)

const graphTestRunID = "12345678-1234-4234-8234-123456789abc"

func graphRunWorkload(name string, labels map[string]string) GraphNode {
	return GraphNode{
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: "payments", Name: name,
		UID: "uid-" + name, ResourceVersion: "7",
		CandidatePatch: "apiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n      - name: app\n        image: example/app:2\n",
		Object: map[string]any{
			"spec": map[string]any{"template": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec":     map[string]any{"containers": []any{map[string]any{"name": "app", "image": "example/app:1"}}},
			}},
		},
	}
}

func TestRunGraphRejectsIncompleteCandidateBeforeCreatingNamespace(t *testing.T) {
	root := graphRunWorkload("api", map[string]string{"app": "api"})
	worker := graphRunWorkload("worker", map[string]string{"app": "worker"})
	worker.CandidatePatch = ""
	clientset := corefake.NewSimpleClientset()
	client := &kube.TypedClient{Clientset: clientset}
	result, err := RunGraph(context.Background(), client, GraphRequest{Graph: DependencyGraph{Root: root.ID(), Nodes: []GraphNode{root, worker}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verified || len(result.Blockers) == 0 {
		t.Fatalf("incomplete candidate must block: %#v", result)
	}
	created, err := clientset.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
	if err != nil || len(created.Items) != 0 {
		t.Fatalf("blocked preflight created namespace: %v %#v", err, created)
	}
}

func TestRunGraphRequiresSeparateStorageConfirmationBeforeNamespaceWrite(t *testing.T) {
	root, worker := graphRunWorkload("api", map[string]string{"app": "api"}), graphRunWorkload("worker", map[string]string{"app": "worker"})
	pvc := GraphNode{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "payments", Name: "data", UID: "pvc-uid", ResourceVersion: "1", Storage: &GraphStorageSource{PVName: "pv", PVUID: "pv-uid", PVResourceVersion: "2", StorageClassName: "gp3", StorageClassUID: "sc", StorageClassResourceVersion: "3", CSIDriver: "ebs.csi.aws.com", AccessModes: []string{"ReadWriteOnce"}, VolumeMode: "Filesystem", RequestedBytes: 1 << 30, Bound: true}}
	clientset := corefake.NewSimpleClientset()
	client := &kube.TypedClient{Clientset: clientset}
	graph := DependencyGraph{Root: root.ID(), Nodes: []GraphNode{root, worker, pvc}, Edges: []GraphEdge{{From: root.ID(), To: pvc.ID(), Type: GraphPVCEdge, SourceVolumeName: "data"}}}
	result, err := RunGraph(context.Background(), client, GraphRequest{Graph: graph})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verified || len(result.Blockers) == 0 {
		t.Fatalf("PVC graph ran without separate storage consent: %#v", result)
	}
	namespaces, err := clientset.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
	if err != nil || len(namespaces.Items) != 0 {
		t.Fatalf("storage confirmation refusal created namespace: %#v %v", namespaces, err)
	}
}

func TestGraphWorkloadCloneStripsProductionIdentityAndAddsRunLabels(t *testing.T) {
	node := graphRunWorkload("api", map[string]string{"app": "api", "team": "payments"})
	node.Object["metadata"] = map[string]any{"labels": map[string]any{"prod": "true"}, "ownerReferences": []any{map[string]any{"uid": "prod-owner"}}}
	pod, _, err := graphWorkloadPod(node, graphTestRunID, "fixora-shadow-run", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if pod.Labels[labelGraph] != graphTestRunID || pod.Labels[labelSession] != graphTestRunID || pod.Labels["prod"] != "" || len(pod.OwnerReferences) != 0 {
		t.Fatalf("unsafe clone identity: %#v", pod.ObjectMeta)
	}
	if pod.Spec.ServiceAccountName != "" || pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatalf("service-account token was not disabled: %#v", pod.Spec)
	}
}

func TestGraphCloneAllowsOnlySnapshottedSecretDependencies(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "runtime-secret"}, Key: "token"}}}}}}}}
	if err := validateShadowSourceWithSecretDependencies(pod, map[string]bool{"runtime-secret": true}); err != nil {
		t.Fatalf("snapshotted secret should be permitted: %v", err)
	}
	if err := validateShadowSourceWithSecretDependencies(pod, map[string]bool{"different-secret": true}); err == nil {
		t.Fatal("unreferenced secret must remain blocked")
	}
}

func TestGraphCandidateRejectsReintroducedIdentityAndPullSecrets(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{AutomountServiceAccountToken: func() *bool { value := true; return &value }(), ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry"}}}}
	if err := validateGraphCandidatePod(pod, nil); err == nil {
		t.Fatal("candidate must not reintroduce service credentials or image pull secrets")
	}
}

func TestGraphSecretRunsCannotBeKept(t *testing.T) {
	if shouldKeepGraphNamespace(true, true, true, nil) {
		t.Fatal("Secret-bearing shadow namespaces must always be cleaned")
	}
	if shouldKeepGraphNamespace(true, false, true, nil) != true {
		t.Fatal("explicit keep is allowed for verified non-Secret graph")
	}
	if shouldKeepGraphNamespace(true, false, false, nil) {
		t.Fatal("failed graph runs must be cleaned")
	}
}

func TestGraphPVCBackedRunsCannotBeKept(t *testing.T) {
	if shouldRetainGraphNamespace(true, false, true, nil, true) {
		t.Fatal("PVC-backed graph shadow must always be cleaned even when keep was requested")
	}
	if !shouldRetainGraphNamespace(true, false, true, nil, false) {
		t.Fatal("non-PVC graph keep behavior should remain available")
	}
}

func TestGraphCleanupSurvivesCancelledRunContext(t *testing.T) {
	runID := graphTestRunID
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: shadowNamespaceName(runID), UID: "namespace-uid", ResourceVersion: "1", Labels: map[string]string{labelSandbox: "true", labelSession: runID}, Annotations: map[string]string{shadowSourceNamespaceAnnotation: "payments"}}}
	clientset := corefake.NewSimpleClientset(ns)
	client := &kube.TypedClient{Clientset: clientset}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := deleteGraphNamespace(ctx, client, ns); err != nil {
		t.Fatalf("cancellation must not prevent cleanup: %v", err)
	}
	if _, err := client.GetNamespace(context.Background(), ns.Name); err == nil {
		t.Fatal("namespace still exists after cleanup")
	}
}

func TestGraphCleanupRefusesChangedNamespaceUID(t *testing.T) {
	runID := graphTestRunID
	expected := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: shadowNamespaceName(runID), UID: "expected-uid", Labels: map[string]string{labelSandbox: "true", labelSession: runID}, Annotations: map[string]string{shadowSourceNamespaceAnnotation: "payments"}}}
	actual := expected.DeepCopy()
	actual.UID = "replacement-uid"
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(actual)}
	if err := deleteGraphNamespace(context.Background(), client, expected); err == nil {
		t.Fatal("changed namespace UID must prevent deletion")
	}
	if _, err := client.GetNamespace(context.Background(), actual.Name); err != nil {
		t.Fatal("replacement namespace was deleted")
	}
}

func TestGraphNamespaceInventoryRejectsUnexpectedPod(t *testing.T) {
	labels := map[string]string{labelSandbox: "true", labelSession: graphTestRunID, labelGraph: graphTestRunID}
	clientset := corefake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "candidate", Namespace: "fixora-shadow-run", UID: "pod-uid", Labels: labels}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: "fixora-shadow-run", UID: "other-uid", Labels: labels}},
	)
	client := &kube.TypedClient{Clientset: clientset}
	workloads := []GraphWorkloadResult{{Pod: "candidate", PodUID: "pod-uid"}}
	if err := verifyGraphNamespaceInventory(context.Background(), client, "fixora-shadow-run", graphTestRunID, workloads, nil, nil); err == nil {
		t.Fatal("unexpected pod must block graph certification")
	}
}

func TestRunGraphRoutesServiceOnlyToShadowBackend(t *testing.T) {
	backend := graphRunWorkload("api", map[string]string{"app": "api", "team": "payments"})
	backend.Object["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)["labels"] = map[string]any{"app": "api", "team": "payments"}
	other := graphRunWorkload("worker", map[string]string{"app": "worker"})
	service := GraphNode{APIVersion: "v1", Kind: "Service", Namespace: "payments", Name: "api", UID: "svc-uid", ResourceVersion: "9", Object: map[string]any{"spec": map[string]any{"type": "ClusterIP", "clusterIP": "10.0.0.10", "selector": map[string]any{"app": "api"}, "ports": []any{map[string]any{"port": 8080, "targetPort": 8080, "protocol": "TCP"}}}}}
	graph := DependencyGraph{Nodes: []GraphNode{backend, other, service}, Edges: []GraphEdge{{From: service.ID(), To: backend.ID(), Type: GraphServiceEdge}}}
	clone, err := shadowService(service, "fixora-shadow-run", graphTestRunID, graph)
	if err != nil {
		t.Fatal(err)
	}
	if clone.Spec.Selector[graphServiceLabel(service)] != graphTestRunID || len(clone.Spec.Selector) != 1 {
		t.Fatalf("service selector can match production or unrelated Pods: %#v", clone.Spec.Selector)
	}
	backendPod, _, err := graphWorkloadPod(backend, graphTestRunID, "fixora-shadow-run", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	backendPod.Labels[graphServiceLabel(service)] = graphTestRunID
	otherPod, _, err := graphWorkloadPod(other, graphTestRunID, "fixora-shadow-run", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !graphSelectorMatches(anyLabels(clone.Spec.Selector), labelsAsAny(backendPod.Labels)) || graphSelectorMatches(anyLabels(clone.Spec.Selector), labelsAsAny(otherPod.Labels)) {
		t.Fatal("Service did not select exactly the graph backend")
	}
}

func TestValidateGraphSourceFreshnessRejectsDrift(t *testing.T) {
	workload := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "payments", Name: "api", UID: "current-uid", ResourceVersion: "8"}}
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(workload)}
	node := graphRunWorkload("api", map[string]string{"app": "api"})
	if err := validateGraphSourceFreshness(context.Background(), client, DependencyGraph{Nodes: []GraphNode{node}}); err == nil {
		t.Fatal("expected drift to block")
	}
}

func TestGraphServiceEndpointRequiresExactShadowPodUID(t *testing.T) {
	serviceNode := GraphNode{APIVersion: "v1", Kind: "Service", Namespace: "payments", Name: "api", UID: "service-source", ResourceVersion: "4"}
	backend := graphRunWorkload("api", map[string]string{"app": "api"})
	endpointReady := true
	portName, endpointPort := "http", int32(8080)
	tcp := corev1.ProtocolTCP
	clientset := corefake.NewSimpleClientset(
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "fixora-shadow-run"}, Spec: corev1.ServiceSpec{Selector: map[string]string{graphServiceLabel(serviceNode): graphTestRunID}, Ports: []corev1.ServicePort{{Name: portName, Port: 8080, TargetPort: intstr.FromInt(8080), Protocol: tcp}}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-fixora-12345678", Namespace: "fixora-shadow-run", UID: "pod-uid"}},
		&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "api-abc", Namespace: "fixora-shadow-run", Labels: map[string]string{"kubernetes.io/service-name": "api"}}, Ports: []discoveryv1.EndpointPort{{Name: &portName, Port: &endpointPort, Protocol: &tcp}}, Endpoints: []discoveryv1.Endpoint{{TargetRef: &corev1.ObjectReference{Kind: "Pod", Namespace: "fixora-shadow-run", Name: "api-fixora-12345678", UID: types.UID("pod-uid")}, Conditions: discoveryv1.EndpointConditions{Ready: &endpointReady}}}},
	)
	client := &kube.TypedClient{Clientset: clientset}
	graph := DependencyGraph{Nodes: []GraphNode{serviceNode, backend}, Edges: []GraphEdge{{From: serviceNode.ID(), To: backend.ID(), Type: GraphServiceEdge}}}
	workloads := []GraphWorkloadResult{{Source: backend.ID(), Pod: "api-fixora-12345678", PodUID: "pod-uid", Verified: true}}
	if err := verifyGraphServiceEndpoints(context.Background(), client, "fixora-shadow-run", graphTestRunID, graph, workloads, time.Second); err != nil {
		t.Fatalf("exact endpoint should pass: %v", err)
	}
	slice, err := clientset.DiscoveryV1().EndpointSlices("fixora-shadow-run").Get(context.Background(), "api-abc", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wrongPort := int32(9090)
	slice.Ports[0].Port = &wrongPort
	if _, err = clientset.DiscoveryV1().EndpointSlices("fixora-shadow-run").Update(context.Background(), slice, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := verifyGraphServiceEndpoints(context.Background(), client, "fixora-shadow-run", graphTestRunID, graph, workloads, 5*time.Millisecond); err == nil {
		t.Fatal("mismatched Service target port must not pass")
	}
	slice.Ports[0].Port = &endpointPort
	if _, err = clientset.DiscoveryV1().EndpointSlices("fixora-shadow-run").Update(context.Background(), slice, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	workloads[0].PodUID = "different-uid"
	if err := verifyGraphServiceEndpoints(context.Background(), client, "fixora-shadow-run", graphTestRunID, graph, workloads, 5*time.Millisecond); err == nil {
		t.Fatal("endpoint with non-matching Pod UID must not pass")
	}
}

func labelsAsAny(source map[string]string) map[string]any {
	result := map[string]any{}
	for k, v := range source {
		result[k] = v
	}
	return result
}
