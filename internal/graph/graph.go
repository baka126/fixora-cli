package graph

import (
	"context"
	"fmt"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
)

type Node struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Status    string `json:"status,omitempty"`
}

type Edge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

type Graph struct {
	Root  string `json:"root"`
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

func Build(ctx context.Context, k kube.Kubectl, f analyzer.Finding) Graph {
	g := Graph{Root: id(f.ResourceKind, f.ResourceName)}
	g.add(Node{ID: g.Root, Kind: f.ResourceKind, Name: f.ResourceName, Namespace: f.Namespace, Status: f.Status})
	if f.PodName != "" {
		podID := id("Pod", f.PodName)
		g.add(Node{ID: podID, Kind: "Pod", Name: f.PodName, Namespace: f.Namespace, Status: f.Status})
		g.edge(g.Root, podID, "owns")
	}
	for _, owner := range f.OwnerChain {
		parts := strings.SplitN(owner, "/", 2)
		if len(parts) != 2 {
			continue
		}
		ownerID := id(parts[0], parts[1])
		g.add(Node{ID: ownerID, Kind: parts[0], Name: parts[1], Namespace: f.Namespace})
		if ownerID != g.Root {
			g.edge(ownerID, g.Root, "owner-chain")
		}
	}
	services, _ := k.GetResourceItems(ctx, f.Namespace, false, "services")
	for _, svc := range services {
		name := metaName(svc)
		svcID := id("Service", name)
		g.add(Node{ID: svcID, Kind: "Service", Name: name, Namespace: f.Namespace})
		g.edge(svcID, g.Root, "selects")
	}
	pvcs, _ := k.GetResourceItems(ctx, f.Namespace, false, "pvc")
	for _, pvc := range pvcs {
		name := metaName(pvc)
		pvcID := id("PVC", name)
		g.add(Node{ID: pvcID, Kind: "PVC", Name: name, Namespace: f.Namespace, Status: compactStatus(pvc)})
		g.edge(g.Root, pvcID, "mounts")
	}
	if f.GitOps.HelmRelease != "" {
		helmID := id("HelmRelease", f.GitOps.HelmRelease)
		g.add(Node{ID: helmID, Kind: "HelmRelease", Name: f.GitOps.HelmRelease, Namespace: f.Namespace})
		g.edge(helmID, g.Root, "renders")
	}
	return g
}

func (g *Graph) add(n Node) {
	for _, existing := range g.Nodes {
		if existing.ID == n.ID {
			return
		}
	}
	g.Nodes = append(g.Nodes, n)
}

func (g *Graph) edge(from, to, reason string) {
	if from == "" || to == "" || from == to {
		return
	}
	for _, existing := range g.Edges {
		if existing.From == from && existing.To == to && existing.Reason == reason {
			return
		}
	}
	g.Edges = append(g.Edges, Edge{From: from, To: to, Reason: reason})
}

func Text(g Graph) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Dependency graph: %s\n", g.Root)
	for _, edge := range g.Edges {
		fmt.Fprintf(&b, "  %s --%s--> %s\n", edge.From, edge.Reason, edge.To)
	}
	if len(g.Edges) == 0 {
		fmt.Fprintln(&b, "  no related resources discovered")
	}
	return b.String()
}

func Mermaid(g Graph) string {
	var b strings.Builder
	fmt.Fprintln(&b, "graph TD")
	for _, node := range g.Nodes {
		fmt.Fprintf(&b, "  %s[\"%s/%s\"]\n", sanitize(node.ID), node.Kind, node.Name)
	}
	for _, edge := range g.Edges {
		fmt.Fprintf(&b, "  %s -->|%s| %s\n", sanitize(edge.From), edge.Reason, sanitize(edge.To))
	}
	return b.String()
}

func id(kind, name string) string {
	return kind + "/" + name
}

func sanitize(value string) string {
	value = strings.NewReplacer("/", "_", "-", "_", ".", "_", ":", "_").Replace(value)
	return value
}

func metaName(obj map[string]any) string {
	meta, _ := obj["metadata"].(map[string]any)
	return fmt.Sprint(meta["name"])
}

func compactStatus(obj map[string]any) string {
	status, _ := obj["status"].(map[string]any)
	phase, _ := status["phase"].(string)
	return phase
}
