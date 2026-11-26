package shadow

import (
	"fmt"
	"sort"
	"strings"
)

const (
	GraphNodeLimit          = 32
	graphDepthLimit         = 8
	graphRootEdgeLimit      = 8
	graphConfigMapKeyLimit  = 32
	graphConfigMapByteLimit = 64 * 1024
	graphSecretByteLimit    = 64 * 1024
)

type GraphNode struct {
	APIVersion      string              `json:"apiVersion"`
	Kind            string              `json:"kind"`
	Namespace       string              `json:"namespace"`
	Name            string              `json:"name"`
	UID             string              `json:"uid"`
	ResourceVersion string              `json:"resourceVersion"`
	CandidatePatch  string              `json:"-"`
	Object          map[string]any      `json:"-"`
	Storage         *GraphStorageSource `json:"storage,omitempty"`
}

// GraphStorageSource binds the read-only storage facts needed before a
// snapshot lifecycle can be considered. It intentionally excludes volume
// handles and storage contents.
type GraphStorageSource struct {
	PVName                             string   `json:"pvName"`
	PVUID                              string   `json:"pvUid"`
	PVResourceVersion                  string   `json:"pvResourceVersion"`
	StorageClassName                   string   `json:"storageClassName"`
	StorageClassUID                    string   `json:"storageClassUid"`
	StorageClassResourceVersion        string   `json:"storageClassResourceVersion"`
	CSIDriver                          string   `json:"csiDriver"`
	AccessModes                        []string `json:"accessModes"`
	VolumeMode                         string   `json:"volumeMode"`
	RequestedBytes                     int64    `json:"requestedBytes"`
	Bound                              bool     `json:"bound"`
	HasSelector                        bool     `json:"hasSelector"`
	HasDataSource                      bool     `json:"hasDataSource"`
	VolumeSnapshotClassName            string   `json:"volumeSnapshotClassName,omitempty"`
	RestoreStorageClassName            string   `json:"restoreStorageClassName,omitempty"`
	MaxLogicalBytes                    int64    `json:"maxLogicalBytes,omitempty"`
	VolumeSnapshotClassUID             string   `json:"volumeSnapshotClassUid,omitempty"`
	VolumeSnapshotClassResourceVersion string   `json:"volumeSnapshotClassResourceVersion,omitempty"`
	RestoreStorageClassUID             string   `json:"restoreStorageClassUid,omitempty"`
	RestoreStorageClassResourceVersion string   `json:"restoreStorageClassResourceVersion,omitempty"`
	StoragePolicyDigest                string   `json:"storagePolicyDigest,omitempty"`
}

// DigestString returns a deterministic metadata-only representation for graph
// freshness. It never includes PV handles or volume contents.
func (s *GraphStorageSource) DigestString() string {
	if s == nil {
		return ""
	}
	return strings.Join([]string{s.PVName, s.PVUID, s.PVResourceVersion, s.StorageClassName, s.StorageClassUID, s.StorageClassResourceVersion, s.CSIDriver, s.VolumeMode, fmt.Sprint(s.RequestedBytes), strings.Join(s.AccessModes, ","), s.VolumeSnapshotClassName, s.VolumeSnapshotClassUID, s.VolumeSnapshotClassResourceVersion, s.RestoreStorageClassName, s.RestoreStorageClassUID, s.RestoreStorageClassResourceVersion, s.StoragePolicyDigest}, "|")
}

func (n GraphNode) ID() string {
	return strings.Trim(strings.Join([]string{n.Kind, n.Namespace, n.Name}, "/"), "/")
}

type GraphEdgeType string

const (
	GraphConfigMapEdge GraphEdgeType = "configmap"
	GraphSecretEdge    GraphEdgeType = "secret"
	GraphServiceEdge   GraphEdgeType = "service"
	GraphPVCEdge       GraphEdgeType = "pvc"
)

type GraphEdge struct {
	From             string        `json:"from"`
	To               string        `json:"to"`
	Type             GraphEdgeType `json:"type"`
	SourceVolumeName string        `json:"sourceVolumeName,omitempty"`
}

type DependencyGraph struct {
	Root     string      `json:"root"`
	Nodes    []GraphNode `json:"nodes"`
	Edges    []GraphEdge `json:"edges"`
	Blockers []string    `json:"blockers,omitempty"`
}

type GraphReference struct {
	Name       string
	Kind       string
	Key        string
	Optional   bool
	VolumeName string
}

type GraphBuildOptions struct{ StoragePolicy *GraphStorageAuthority }

// BuildDependencyGraph assembles a stable, bounded graph from the complete set
// of same-namespace resource snapshots discovered by the caller. Nodes not
// reachable from root are not included. Unsafe or incomplete references become
// blockers so preview can explain why the graph is not certifiable.
func BuildDependencyGraph(root string, snapshots []GraphNode) (DependencyGraph, error) {
	return BuildDependencyGraphWithOptions(root, snapshots, GraphBuildOptions{})
}

// BuildDependencyGraphWithOptions builds a graph with explicitly authorized
// PVC source nodes. The default builder deliberately keeps PVCs blocked.
func BuildDependencyGraphWithOptions(root string, snapshots []GraphNode, options GraphBuildOptions) (DependencyGraph, error) {
	byID := make(map[string]GraphNode, len(snapshots))
	byName := make(map[string][]string, len(snapshots))
	for _, node := range snapshots {
		id := node.ID()
		if node.APIVersion == "" || node.Kind == "" || node.Namespace == "" || node.Name == "" {
			return DependencyGraph{}, fmt.Errorf("graph node identity is incomplete")
		}
		if _, exists := byID[id]; exists {
			return DependencyGraph{}, fmt.Errorf("duplicate graph node identity %s", id)
		}
		if node.UID == "" || node.ResourceVersion == "" {
			return DependencyGraph{}, fmt.Errorf("graph node %s has incomplete source identity", id)
		}
		byID[id] = node
		byName[node.Kind+"/"+node.Name] = append(byName[node.Kind+"/"+node.Name], id)
	}
	rootID, err := resolveGraphRoot(root, byID, byName)
	if err != nil {
		return DependencyGraph{}, err
	}
	rootNode := byID[rootID]
	graph := DependencyGraph{Root: rootID}
	nodeBlockers := map[string][]string{}
	addNodeBlocker := func(nodeID, message string) {
		nodeBlockers[nodeID] = appendUnique(nodeBlockers[nodeID], message)
	}
	adj := map[string]map[string]bool{}
	addEdge := func(from, to string, edgeType GraphEdgeType, volumeName ...string) {
		if from == "" || to == "" {
			return
		}
		if adj[from] == nil {
			adj[from] = map[string]bool{}
		}
		if adj[to] == nil {
			adj[to] = map[string]bool{}
		}
		adj[from][to], adj[to][from] = true, true
		sourceVolumeName := ""
		if len(volumeName) > 0 {
			sourceVolumeName = volumeName[0]
		}
		for _, edge := range graph.Edges {
			if edge.From == from && edge.To == to && edge.Type == edgeType && edge.SourceVolumeName == sourceVolumeName {
				return
			}
		}
		graph.Edges = append(graph.Edges, GraphEdge{From: from, To: to, Type: edgeType, SourceVolumeName: sourceVolumeName})
	}
	addBlocker := func(message string) { graph.Blockers = appendUnique(graph.Blockers, message) }

	for _, node := range snapshots {
		if node.Namespace != rootNode.Namespace {
			addNodeBlocker(node.ID(), fmt.Sprintf("cross-namespace graph node %s is unsupported", node.Kind+"/"+node.Name))
		}
		if !supportedGraphNode(node.Kind) && !(node.Kind == "PersistentVolumeClaim" && options.StoragePolicy != nil) {
			addNodeBlocker(node.ID(), fmt.Sprintf("unsupported graph resource %s", node.Kind+"/"+node.Name))
		}
	}

	// Workload references form edges to exact named dependencies. Optional
	// missing references are omitted; required missing references block the graph.
	for _, node := range snapshots {
		if !graphWorkload(node.Kind) {
			continue
		}
		refs, workloadBlockers := graphWorkloadReferences(node)
		for _, blocker := range workloadBlockers {
			if blocker == fmt.Sprintf("workload %s uses PersistentVolumeClaim storage outside combined shadow scope", node.Name) && options.StoragePolicy != nil {
				continue
			}
			addNodeBlocker(node.ID(), blocker)
		}
		for _, ref := range refs {
			id, ok := resolveGraphReference(node.Namespace, ref, byID, byName)
			if !ok {
				if !ref.Optional {
					if graphReferenceExistsOutsideNamespace(node.Namespace, ref, byName, byID) {
						addNodeBlocker(node.ID(), fmt.Sprintf("cross-namespace %s reference from %s is unsupported", ref.Kind, node.Kind+"/"+node.Name))
					} else {
						addNodeBlocker(node.ID(), fmt.Sprintf("required %s reference %s from %s is missing or ambiguous", ref.Kind, ref.Name, node.Kind+"/"+node.Name))
					}
				}
				continue
			}
			if ref.Kind == "PersistentVolumeClaim" {
				pvc := byID[id]
				if options.StoragePolicy == nil {
					addNodeBlocker(node.ID(), fmt.Sprintf("workload %s uses PersistentVolumeClaim storage outside combined shadow scope", node.Name))
					continue
				}
				volumeName := ref.VolumeName
				if !eligibleGraphPVC(pvc, volumeName) {
					addNodeBlocker(node.ID(), fmt.Sprintf("PVC %s is not a bound CSI RWO filesystem claim without selectors or existing data sources", pvc.Name))
					continue
				}
				rule, err := options.StoragePolicy.Authorize(node.Namespace, pvc.Name, pvc.Storage.RequestedBytes)
				if err != nil {
					addNodeBlocker(node.ID(), err.Error())
					continue
				}
				pvc.Storage.VolumeSnapshotClassName = rule.VolumeSnapshotClassName
				pvc.Storage.RestoreStorageClassName = rule.RestoreStorageClassName
				pvc.Storage.MaxLogicalBytes = rule.MaxLogicalBytes
			}
			edgeType := GraphConfigMapEdge
			switch ref.Kind {
			case "Secret":
				edgeType = GraphSecretEdge
			case "PersistentVolumeClaim":
				edgeType = GraphPVCEdge
			}
			if edgeType == GraphPVCEdge {
				addEdge(node.ID(), id, edgeType, ref.VolumeName)
			} else {
				addEdge(node.ID(), id, edgeType)
			}
		}
	}

	// A selector-based Service belongs in the graph only when it selects one
	// or more discovered workload templates. The caller supplies the namespace
	// inventory so selectors cannot silently point at an omitted backend.
	for _, service := range snapshots {
		if service.Kind != "Service" {
			continue
		}
		spec := graphMap(service.Object, "spec")
		serviceType := graphString(spec, "type")
		if serviceType != "" && serviceType != "ClusterIP" {
			addNodeBlocker(service.ID(), fmt.Sprintf("Service %s uses unsupported type %s", service.Name, serviceType))
		}
		if graphString(spec, "clusterIP") == "None" || graphBool(spec, "publishNotReadyAddresses") {
			addNodeBlocker(service.ID(), fmt.Sprintf("Service %s uses unsupported headless or not-ready endpoint behavior", service.Name))
		}
		ports := graphSlice(spec, "ports")
		if len(ports) == 0 {
			addNodeBlocker(service.ID(), fmt.Sprintf("Service %s has no ports", service.Name))
		}
		for _, rawPort := range ports {
			protocol := graphString(asGraphMap(rawPort), "protocol")
			if protocol != "" && protocol != "TCP" {
				addNodeBlocker(service.ID(), fmt.Sprintf("Service %s uses unsupported non-TCP ports", service.Name))
				break
			}
		}
		selector := graphMap(graphMap(service.Object, "spec"), "selector")
		if len(selector) == 0 {
			addNodeBlocker(service.ID(), fmt.Sprintf("selectorless Service %s is unsupported", service.Name))
			continue
		}
		matches := 0
		for _, workload := range snapshots {
			if !graphWorkload(workload.Kind) || workload.Namespace != service.Namespace {
				continue
			}
			labels := graphWorkloadLabels(workload)
			if graphSelectorMatches(selector, labels) {
				matches++
				addEdge(service.ID(), workload.ID(), GraphServiceEdge)
			}
		}
		if matches == 0 {
			addNodeBlocker(service.ID(), fmt.Sprintf("Service %s has no discovered workload backend", service.Name))
		}
	}

	// Only the connected component containing the selected root is eligible.
	connected := map[string]bool{rootID: true}
	queue := []string{rootID}
	depth := map[string]int{rootID: 0}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for next := range adj[current] {
			if connected[next] {
				continue
			}
			connected[next] = true
			depth[next] = depth[current] + 1
			if depth[next] > graphDepthLimit {
				addBlocker(fmt.Sprintf("dependency graph exceeds maximum depth of %d", graphDepthLimit))
			}
			queue = append(queue, next)
		}
	}
	for _, node := range snapshots {
		if connected[node.ID()] {
			graph.Nodes = append(graph.Nodes, node)
			for _, blocker := range nodeBlockers[node.ID()] {
				addBlocker(blocker)
			}
		}
	}
	if len(graph.Nodes) > GraphNodeLimit {
		addBlocker(fmt.Sprintf("dependency graph exceeds maximum of %d nodes", GraphNodeLimit))
	}
	if len(adj[rootID]) > graphRootEdgeLimit {
		addBlocker(fmt.Sprintf("dependency graph exceeds maximum of %d root edges", graphRootEdgeLimit))
	}
	configKeys, configBytes, secretBytes := graphDataSizes(graph.Nodes)
	if configKeys > graphConfigMapKeyLimit {
		addBlocker(fmt.Sprintf("dependency graph exceeds maximum of %d ConfigMap keys", graphConfigMapKeyLimit))
	}
	if configBytes > graphConfigMapByteLimit {
		addBlocker(fmt.Sprintf("dependency graph ConfigMap data exceeds %d bytes", graphConfigMapByteLimit))
	}
	if secretBytes > graphSecretByteLimit {
		addBlocker(fmt.Sprintf("dependency graph Secret data exceeds %d bytes", graphSecretByteLimit))
	}
	workloadCount := 0
	if options.StoragePolicy != nil {
		requested := map[string]int64{}
		for _, node := range graph.Nodes {
			if node.Kind == "PersistentVolumeClaim" && node.Storage != nil {
				requested[node.Name] = node.Storage.RequestedBytes
			}
		}
		if len(requested) > 0 {
			if _, err := options.StoragePolicy.AuthorizeGraph(rootNode.Namespace, requested); err != nil {
				addBlocker(err.Error())
			}
		}
	}
	for _, node := range graph.Nodes {
		if graphWorkload(node.Kind) {
			workloadCount++
		}
	}
	if workloadCount < 2 {
		addBlocker("combined shadow requires at least two connected supported workloads")
	}
	sort.Slice(graph.Nodes, func(i, j int) bool { return graph.Nodes[i].ID() < graph.Nodes[j].ID() })
	sort.Slice(graph.Edges, func(i, j int) bool {
		if graph.Edges[i].From != graph.Edges[j].From {
			return graph.Edges[i].From < graph.Edges[j].From
		}
		if graph.Edges[i].To != graph.Edges[j].To {
			return graph.Edges[i].To < graph.Edges[j].To
		}
		if graph.Edges[i].Type != graph.Edges[j].Type {
			return graph.Edges[i].Type < graph.Edges[j].Type
		}
		return graph.Edges[i].SourceVolumeName < graph.Edges[j].SourceVolumeName
	})
	sort.Strings(graph.Blockers)
	return graph, nil
}

func eligibleGraphPVC(node GraphNode, volumeName string) bool {
	if node.Kind != "PersistentVolumeClaim" || node.Storage == nil || node.Storage.PVUID == "" || node.Storage.PVResourceVersion == "" || node.Storage.StorageClassName == "" || node.Storage.StorageClassUID == "" || node.Storage.StorageClassResourceVersion == "" || node.Storage.CSIDriver == "" || !node.Storage.Bound || node.Storage.HasSelector || node.Storage.HasDataSource || node.Storage.RequestedBytes <= 0 || node.Storage.VolumeMode != "Filesystem" {
		return false
	}
	if volumeName == "" {
		return false
	}
	return len(node.Storage.AccessModes) == 1 && node.Storage.AccessModes[0] == "ReadWriteOnce"
}

func resolveGraphRoot(root string, byID map[string]GraphNode, byName map[string][]string) (string, error) {
	if _, ok := byID[root]; ok {
		return root, nil
	}
	parts := strings.Split(root, "/")
	if len(parts) == 2 {
		ids := byName[root]
		if len(ids) == 1 {
			return ids[0], nil
		}
	}
	return "", fmt.Errorf("graph root %s is missing or ambiguous", root)
}

func resolveGraphReference(namespace string, ref GraphReference, byID map[string]GraphNode, byName map[string][]string) (string, bool) {
	ids := byName[ref.Kind+"/"+ref.Name]
	var found string
	for _, id := range ids {
		if byID[id].Namespace != namespace {
			continue
		}
		if found != "" {
			return "", false
		}
		found = id
	}
	return found, found != ""
}

func graphReferenceExistsOutsideNamespace(namespace string, ref GraphReference, byName map[string][]string, byID map[string]GraphNode) bool {
	for _, id := range byName[ref.Kind+"/"+ref.Name] {
		if byID[id].Namespace != namespace {
			return true
		}
	}
	return false
}

func graphDataSizes(nodes []GraphNode) (configKeys, configBytes, secretBytes int) {
	for _, node := range nodes {
		field := ""
		switch node.Kind {
		case "ConfigMap":
			field = "data"
		case "Secret":
			field = "data"
		default:
			continue
		}
		data := graphMap(node.Object, field)
		for _, value := range data {
			if text, ok := value.(string); ok {
				if node.Kind == "ConfigMap" {
					configKeys++
					configBytes += len(text)
				} else {
					secretBytes += len(text)
				}
			}
		}
		if node.Kind == "ConfigMap" {
			for _, value := range graphMap(node.Object, "binaryData") {
				if text, ok := value.(string); ok {
					configKeys++
					configBytes += len(text)
				}
			}
		}
	}
	return configKeys, configBytes, secretBytes
}

func supportedGraphNode(kind string) bool {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "ConfigMap", "Secret", "Service":
		return true
	default:
		return false
	}
}

func graphWorkload(kind string) bool {
	return kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet"
}

func graphWorkloadReferences(node GraphNode) ([]GraphReference, []string) {
	spec := graphMap(node.Object, "spec")
	template := graphMap(spec, "template")
	podSpec := graphMap(template, "spec")
	var refs []GraphReference
	var blockers []string
	if len(graphSlice(podSpec, "imagePullSecrets")) > 0 {
		blockers = append(blockers, fmt.Sprintf("workload %s uses imagePullSecrets, which are outside combined shadow scope", node.Name))
	}
	containers := append([]any{}, graphSlice(podSpec, "initContainers")...)
	containers = append(containers, graphSlice(podSpec, "containers")...)
	for _, raw := range containers {
		container := asGraphMap(raw)
		for _, rawEnvFrom := range graphSlice(container, "envFrom") {
			envFrom := asGraphMap(rawEnvFrom)
			if config := graphMap(envFrom, "configMapRef"); config != nil {
				refs = append(refs, GraphReference{Name: graphString(config, "name"), Kind: "ConfigMap", Key: "*", Optional: graphBool(envFrom, "optional")})
			}
			if secret := graphMap(envFrom, "secretRef"); secret != nil {
				refs = append(refs, GraphReference{Name: graphString(secret, "name"), Kind: "Secret", Key: "*", Optional: graphBool(envFrom, "optional")})
			}
		}
		for _, rawEnv := range graphSlice(container, "env") {
			valueFrom := graphMap(asGraphMap(rawEnv), "valueFrom")
			for _, kind := range []string{"ConfigMap", "Secret"} {
				field := "configMapKeyRef"
				if kind == "Secret" {
					field = "secretKeyRef"
				}
				ref := graphMap(valueFrom, field)
				if ref != nil {
					refs = append(refs, GraphReference{Name: graphString(ref, "name"), Kind: kind, Key: graphString(ref, "key"), Optional: graphBool(ref, "optional")})
				}
			}
		}
	}
	for _, rawVolume := range graphSlice(podSpec, "volumes") {
		volume := asGraphMap(rawVolume)
		if config := graphMap(volume, "configMap"); config != nil {
			refs = append(refs, graphVolumeReferences("ConfigMap", graphString(config, "name"), config)...)
		}
		if secret := graphMap(volume, "secret"); secret != nil {
			refs = append(refs, graphVolumeReferences("Secret", graphString(secret, "secretName"), secret)...)
		}
		if projected := graphMap(volume, "projected"); projected != nil {
			for _, rawSource := range graphSlice(projected, "sources") {
				source := asGraphMap(rawSource)
				if config := graphMap(source, "configMap"); config != nil {
					refs = append(refs, graphVolumeReferences("ConfigMap", graphString(config, "name"), config)...)
				}
				if secret := graphMap(source, "secret"); secret != nil {
					refs = append(refs, graphVolumeReferences("Secret", graphString(secret, "name"), secret)...)
				}
				if graphMap(source, "serviceAccountToken") != nil || graphMap(source, "downwardAPI") != nil {
					blockers = append(blockers, fmt.Sprintf("workload %s uses a projected identity or downward API source outside combined shadow scope", node.Name))
				}
			}
		}
		if graphMap(volume, "persistentVolumeClaim") != nil {
			blockers = append(blockers, fmt.Sprintf("workload %s uses PersistentVolumeClaim storage outside combined shadow scope", node.Name))
			claim := graphMap(volume, "persistentVolumeClaim")
			refs = append(refs, GraphReference{Name: graphString(claim, "claimName"), Kind: "PersistentVolumeClaim", VolumeName: graphString(volume, "name")})
		}
		if graphMap(volume, "hostPath") != nil {
			blockers = append(blockers, fmt.Sprintf("workload %s uses hostPath storage outside combined shadow scope", node.Name))
		}
	}
	if account := graphString(podSpec, "serviceAccountName"); account != "" && account != "default" {
		blockers = append(blockers, fmt.Sprintf("workload %s uses a non-default service account outside combined shadow scope", node.Name))
	}
	return dedupeGraphRefs(refs), blockers
}

func dedupeGraphRefs(refs []GraphReference) []GraphReference {
	seen := map[string]int{}
	var out []GraphReference
	for _, ref := range refs {
		if ref.Name == "" {
			continue
		}
		key := ref.Kind + "/" + ref.Name + "/" + ref.Key
		if i, ok := seen[key]; ok {
			out[i].Optional = out[i].Optional && ref.Optional
			continue
		}
		seen[key] = len(out)
		out = append(out, ref)
	}
	return out
}

func graphVolumeReferences(kind, name string, source map[string]any) []GraphReference {
	optional := graphBool(source, "optional")
	items := graphSlice(source, "items")
	if len(items) == 0 {
		return []GraphReference{{Name: name, Kind: kind, Key: "*", Optional: optional}}
	}
	refs := make([]GraphReference, 0, len(items))
	for _, raw := range items {
		item := asGraphMap(raw)
		refs = append(refs, GraphReference{Name: name, Kind: kind, Key: graphString(item, "key"), Optional: optional})
	}
	return refs
}

func GraphReferences(node GraphNode) ([]GraphReference, []string) {
	return graphWorkloadReferences(node)
}

func graphWorkloadLabels(node GraphNode) map[string]any {
	template := graphMap(graphMap(graphMap(node.Object, "spec"), "template"), "metadata")
	return graphMap(template, "labels")
}

func graphSelectorMatches(selector, labels map[string]any) bool {
	if len(selector) == 0 {
		return false
	}
	for key, value := range selector {
		want, ok := value.(string)
		if !ok || labels[key] != want {
			return false
		}
	}
	return true
}

func graphMap(value map[string]any, key string) map[string]any {
	result, _ := value[key].(map[string]any)
	return result
}

func asGraphMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func graphSlice(value map[string]any, key string) []any {
	result, _ := value[key].([]any)
	return result
}

func graphString(value map[string]any, key string) string {
	result, _ := value[key].(string)
	return result
}

func graphBool(value map[string]any, key string) bool {
	result, _ := value[key].(bool)
	return result
}
