package shadow

import (
	"context"
	"fmt"
	"reflect"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DependencySnapshot struct {
	Dependencies []GraphNode `json:"dependencies"`
	HasSecrets   bool        `json:"hasSecrets"`
	configMaps   []*corev1.ConfigMap
	secrets      []*corev1.Secret
}

type CreatedDependencies struct {
	Dependencies []GraphNode `json:"dependencies"`
}

// SnapshotGraphDependencies fetches only dependency nodes present in the
// already bounded graph, and binds their contents to the captured source UID
// and resourceVersion. The data-bearing copies are private fields so JSON
// serialization can expose metadata without exposing values.
func SnapshotGraphDependencies(ctx context.Context, client *kube.TypedClient, graph DependencyGraph) (DependencySnapshot, error) {
	if client == nil || client.Clientset == nil {
		return DependencySnapshot{}, fmt.Errorf("typed Kubernetes client is required for dependency snapshots")
	}
	snapshot := DependencySnapshot{}
	configKeyCount, configByteCount, secretByteCount := 0, 0, 0
	for _, node := range graph.Nodes {
		switch node.Kind {
		case "ConfigMap":
			current, err := client.Clientset.CoreV1().ConfigMaps(node.Namespace).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return DependencySnapshot{}, fmt.Errorf("read referenced ConfigMap %s: %w", node.Name, err)
			}
			if string(current.UID) != node.UID || current.ResourceVersion != node.ResourceVersion {
				return DependencySnapshot{}, fmt.Errorf("referenced ConfigMap %s changed after graph discovery", node.Name)
			}
			if !matchesGraphDependencyShape(node, "", current.Immutable) {
				return DependencySnapshot{}, fmt.Errorf("referenced ConfigMap %s type or immutability changed after graph discovery", node.Name)
			}
			if err := validateGraphDependencyKeys(graph, node, current.Data, current.BinaryData, nil); err != nil {
				return DependencySnapshot{}, err
			}
			configKeyCount += len(current.Data) + len(current.BinaryData)
			for _, value := range current.Data {
				configByteCount += len(value)
			}
			for _, value := range current.BinaryData {
				configByteCount += len(value)
			}
			if configKeyCount > graphConfigMapKeyLimit {
				return DependencySnapshot{}, fmt.Errorf("referenced ConfigMaps exceed the %d-key shadow limit", graphConfigMapKeyLimit)
			}
			if configByteCount > graphConfigMapByteLimit {
				return DependencySnapshot{}, fmt.Errorf("referenced ConfigMaps exceed the %d-byte shadow limit", graphConfigMapByteLimit)
			}
			copy := current.DeepCopy()
			snapshot.configMaps = append(snapshot.configMaps, copy)
			snapshot.Dependencies = append(snapshot.Dependencies, graphDependencyIdentity(node))
		case "Secret":
			current, err := client.Clientset.CoreV1().Secrets(node.Namespace).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					return DependencySnapshot{}, fmt.Errorf("referenced Secret %s is missing", node.Name)
				}
				return DependencySnapshot{}, fmt.Errorf("read referenced Secret %s failed", node.Name)
			}
			if string(current.UID) != node.UID || current.ResourceVersion != node.ResourceVersion {
				return DependencySnapshot{}, fmt.Errorf("referenced Secret %s changed after graph discovery", node.Name)
			}
			if !matchesGraphDependencyShape(node, string(current.Type), current.Immutable) {
				return DependencySnapshot{}, fmt.Errorf("referenced Secret %s type or immutability changed after graph discovery", node.Name)
			}
			if err := validateGraphDependencyKeys(graph, node, nil, nil, current.Data); err != nil {
				return DependencySnapshot{}, err
			}
			for _, value := range current.Data {
				secretByteCount += len(value)
			}
			if secretByteCount > graphSecretByteLimit {
				return DependencySnapshot{}, fmt.Errorf("referenced Secrets exceed the %d-byte shadow limit", graphSecretByteLimit)
			}
			copy := current.DeepCopy()
			snapshot.secrets = append(snapshot.secrets, copy)
			snapshot.HasSecrets = true
			snapshot.Dependencies = append(snapshot.Dependencies, graphDependencyIdentity(node))
		}
	}
	return snapshot, nil
}

// CreateGraphDependencies copies the already snapshotted dependencies into
// the run namespace with no source labels, annotations, owner references, or
// server metadata. API errors for Secret creation are deliberately sanitized.
func CreateGraphDependencies(ctx context.Context, client *kube.TypedClient, namespace, runID string, snapshot DependencySnapshot) (CreatedDependencies, error) {
	if client == nil || client.Clientset == nil || namespace == "" || runID == "" {
		return CreatedDependencies{}, fmt.Errorf("typed client, shadow namespace, and run identity are required")
	}
	created := CreatedDependencies{}
	labels := map[string]string{labelSandbox: "true", labelSession: runID}
	for _, source := range snapshot.configMaps {
		copy := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: source.Name, Namespace: namespace, Labels: cloneLabels(labels)},
			Immutable:  cloneBool(source.Immutable),
			Data:       cloneStringMap(source.Data),
			BinaryData: cloneBytesMap(source.BinaryData),
		}
		result, err := client.Clientset.CoreV1().ConfigMaps(namespace).Create(ctx, copy, metav1.CreateOptions{})
		if err != nil {
			return created, fmt.Errorf("create isolated ConfigMap %s failed", source.Name)
		}
		if result == nil || result.Name != source.Name || result.Namespace != namespace || result.Labels[labelSandbox] != "true" || result.Labels[labelSession] != runID || !reflect.DeepEqual(result.Data, copy.Data) || !reflect.DeepEqual(result.BinaryData, copy.BinaryData) || !reflect.DeepEqual(result.Immutable, copy.Immutable) {
			return created, fmt.Errorf("isolated ConfigMap %s did not preserve the snapshotted data", source.Name)
		}
		created.Dependencies = append(created.Dependencies, GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: namespace, Name: result.Name, UID: string(result.UID), ResourceVersion: result.ResourceVersion})
	}
	for _, source := range snapshot.secrets {
		copy := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: source.Name, Namespace: namespace, Labels: cloneLabels(labels)},
			Immutable:  cloneBool(source.Immutable),
			Type:       source.Type,
			Data:       cloneBytesMap(source.Data),
		}
		result, err := client.Clientset.CoreV1().Secrets(namespace).Create(ctx, copy, metav1.CreateOptions{})
		if err != nil {
			return created, fmt.Errorf("create isolated Secret %s failed", source.Name)
		}
		if result == nil || result.Name != source.Name || result.Namespace != namespace || result.Labels[labelSandbox] != "true" || result.Labels[labelSession] != runID || result.Type != copy.Type || !reflect.DeepEqual(result.Data, copy.Data) || !reflect.DeepEqual(result.Immutable, copy.Immutable) {
			return created, fmt.Errorf("isolated Secret %s did not preserve the snapshotted data", source.Name)
		}
		created.Dependencies = append(created.Dependencies, GraphNode{APIVersion: "v1", Kind: "Secret", Namespace: namespace, Name: result.Name, UID: string(result.UID), ResourceVersion: result.ResourceVersion})
	}
	return created, nil
}

func graphDependencyIdentity(node GraphNode) GraphNode {
	return GraphNode{APIVersion: node.APIVersion, Kind: node.Kind, Namespace: node.Namespace, Name: node.Name, UID: node.UID, ResourceVersion: node.ResourceVersion}
}

func matchesGraphDependencyShape(node GraphNode, objectType string, immutable *bool) bool {
	if node.Object == nil {
		return true
	}
	if expected := graphString(node.Object, "type"); expected != "" && expected != objectType {
		return false
	}
	if expected, exists := node.Object["immutable"]; exists {
		want, _ := expected.(bool)
		got := immutable != nil && *immutable
		return want == got
	}
	return true
}

func validateGraphDependencyKeys(graph DependencyGraph, dependency GraphNode, configData map[string]string, binaryData map[string][]byte, secretData map[string][]byte) error {
	for _, workload := range graph.Nodes {
		if !graphWorkload(workload.Kind) {
			continue
		}
		refs, _ := graphWorkloadReferences(workload)
		for _, ref := range refs {
			if ref.Kind != dependency.Kind || ref.Name != dependency.Name || ref.Key == "" || ref.Key == "*" {
				continue
			}
			present := false
			switch dependency.Kind {
			case "ConfigMap":
				_, present = configData[ref.Key]
				if !present {
					_, present = binaryData[ref.Key]
				}
			case "Secret":
				_, present = secretData[ref.Key]
			}
			if !present && !ref.Optional {
				return fmt.Errorf("required key %s is missing from referenced %s %s", ref.Key, dependency.Kind, dependency.Name)
			}
		}
	}
	return nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	copy := make(map[string]string, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

func cloneBytesMap(source map[string][]byte) map[string][]byte {
	if source == nil {
		return nil
	}
	copy := make(map[string][]byte, len(source))
	for key, value := range source {
		copy[key] = append([]byte(nil), value...)
	}
	return copy
}

func cloneBool(source *bool) *bool {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func cloneLabels(source map[string]string) map[string]string { return cloneStringMap(source) }
