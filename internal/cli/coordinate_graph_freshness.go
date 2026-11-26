package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/coordinate"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func bindCoordinateGraphCandidates(graph shadow.DependencyGraph, steps []coordinate.Step) (shadow.DependencyGraph, error) {
	current := graph
	current.Nodes = append([]shadow.GraphNode(nil), graph.Nodes...)
	candidates := map[string]string{}
	for _, step := range steps {
		key := step.Namespace + "/" + step.Kind + "/" + step.Name
		if _, exists := candidates[key]; exists {
			return shadow.DependencyGraph{}, fmt.Errorf("duplicate candidate for %s", step.Ref)
		}
		patch := step.Plan.PatchYAML()
		if strings.TrimSpace(patch) == "" {
			return shadow.DependencyGraph{}, fmt.Errorf("candidate patch for %s is empty", step.Ref)
		}
		candidates[key] = patch
	}
	used := map[string]bool{}
	for i := range current.Nodes {
		node := &current.Nodes[i]
		if node.Kind != "Deployment" && node.Kind != "StatefulSet" && node.Kind != "DaemonSet" {
			continue
		}
		key := node.Namespace + "/" + node.Kind + "/" + node.Name
		patch, ok := candidates[key]
		if !ok {
			return shadow.DependencyGraph{}, fmt.Errorf("candidate is missing for graph workload %s", node.ID())
		}
		node.CandidatePatch = patch
		used[key] = true
	}
	if len(used) != len(candidates) {
		return shadow.DependencyGraph{}, fmt.Errorf("candidate set differs from dependency graph")
	}
	return current, nil
}

// refreshCoordinateGraphSnapshot re-reads exact graph source identities just
// before the guarded apply boundary. Candidate bytes come from the certified
// in-memory graph and are compared by GraphCertificate.Check.
func refreshCoordinateGraphSnapshot(ctx context.Context, client *kube.TypedClient, graph shadow.DependencyGraph, authorities ...*shadow.GraphStorageAuthority) (coordinate.GraphSnapshot, error) {
	if client == nil || client.Clientset == nil {
		return coordinate.GraphSnapshot{}, fmt.Errorf("typed Kubernetes client is required for graph freshness checks")
	}
	current := graph
	current.Nodes = append([]shadow.GraphNode(nil), graph.Nodes...)
	for i := range current.Nodes {
		node := &current.Nodes[i]
		var uid, rv string
		switch node.Kind {
		case "Deployment":
			obj, err := client.Clientset.AppsV1().Deployments(node.Namespace).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return coordinate.GraphSnapshot{}, fmt.Errorf("re-read graph workload %s failed", node.Name)
			}
			uid, rv = string(obj.UID), obj.ResourceVersion
		case "StatefulSet":
			obj, err := client.Clientset.AppsV1().StatefulSets(node.Namespace).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return coordinate.GraphSnapshot{}, fmt.Errorf("re-read graph workload %s failed", node.Name)
			}
			uid, rv = string(obj.UID), obj.ResourceVersion
		case "DaemonSet":
			obj, err := client.Clientset.AppsV1().DaemonSets(node.Namespace).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return coordinate.GraphSnapshot{}, fmt.Errorf("re-read graph workload %s failed", node.Name)
			}
			uid, rv = string(obj.UID), obj.ResourceVersion
		case "Service":
			obj, err := client.Clientset.CoreV1().Services(node.Namespace).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return coordinate.GraphSnapshot{}, fmt.Errorf("re-read graph Service %s failed", node.Name)
			}
			uid, rv = string(obj.UID), obj.ResourceVersion
		case "ConfigMap":
			obj, err := client.Clientset.CoreV1().ConfigMaps(node.Namespace).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return coordinate.GraphSnapshot{}, fmt.Errorf("re-read graph ConfigMap %s failed", node.Name)
			}
			uid, rv = string(obj.UID), obj.ResourceVersion
		case "Secret":
			obj, err := client.Clientset.CoreV1().Secrets(node.Namespace).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return coordinate.GraphSnapshot{}, fmt.Errorf("re-read graph Secret %s failed", node.Name)
			}
			uid, rv = string(obj.UID), obj.ResourceVersion
		case "PersistentVolumeClaim":
			if client.Dynamic == nil || len(authorities) == 0 || authorities[0] == nil || node.Storage == nil {
				return coordinate.GraphSnapshot{}, fmt.Errorf("storage authority is required to refresh graph PVC %s", node.Name)
			}
			if err := authorities[0].CheckFresh(); err != nil || authorities[0].Digest() != node.Storage.StoragePolicyDigest {
				return coordinate.GraphSnapshot{}, fmt.Errorf("storage policy changed after graph verification")
			}
			pvc, err := client.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(node.Namespace).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil || string(pvc.GetUID()) != node.UID || pvc.GetResourceVersion() != node.ResourceVersion {
				return coordinate.GraphSnapshot{}, fmt.Errorf("re-read graph PVC %s failed or changed", node.Name)
			}
			checks := []struct {
				gvr           schema.GroupVersionResource
				name, uid, rv string
			}{
				{schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}, node.Storage.PVName, node.Storage.PVUID, node.Storage.PVResourceVersion},
				{schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}, node.Storage.StorageClassName, node.Storage.StorageClassUID, node.Storage.StorageClassResourceVersion},
				{schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotclasses"}, node.Storage.VolumeSnapshotClassName, node.Storage.VolumeSnapshotClassUID, node.Storage.VolumeSnapshotClassResourceVersion},
				{schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}, node.Storage.RestoreStorageClassName, node.Storage.RestoreStorageClassUID, node.Storage.RestoreStorageClassResourceVersion},
			}
			for _, check := range checks {
				obj, err := client.Dynamic.Resource(check.gvr).Get(ctx, check.name, metav1.GetOptions{})
				if err != nil || string(obj.GetUID()) != check.uid || obj.GetResourceVersion() != check.rv {
					return coordinate.GraphSnapshot{}, fmt.Errorf("storage source identity changed after graph verification")
				}
			}
			uid, rv = string(pvc.GetUID()), pvc.GetResourceVersion()
		default:
			return coordinate.GraphSnapshot{}, fmt.Errorf("unsupported graph source kind %s", node.Kind)
		}
		node.UID, node.ResourceVersion = uid, rv
	}
	return coordinate.GraphSourceSnapshot(current)
}
