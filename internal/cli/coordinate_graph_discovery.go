package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"k8s.io/apimachinery/pkg/api/resource"
)

type coordinateGraphWorkloads struct {
	Workloads []shadow.GraphNode
	Services  []shadow.GraphNode
}

func coordinateGraphWorkloadKind(kind string) bool {
	return kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet"
}

func expandCoordinateGraphWorkloads(rootID string, workloads, services []shadow.GraphNode) coordinateGraphWorkloads {
	byID := make(map[string]shadow.GraphNode, len(workloads))
	selected := map[string]bool{rootID: true}
	for _, workload := range workloads {
		byID[workload.ID()] = workload
	}
	if _, exists := byID[rootID]; !exists {
		return coordinateGraphWorkloads{}
	}
	selectedServices := map[string]shadow.GraphNode{}
	for changed := true; changed; {
		changed = false
		sharedRefs := map[string]bool{}
		for id := range selected {
			workload := byID[id]
			refs, _ := shadow.GraphReferences(workload)
			for _, ref := range refs {
				sharedRefs[ref.Kind+"/"+ref.Name] = true
			}
		}
		for id, workload := range byID {
			if selected[id] {
				continue
			}
			refs, _ := shadow.GraphReferences(workload)
			for _, ref := range refs {
				if sharedRefs[ref.Kind+"/"+ref.Name] {
					selected[id] = true
					changed = true
					break
				}
			}
		}
		for _, service := range services {
			selector := mapAt(mapAt(service.Object, "spec"), "selector")
			if len(selector) == 0 {
				continue
			}
			matchesSelected := false
			for id := range selected {
				if graphSelectorMatchesCLI(selector, graphLabelsCLI(byID[id])) {
					matchesSelected = true
					break
				}
			}
			if !matchesSelected {
				continue
			}
			if _, exists := selectedServices[service.ID()]; !exists {
				selectedServices[service.ID()] = service
				changed = true
			}
			for id, workload := range byID {
				if graphSelectorMatchesCLI(selector, graphLabelsCLI(workload)) && !selected[id] {
					selected[id] = true
					changed = true
				}
			}
		}
	}
	result := coordinateGraphWorkloads{}
	for id := range selected {
		result.Workloads = append(result.Workloads, byID[id])
	}
	for _, service := range selectedServices {
		result.Services = append(result.Services, service)
	}
	sort.Slice(result.Workloads, func(i, j int) bool { return result.Workloads[i].ID() < result.Workloads[j].ID() })
	sort.Slice(result.Services, func(i, j int) bool { return result.Services[i].ID() < result.Services[j].ID() })
	return result
}

func discoverCoordinateDependencyGraph(ctx context.Context, k kube.Kubectl, rootRef, namespace string, storagePolicy ...*shadow.GraphStorageAuthority) (shadow.DependencyGraph, error) {
	var workloads []shadow.GraphNode
	for _, resource := range []string{"deployments", "statefulsets", "daemonsets"} {
		items, err := k.GetResourceItems(ctx, namespace, false, resource)
		if err != nil {
			return shadow.DependencyGraph{}, fmt.Errorf("list %s for dependency graph: %w", resource, err)
		}
		for _, item := range items {
			node, err := coordinateGraphNode(item)
			if err != nil {
				return shadow.DependencyGraph{}, err
			}
			workloads = append(workloads, node)
		}
	}
	serviceItems, err := k.GetResourceItems(ctx, namespace, false, "services")
	if err != nil {
		return shadow.DependencyGraph{}, fmt.Errorf("list services for dependency graph: %w", err)
	}
	services := make([]shadow.GraphNode, 0, len(serviceItems))
	for _, item := range serviceItems {
		node, err := coordinateGraphNode(item)
		if err != nil {
			return shadow.DependencyGraph{}, err
		}
		services = append(services, node)
	}
	rootID := rootRef
	if strings.Count(rootRef, "/") == 1 {
		parts := strings.SplitN(rootRef, "/", 2)
		rootID = parts[0] + "/" + namespace + "/" + parts[1]
	}
	selected := expandCoordinateGraphWorkloads(rootID, workloads, services)
	if len(selected.Workloads) == 0 {
		return shadow.DependencyGraph{}, fmt.Errorf("selected workload %s is not present in the namespace inventory", rootRef)
	}
	nodes := append([]shadow.GraphNode(nil), selected.Workloads...)
	nodes = append(nodes, selected.Services...)
	dependencies := map[string]shadow.GraphReference{}
	for _, workload := range selected.Workloads {
		refs, _ := shadow.GraphReferences(workload)
		for _, ref := range refs {
			dependencies[ref.Kind+"/"+workload.Namespace+"/"+ref.Name] = ref
		}
	}
	depKeys := make([]string, 0, len(dependencies))
	for key := range dependencies {
		depKeys = append(depKeys, key)
	}
	sort.Strings(depKeys)
	for _, key := range depKeys {
		if len(nodes) >= shadow.GraphNodeLimit {
			break
		}
		parts := strings.SplitN(key, "/", 3)
		if len(parts) != 3 {
			continue
		}
		resource, ns, name := parts[0], parts[1], parts[2]
		obj, getErr := k.GetResource(ctx, ns, resource+"/"+name)
		if getErr != nil {
			if isKubernetesNotFound(getErr) {
				continue
			}
			return shadow.DependencyGraph{}, fmt.Errorf("read referenced %s metadata for dependency graph: %w", resource, getErr)
		}
		node, nodeErr := coordinateGraphNode(obj)
		if nodeErr != nil {
			return shadow.DependencyGraph{}, nodeErr
		}
		if node.Kind == "PersistentVolumeClaim" && len(storagePolicy) > 0 && storagePolicy[0] != nil {
			if err := populateCoordinateGraphStorage(ctx, k, &node); err != nil {
				return shadow.DependencyGraph{}, fmt.Errorf("inspect PVC %s storage source: %w", node.Name, err)
			}
		}
		nodes = append(nodes, node)
	}
	options := shadow.GraphBuildOptions{}
	if len(storagePolicy) > 0 {
		options.StoragePolicy = storagePolicy[0]
	}
	return shadow.BuildDependencyGraphWithOptions(rootID, nodes, options)
}

func populateCoordinateGraphStorage(ctx context.Context, k kube.Kubectl, pvc *shadow.GraphNode) error {
	spec := mapAt(pvc.Object, "spec")
	status := mapAt(pvc.Object, "status")
	storage := &shadow.GraphStorageSource{Bound: strAt(status, "phase") == "Bound", VolumeMode: strAt(spec, "volumeMode"), HasSelector: len(mapAt(spec, "selector")) > 0, HasDataSource: mapAt(spec, "dataSource") != nil || mapAt(spec, "dataSourceRef") != nil}
	if storage.VolumeMode == "" {
		storage.VolumeMode = "Filesystem"
	}
	for _, value := range sliceAt(spec, "accessModes") {
		if mode, ok := value.(string); ok {
			storage.AccessModes = append(storage.AccessModes, mode)
		}
	}
	request := strAt(mapAt(mapAt(spec, "resources"), "requests"), "storage")
	if quantity, err := resource.ParseQuantity(request); err == nil {
		storage.RequestedBytes, _ = quantity.AsInt64()
	}
	pvName := strAt(spec, "volumeName")
	if storage.Bound && pvName != "" {
		pv, err := k.GetResource(ctx, "", "persistentvolumes/"+pvName)
		if err != nil {
			return fmt.Errorf("read bound PV metadata")
		}
		pvMetadata, pvSpec := mapAt(pv, "metadata"), mapAt(pv, "spec")
		if strAt(mapAt(pv, "status"), "phase") != "Bound" || strAt(pvSpec, "storageClassName") != strAt(spec, "storageClassName") {
			return fmt.Errorf("PV is not bound to the PVC's exact StorageClass")
		}
		claim := mapAt(pvSpec, "claimRef")
		if strAt(claim, "namespace") != pvc.Namespace || strAt(claim, "name") != pvc.Name || strAt(claim, "uid") != pvc.UID {
			return fmt.Errorf("bound PV claim identity does not match PVC")
		}
		csi := mapAt(pvSpec, "csi")
		storage.PVName, storage.PVUID, storage.PVResourceVersion = pvName, strAt(pvMetadata, "uid"), strAt(pvMetadata, "resourceVersion")
		storage.CSIDriver = strAt(csi, "driver")
		storage.StorageClassName = strAt(spec, "storageClassName")
		if storage.StorageClassName != "" {
			class, classErr := k.GetResource(ctx, "", "storageclasses/"+storage.StorageClassName)
			if classErr != nil {
				return fmt.Errorf("read source StorageClass metadata")
			}
			classMetadata := mapAt(class, "metadata")
			storage.StorageClassUID, storage.StorageClassResourceVersion = strAt(classMetadata, "uid"), strAt(classMetadata, "resourceVersion")
		}
	}
	pvc.Storage = storage
	return nil
}

func coordinateGraphNode(object map[string]any) (shadow.GraphNode, error) {
	metadata := asMap(object["metadata"])
	node := shadow.GraphNode{
		APIVersion: strAt(object, "apiVersion"), Kind: strAt(object, "kind"),
		Namespace: strAt(metadata, "namespace"), Name: strAt(metadata, "name"),
		UID: strAt(metadata, "uid"), ResourceVersion: strAt(metadata, "resourceVersion"), Object: object,
	}
	if node.APIVersion == "" || node.Kind == "" || node.Namespace == "" || node.Name == "" || node.UID == "" || node.ResourceVersion == "" {
		return shadow.GraphNode{}, fmt.Errorf("resource snapshot has incomplete identity; cannot build dependency graph")
	}
	return node, nil
}

func graphLabelsCLI(node shadow.GraphNode) map[string]any {
	if node.Kind == "Pod" {
		return mapAt(mapAt(node.Object, "metadata"), "labels")
	}
	return mapAt(mapAt(mapAt(mapAt(node.Object, "spec"), "template"), "metadata"), "labels")
}

func graphSelectorMatchesCLI(selector, labels map[string]any) bool {
	if len(selector) == 0 {
		return false
	}
	for key, raw := range selector {
		want, ok := raw.(string)
		if !ok || labels[key] != want {
			return false
		}
	}
	return true
}

func isKubernetesNotFound(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "notfound") || strings.Contains(message, "not found")
}
