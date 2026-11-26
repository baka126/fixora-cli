package cli

import (
	"fmt"
	"io"
	"sort"

	"github.com/fixora/kubectl-fixora/internal/coordinate"
	"github.com/fixora/kubectl-fixora/internal/output"
	"github.com/fixora/kubectl-fixora/internal/redact"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

type coordinateGraphPreview struct {
	Root                       string                       `json:"root"`
	Nodes                      []coordinateGraphPreviewNode `json:"nodes"`
	Edges                      []shadow.GraphEdge           `json:"edges"`
	Blockers                   []string                     `json:"blockers,omitempty"`
	ShadowVerified             bool                         `json:"shadowVerified"`
	LiveVerified               bool                         `json:"liveVerified"`
	ExternalDependenciesTested bool                         `json:"externalDependenciesTested"`
	Cleanup                    string                       `json:"cleanup"`
	StoragePlan                []coordinateGraphStoragePlan `json:"storagePlan,omitempty"`
	StoragePrerequisites       []string                     `json:"storagePrerequisites,omitempty"`
	StorageWrites              []string                     `json:"storageWrites,omitempty"`
}

type coordinateGraphStoragePlan struct {
	PVC                 string `json:"pvc"`
	SourceStorageClass  string `json:"sourceStorageClass"`
	SnapshotClass       string `json:"snapshotClass"`
	RestoreStorageClass string `json:"restoreStorageClass"`
	RequestedBytes      int64  `json:"requestedBytes"`
	MaximumBytes        int64  `json:"maximumBytes"`
}

type coordinateGraphPreviewNode struct {
	ID              string   `json:"id"`
	Role            string   `json:"role"`
	UID             string   `json:"uid,omitempty"`
	ResourceVersion string   `json:"resourceVersion,omitempty"`
	Eligibility     string   `json:"eligibility,omitempty"`
	Strategy        string   `json:"strategy,omitempty"`
	Reasons         []string `json:"reasons,omitempty"`
}

func buildCoordinateGraphPreview(graph shadow.DependencyGraph, steps []coordinate.Step) coordinateGraphPreview {
	stepByID := make(map[string]coordinate.Step, len(steps))
	for _, step := range steps {
		stepByID[step.Kind+"/"+step.Namespace+"/"+step.Name] = step
	}
	preview := coordinateGraphPreview{
		Root: graph.Root, Edges: append([]shadow.GraphEdge(nil), graph.Edges...),
		Blockers: append([]string(nil), graph.Blockers...),
		Cleanup:  "not-started",
	}
	for _, node := range graph.Nodes {
		item := coordinateGraphPreviewNode{ID: node.ID(), UID: node.UID, ResourceVersion: node.ResourceVersion, Role: "sandbox dependency"}
		switch node.Kind {
		case "Deployment", "StatefulSet", "DaemonSet":
			item.Role = "workload"
			if step, ok := stepByID[node.ID()]; ok {
				item.Strategy = step.Plan.Strategy
				if step.Plan.ApplyEligible {
					item.Eligibility = "apply-eligible candidate"
				} else {
					item.Eligibility = "review required"
					for _, reason := range step.Plan.BlockedReasons {
						item.Reasons = append(item.Reasons, redact.Text(reason))
					}
				}
			} else {
				item.Eligibility = "runtime dependency"
			}
		case "Secret":
			item.Role = "ephemeral Secret input"
		case "Service":
			item.Role = "isolated Service route"
		case "PersistentVolumeClaim":
			item.Role = "authorized CSI snapshot source"
			if node.Storage != nil {
				preview.StoragePlan = append(preview.StoragePlan, coordinateGraphStoragePlan{PVC: node.ID(), SourceStorageClass: node.Storage.StorageClassName, SnapshotClass: node.Storage.VolumeSnapshotClassName, RestoreStorageClass: node.Storage.RestoreStorageClassName, RequestedBytes: node.Storage.RequestedBytes, MaximumBytes: node.Storage.MaxLogicalBytes})
				preview.StoragePrerequisites = []string{"VolumeSnapshot CRDs and CSI snapshot controller must be installed", "AnyVolumeDataSource and CrossNamespaceVolumeDataSource alpha gates must be enabled on the API server and kube-controller-manager; CSI provisioner needs CrossNamespaceVolumeDataSource", "Gateway API ReferenceGrant CRD and compatible CSI snapshot/restore support are required", "PVC snapshots are crash-consistent; application quiescing is not performed"}
				preview.StorageWrites = []string{"source namespace: temporary VolumeSnapshot and exact ReferenceGrant", "per-run namespace: restored PVCs and shadow Pods"}
			}
		}
		preview.Nodes = append(preview.Nodes, item)
	}
	sort.Slice(preview.Nodes, func(i, j int) bool { return preview.Nodes[i].ID < preview.Nodes[j].ID })
	sort.Strings(preview.Blockers)
	return preview
}

func writeCoordinateGraphPreview(w io.Writer, format string, preview coordinateGraphPreview) int {
	if format != "" && format != "text" {
		return output.Write(w, format, preview)
	}
	fmt.Fprintln(w, "Combined dependency graph preview")
	fmt.Fprintln(w, "==================================")
	fmt.Fprintf(w, "Root: %s\n", preview.Root)
	fmt.Fprintln(w, "Resources")
	for _, node := range preview.Nodes {
		fmt.Fprintf(w, "  %-48s %-28s", node.ID, node.Role)
		if node.Eligibility != "" {
			fmt.Fprintf(w, " %s", node.Eligibility)
		}
		if node.Strategy != "" {
			fmt.Fprintf(w, " (%s)", node.Strategy)
		}
		fmt.Fprintln(w)
		for _, reason := range node.Reasons {
			fmt.Fprintf(w, "    - %s\n", reason)
		}
	}
	if len(preview.Edges) > 0 {
		fmt.Fprintln(w, "Connections")
		for _, edge := range preview.Edges {
			fmt.Fprintf(w, "  %s --%s--> %s\n", edge.From, edge.Type, edge.To)
		}
	}
	if len(preview.StoragePlan) > 0 {
		fmt.Fprintln(w, "CSI snapshot plan (no writes in preview)")
		for _, item := range preview.StoragePlan {
			fmt.Fprintf(w, "  %s: %s → snapshot %s → restore %s (%d bytes requested; %d byte policy maximum)\n", item.PVC, item.SourceStorageClass, item.SnapshotClass, item.RestoreStorageClass, item.RequestedBytes, item.MaximumBytes)
		}
		fmt.Fprintln(w, "Prerequisites")
		for _, prerequisite := range preview.StoragePrerequisites {
			fmt.Fprintf(w, "  - %s\n", prerequisite)
		}
		fmt.Fprintln(w, "Planned writes after explicit storage confirmation")
		for _, write := range preview.StorageWrites {
			fmt.Fprintf(w, "  - %s\n", write)
		}
	}
	if len(preview.Blockers) > 0 {
		fmt.Fprintln(w, "Cannot verify this graph")
		for _, blocker := range preview.Blockers {
			fmt.Fprintf(w, "  - %s\n", redact.Text(blocker))
		}
	}
	fmt.Fprintln(w, "Shadow verification: not run")
	fmt.Fprintln(w, "Live verification: not run; no changes made")
	return 0
}
