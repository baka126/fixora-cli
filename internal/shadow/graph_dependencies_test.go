package shadow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corefake "k8s.io/client-go/kubernetes/fake"
)

func graphDependencyFixture() DependencyGraph {
	root := graphDeployment("api", "shared-config", "shared-secret")
	worker := graphDeployment("worker", "shared-config", "shared-secret")
	config := GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: "payments", Name: "shared-config", UID: "cm-uid", ResourceVersion: "4"}
	secret := GraphNode{APIVersion: "v1", Kind: "Secret", Namespace: "payments", Name: "shared-secret", UID: "secret-uid", ResourceVersion: "5"}
	return DependencyGraph{
		Root:  root.ID(),
		Nodes: []GraphNode{root, worker, config, secret},
		Edges: []GraphEdge{
			{From: root.ID(), To: config.ID(), Type: GraphConfigMapEdge},
			{From: root.ID(), To: secret.ID(), Type: GraphSecretEdge},
			{From: worker.ID(), To: config.ID(), Type: GraphConfigMapEdge},
			{From: worker.ID(), To: secret.ID(), Type: GraphSecretEdge},
		},
	}
}

func graphDependencyClient(configMap *corev1.ConfigMap, secret *corev1.Secret) *kube.TypedClient {
	return &kube.TypedClient{Clientset: corefake.NewSimpleClientset(configMap, secret)}
}

func TestSnapshotGraphDependenciesRejectsSourceDrift(t *testing.T) {
	graph := graphDependencyFixture()
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "shared-config", Namespace: "payments", UID: "cm-uid", ResourceVersion: "changed"}, Data: map[string]string{"mode": "safe"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "shared-secret", Namespace: "payments", UID: "secret-uid", ResourceVersion: "5"}, Data: map[string][]byte{"token": []byte("secret-value")}}
	if _, err := SnapshotGraphDependencies(context.Background(), graphDependencyClient(config, secret), graph); err == nil {
		t.Fatal("source resourceVersion drift must refuse the snapshot")
	}
}

func TestCreateGraphDependenciesCopiesOnlyReferencedObjects(t *testing.T) {
	const sentinel = "do-not-serialize-secret-sentinel"
	graph := graphDependencyFixture()
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "shared-config", Namespace: "payments", UID: "cm-uid", ResourceVersion: "4"}, Data: map[string]string{"mode": "safe"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "shared-secret", Namespace: "payments", UID: "secret-uid", ResourceVersion: "5"}, Data: map[string][]byte{"token": []byte(sentinel)}}
	extra := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unreferenced", Namespace: "payments"}, Data: map[string][]byte{"token": []byte("extra")}}
	clientset := corefake.NewSimpleClientset(config, secret, extra)
	client := &kube.TypedClient{Clientset: clientset}
	snapshot, err := SnapshotGraphDependencies(context.Background(), client, graph)
	if err != nil {
		t.Fatal(err)
	}
	created, err := CreateGraphDependencies(context.Background(), client, "fixora-shadow-test", "run-123", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Dependencies) != 2 {
		t.Fatalf("expected exactly two referenced dependencies, got %#v", created)
	}
	if _, err = clientset.CoreV1().Secrets("fixora-shadow-test").Get(context.Background(), "unreferenced", metav1.GetOptions{}); err == nil {
		t.Fatal("unreferenced Secret must not be copied")
	}
	shadowSecret, err := clientset.CoreV1().Secrets("fixora-shadow-test").Get(context.Background(), "shared-secret", metav1.GetOptions{})
	if err != nil || string(shadowSecret.Data["token"]) != sentinel || shadowSecret.Labels[labelSession] != "run-123" {
		t.Fatalf("referenced Secret was not copied with run identity: secret=%#v err=%v", shadowSecret, err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("snapshot serialization leaked Secret data: %s", encoded)
	}
}

func TestGraphSecretSentinelNeverAppearsInOutputOrErrors(t *testing.T) {
	const sentinel = "private-value-must-not-leak"
	graph := graphDependencyFixture()
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "shared-config", Namespace: "payments", UID: "cm-uid", ResourceVersion: "4"}, Data: map[string]string{"mode": "safe"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "shared-secret", Namespace: "payments", UID: "secret-uid", ResourceVersion: "5"}, Data: map[string][]byte{"token": []byte(sentinel)}}
	snapshot, err := SnapshotGraphDependencies(context.Background(), graphDependencyClient(config, secret), graph)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("Secret sentinel leaked into serialized snapshot: %s", encoded)
	}
}

func TestGraphSecretSizeLimitRefusesBeforeCreate(t *testing.T) {
	graph := graphDependencyFixture()
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "shared-config", Namespace: "payments", UID: "cm-uid", ResourceVersion: "4"}, Data: map[string]string{"mode": "safe"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "shared-secret", Namespace: "payments", UID: "secret-uid", ResourceVersion: "5"}, Data: map[string][]byte{"token": make([]byte, graphSecretByteLimit+1)}}
	client := graphDependencyClient(config, secret)
	if _, err := SnapshotGraphDependencies(context.Background(), client, graph); err == nil {
		t.Fatal("oversized Secret must be refused before shadow objects are created")
	}
	items, err := client.Clientset.CoreV1().Secrets("fixora-shadow-test").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(items.Items) != 0 {
		t.Fatalf("snapshot validation must precede create, got %#v err=%v", items, err)
	}
}

func TestSnapshotGraphDependenciesRejectsMissingRequiredKey(t *testing.T) {
	graph := graphDependencyFixture()
	root := &graph.Nodes[0]
	container := root.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	container["env"] = append(container["env"].([]any), map[string]any{"name": "MODE", "valueFrom": map[string]any{
		"configMapKeyRef": map[string]any{"name": "shared-config", "key": "missing"},
	}})
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "shared-config", Namespace: "payments", UID: "cm-uid", ResourceVersion: "4"}, Data: map[string]string{"mode": "safe"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "shared-secret", Namespace: "payments", UID: "secret-uid", ResourceVersion: "5"}, Data: map[string][]byte{"token": []byte("secret-value")}}
	if _, err := SnapshotGraphDependencies(context.Background(), graphDependencyClient(config, secret), graph); err == nil || !strings.Contains(err.Error(), "required key missing") {
		t.Fatalf("missing required key must be refused, got %v", err)
	}
}

func TestSnapshotGraphDependenciesRejectsTypeDrift(t *testing.T) {
	graph := graphDependencyFixture()
	for i := range graph.Nodes {
		if graph.Nodes[i].Kind == "Secret" {
			graph.Nodes[i].Object = map[string]any{"type": "kubernetes.io/tls"}
		}
	}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "shared-config", Namespace: "payments", UID: "cm-uid", ResourceVersion: "4"}, Data: map[string]string{"mode": "safe"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "shared-secret", Namespace: "payments", UID: "secret-uid", ResourceVersion: "5"}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"token": []byte("secret-value")}}
	if _, err := SnapshotGraphDependencies(context.Background(), graphDependencyClient(config, secret), graph); err == nil || !strings.Contains(err.Error(), "type or immutability changed") {
		t.Fatalf("Secret type drift must be refused, got %v", err)
	}
}
