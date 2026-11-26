package cli

import (
	"testing"

	"github.com/fixora/kubectl-fixora/internal/shadow"
)

func discoveryWorkload(name string, labels map[string]any, config string) shadow.GraphNode {
	return shadow.GraphNode{
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: "payments", Name: name,
		UID: "uid-" + name, ResourceVersion: "1",
		Object: map[string]any{
			"spec": map[string]any{"template": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec": map[string]any{"containers": []any{map[string]any{
					"name": "app", "image": "example/app:1",
					"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": config}}},
				}}, "volumes": []any{}},
			}},
		},
	}
}

func TestExpandCoordinateGraphFindsSharedDependencyWorkloads(t *testing.T) {
	root := discoveryWorkload("api", map[string]any{"app": "api"}, "shared")
	worker := discoveryWorkload("worker", map[string]any{"app": "worker"}, "shared")
	unrelated := discoveryWorkload("batch", map[string]any{"app": "batch"}, "other")
	selected := expandCoordinateGraphWorkloads(root.ID(), []shadow.GraphNode{root, worker, unrelated}, nil)
	if len(selected.Workloads) != 2 || selected.Workloads[0].Name != "api" || selected.Workloads[1].Name != "worker" {
		t.Fatalf("shared dependency closure mismatch: %#v", selected.Workloads)
	}
}

func TestExpandCoordinateGraphFindsEveryServiceBackend(t *testing.T) {
	root := discoveryWorkload("api", map[string]any{"tier": "backend"}, "root-config")
	worker := discoveryWorkload("worker", map[string]any{"tier": "backend"}, "worker-config")
	unrelated := discoveryWorkload("web", map[string]any{"tier": "frontend"}, "web-config")
	service := shadow.GraphNode{
		APIVersion: "v1", Kind: "Service", Namespace: "payments", Name: "backend",
		UID: "svc-uid", ResourceVersion: "1",
		Object: map[string]any{"spec": map[string]any{"selector": map[string]any{"tier": "backend"}}},
	}
	selected := expandCoordinateGraphWorkloads(root.ID(), []shadow.GraphNode{root, worker, unrelated}, []shadow.GraphNode{service})
	if len(selected.Workloads) != 2 || len(selected.Services) != 1 {
		t.Fatalf("service backend closure mismatch: %#v", selected)
	}
}
