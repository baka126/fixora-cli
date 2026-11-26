package shadow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
)

var (
	graphSnapshotGVR        = schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshots"}
	graphSnapshotClassGVR   = schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotclasses"}
	graphSnapshotContentGVR = schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotcontents"}
	graphReferenceGrantGVR  = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1beta1", Resource: "referencegrants"}
)

type graphStoragePVCPlan struct {
	Name, UID, ResourceVersion                                        string
	PVName, PVUID, PVResourceVersion                                  string
	StorageClassName, StorageClassUID, StorageClassResourceVersion    string
	SnapshotClassName, SnapshotClassUID, SnapshotClassResourceVersion string
	RestoreStorageClassName                                           string
	RestoreStorageClassUID, RestoreStorageClassResourceVersion        string
	Driver                                                            string
	CapacityBytes                                                     int64
	MaxLogicalBytes                                                   int64
	RequestedBytes                                                    int64
	AccessModes                                                       []string
	VolumeMode                                                        string
}

// GraphStoragePlan is a read-only preflight snapshot. Its fields are process
// local and do not contain CSI handles or data.
type GraphStoragePlan struct {
	Namespace    string
	Policy       *GraphStorageAuthority
	PolicyDigest string
	PVCs         []graphStoragePVCPlan
}

// GraphStorageConsentItem exposes the exact storage metadata an operator must
// review before authorizing source snapshots and temporary cross-namespace
// restore references.
type GraphStorageConsentItem struct {
	Name                string
	UID                 string
	RequestedBytes      int64
	MaximumBytes        int64
	SourceStorageClass  string
	VolumeSnapshotClass string
	RestoreStorageClass string
}

func (p *GraphStoragePlan) ConsentItems() []GraphStorageConsentItem {
	if p == nil {
		return nil
	}
	items := make([]GraphStorageConsentItem, 0, len(p.PVCs))
	for _, pvc := range p.PVCs {
		items = append(items, GraphStorageConsentItem{Name: pvc.Name, UID: pvc.UID, RequestedBytes: pvc.RequestedBytes, MaximumBytes: pvc.MaxLogicalBytes, SourceStorageClass: pvc.StorageClassName, VolumeSnapshotClass: pvc.SnapshotClassName, RestoreStorageClass: pvc.RestoreStorageClassName})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

// GraphStorageSnapshotReceipt correlates one exact source claim with its
// VolumeSnapshot and bound VolumeSnapshotContent identities.
type GraphStorageSnapshotReceipt struct {
	SourcePVC string    `json:"sourcePVC"`
	Snapshot  GraphNode `json:"snapshot"`
	Content   GraphNode `json:"content"`
}

type graphStorageObjectIdentity struct {
	GVR             schema.GroupVersionResource
	Namespace, Name string
	UID             types.UID
	ResourceVersion string
}

// GraphStorageRun tracks only objects created by one confirmed run.
type GraphStorageRun struct {
	RunID, RunNamespace string
	SourceSnapshots     []GraphNode
	SnapshotReceipts    []GraphStorageSnapshotReceipt
	GrantIdentity       GraphNode
	RestoredClaims      []GraphNode
	privateSnapshots    []graphStorageObjectIdentity
	privateContents     []graphStorageObjectIdentity
	privateGrants       []graphStorageObjectIdentity
	privateClaims       []graphStorageObjectIdentity
	privatePlan         *GraphStoragePlan
}

// GraphClaimReceipt contains only Kubernetes identities and the selected
// topology node. It contains no PVC data or CSI backend handle.
type GraphClaimReceipt struct {
	SourcePVC                string `json:"sourcePVC,omitempty"`
	SourcePVCUID             string `json:"sourcePVCUid,omitempty"`
	SourcePVCResourceVersion string `json:"sourcePVCResourceVersion,omitempty"`
	RestoredPVC              string `json:"restoredPVC,omitempty"`
	RestoredPVCUID           string `json:"restoredPVCUid,omitempty"`
	PV                       string `json:"pv,omitempty"`
	PVUID                    string `json:"pvUid,omitempty"`
	CSIDriver                string `json:"csiDriver,omitempty"`
	Node                     string `json:"node,omitempty"`
	NodeUID                  string `json:"nodeUid,omitempty"`
}

type GraphStorageCleanup struct {
	Complete  bool        `json:"complete"`
	Deleted   []GraphNode `json:"deleted,omitempty"`
	Remaining []GraphNode `json:"remaining,omitempty"`
}

// PreflightGraphStorage performs read-only checks. It does not imply that a
// CSI provisioner can perform the later cross-namespace restore.
func PreflightGraphStorage(ctx context.Context, client *kube.TypedClient, graph DependencyGraph, authority *GraphStorageAuthority) (*GraphStoragePlan, error) {
	if client == nil || client.Dynamic == nil || client.Discovery == nil || authority == nil {
		return nil, errors.New("typed Kubernetes discovery, dynamic access, and storage policy are required")
	}
	if err := authority.CheckFresh(); err != nil {
		return nil, err
	}
	for _, required := range []struct{ gv, resource string }{{"snapshot.storage.k8s.io/v1", "volumesnapshots"}, {"snapshot.storage.k8s.io/v1", "volumesnapshotclasses"}, {"snapshot.storage.k8s.io/v1", "volumesnapshotcontents"}, {"gateway.networking.k8s.io/v1beta1", "referencegrants"}} {
		list, err := client.Discovery.ServerResourcesForGroupVersion(required.gv)
		if err != nil {
			return nil, fmt.Errorf("required storage API %s is unavailable", required.gv)
		}
		found := false
		for _, api := range list.APIResources {
			if api.Name == required.resource {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("required storage API resource %s is unavailable", required.resource)
		}
	}
	plan := &GraphStoragePlan{Namespace: graphSourceNamespace(graph), Policy: authority, PolicyDigest: authority.Digest()}
	requested := map[string]int64{}
	for _, node := range graph.Nodes {
		if node.Kind != "PersistentVolumeClaim" {
			continue
		}
		if node.Namespace != plan.Namespace || !eligibleGraphPVC(node, "source-volume") {
			return nil, fmt.Errorf("PVC %s is not eligible for snapshot shadow", node.Name)
		}
		pvc, err := getGraphStorageObject(ctx, client.Dynamic, schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, node.Namespace, node.Name)
		if err != nil || graphStorageUID(pvc) != types.UID(node.UID) || graphStorageRV(pvc) != node.ResourceVersion {
			return nil, fmt.Errorf("source PVC %s changed after graph discovery", node.Name)
		}
		pvcSpec := graphStorageMap(pvc.Object, "spec")
		if graphStorageString(graphStorageMap(pvc.Object, "status"), "phase") != "Bound" || graphStorageString(pvcSpec, "volumeName") != node.Storage.PVName || len(graphStorageMap(pvcSpec, "selector")) > 0 || graphStorageMap(pvcSpec, "dataSource") != nil || graphStorageMap(pvcSpec, "dataSourceRef") != nil {
			return nil, fmt.Errorf("source PVC %s is no longer an eligible bound claim", node.Name)
		}
		pv, err := getGraphStorageObject(ctx, client.Dynamic, schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}, "", node.Storage.PVName)
		if err != nil || graphStorageUID(pv) != types.UID(node.Storage.PVUID) || graphStorageRV(pv) != node.Storage.PVResourceVersion {
			return nil, fmt.Errorf("source PV for PVC %s changed after graph discovery", node.Name)
		}
		pvSpec := graphStorageMap(pv.Object, "spec")
		claim := graphStorageMap(pvSpec, "claimRef")
		csi := graphStorageMap(pvSpec, "csi")
		if graphStorageString(graphStorageMap(pv.Object, "status"), "phase") != "Bound" || graphStorageString(claim, "uid") != node.UID || graphStorageString(claim, "name") != node.Name || graphStorageString(claim, "namespace") != node.Namespace || graphStorageString(csi, "driver") != node.Storage.CSIDriver || graphStorageString(pvSpec, "storageClassName") != node.Storage.StorageClassName {
			return nil, fmt.Errorf("source PV binding or CSI driver for PVC %s is incompatible", node.Name)
		}
		sc, err := getGraphStorageObject(ctx, client.Dynamic, schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}, "", node.Storage.StorageClassName)
		if err != nil || graphStorageUID(sc) != types.UID(node.Storage.StorageClassUID) || graphStorageRV(sc) != node.Storage.StorageClassResourceVersion || graphStorageString(sc.Object, "provisioner") != node.Storage.CSIDriver {
			return nil, fmt.Errorf("source StorageClass for PVC %s changed or has a mismatched CSI driver", node.Name)
		}
		rule, err := authority.Authorize(node.Namespace, node.Name, node.Storage.RequestedBytes)
		if err != nil {
			return nil, err
		}
		snapshotClass, err := getGraphStorageObject(ctx, client.Dynamic, graphSnapshotClassGVR, "", rule.VolumeSnapshotClassName)
		if err != nil || graphStorageUID(snapshotClass) == "" || graphStorageRV(snapshotClass) == "" {
			return nil, fmt.Errorf("configured VolumeSnapshotClass for PVC %s is unavailable", node.Name)
		}
		if graphStorageString(snapshotClass.Object, "driver") != node.Storage.CSIDriver {
			return nil, fmt.Errorf("VolumeSnapshotClass driver does not match PVC %s CSI driver", node.Name)
		}
		if graphStorageString(snapshotClass.Object, "deletionPolicy") != "Delete" {
			return nil, fmt.Errorf("VolumeSnapshotClass for PVC %s must use Delete policy", node.Name)
		}
		restoreClass, err := getGraphStorageObject(ctx, client.Dynamic, schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}, "", rule.RestoreStorageClassName)
		if err != nil || graphStorageUID(restoreClass) == "" || graphStorageRV(restoreClass) == "" || graphStorageString(restoreClass.Object, "provisioner") != node.Storage.CSIDriver {
			return nil, fmt.Errorf("restore StorageClass for PVC %s is unavailable or uses a different CSI driver", node.Name)
		}
		capacity := node.Storage.RequestedBytes
		rawCapacity, found, capErr := unstructured.NestedString(pv.Object, "status", "capacity", "storage")
		if !found || capErr != nil {
			return nil, fmt.Errorf("PV capacity for PVC %s is unavailable", node.Name)
		}
		pvQuantity, quantityErr := resource.ParseQuantity(rawCapacity)
		if quantityErr != nil {
			return nil, fmt.Errorf("PV capacity for PVC %s is invalid", node.Name)
		}
		if pvCapacity, ok := pvQuantity.AsInt64(); !ok || pvCapacity <= 0 {
			return nil, fmt.Errorf("PV capacity for PVC %s is invalid", node.Name)
		} else if pvCapacity > capacity {
			capacity = pvCapacity
		}
		if _, err := authority.Authorize(node.Namespace, node.Name, capacity); err != nil {
			return nil, err
		}
		requested[node.Name] = capacity
		plan.PVCs = append(plan.PVCs, graphStoragePVCPlan{Name: node.Name, UID: node.UID, ResourceVersion: node.ResourceVersion, PVName: node.Storage.PVName, PVUID: node.Storage.PVUID, PVResourceVersion: node.Storage.PVResourceVersion, StorageClassName: node.Storage.StorageClassName, StorageClassUID: node.Storage.StorageClassUID, StorageClassResourceVersion: node.Storage.StorageClassResourceVersion, SnapshotClassName: rule.VolumeSnapshotClassName, SnapshotClassUID: string(graphStorageUID(snapshotClass)), SnapshotClassResourceVersion: graphStorageRV(snapshotClass), RestoreStorageClassName: rule.RestoreStorageClassName, RestoreStorageClassUID: string(graphStorageUID(restoreClass)), RestoreStorageClassResourceVersion: graphStorageRV(restoreClass), Driver: node.Storage.CSIDriver, CapacityBytes: capacity, MaxLogicalBytes: rule.MaxLogicalBytes, RequestedBytes: node.Storage.RequestedBytes, AccessModes: append([]string(nil), node.Storage.AccessModes...), VolumeMode: node.Storage.VolumeMode})
	}
	if len(plan.PVCs) == 0 {
		return nil, errors.New("graph contains no authorized PVC storage")
	}
	if _, err := authority.AuthorizeGraph(plan.Namespace, requested); err != nil {
		return nil, err
	}
	return plan, nil
}

// BindGraphStoragePlan adds the policy-selected class identities to each PVC
// source node so graph snapshots and certificates bind those exact choices.
func BindGraphStoragePlan(graph *DependencyGraph, plan *GraphStoragePlan) error {
	if graph == nil || plan == nil || plan.Policy == nil || plan.PolicyDigest == "" || plan.Namespace != graphSourceNamespace(*graph) {
		return errors.New("graph and storage plan identities do not match")
	}
	for i := range graph.Nodes {
		node := &graph.Nodes[i]
		if node.Kind != "PersistentVolumeClaim" {
			continue
		}
		if node.Storage == nil {
			return fmt.Errorf("PVC %s has no storage source metadata", node.Name)
		}
		found := false
		for _, p := range plan.PVCs {
			if p.Name == node.Name && p.UID == node.UID && p.ResourceVersion == node.ResourceVersion {
				node.Storage.VolumeSnapshotClassUID = p.SnapshotClassUID
				node.Storage.VolumeSnapshotClassResourceVersion = p.SnapshotClassResourceVersion
				node.Storage.RestoreStorageClassUID = p.RestoreStorageClassUID
				node.Storage.RestoreStorageClassResourceVersion = p.RestoreStorageClassResourceVersion
				node.Storage.StoragePolicyDigest = plan.PolicyDigest
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("storage plan does not bind PVC %s", node.Name)
		}
	}
	return nil
}

// PrepareGraphStorage is called only after separate operator consent and
// creation of the run namespace. It adds a narrow grant, dry-runs remote restore
// admission, and only then creates source snapshots.
func PrepareGraphStorage(ctx context.Context, client *kube.TypedClient, plan *GraphStoragePlan, runNamespace, runID string) (*GraphStorageRun, error) {
	if client == nil || client.Dynamic == nil || client.Clientset == nil || plan == nil || plan.Policy == nil || len(validation.IsDNS1123Label(runNamespace)) != 0 || runID == "" {
		return nil, errors.New("storage plan, typed client, run identity, and namespace are required")
	}
	ns, nsErr := client.GetNamespace(ctx, runNamespace)
	if nsErr != nil || ns == nil || ns.UID == "" || ns.Labels[labelSandbox] != "true" || ns.Labels[labelSession] != runID {
		return nil, errors.New("run namespace identity does not belong to this Fixora graph run")
	}
	if err := plan.Policy.CheckFresh(); err != nil {
		return nil, err
	}
	if err := checkGraphStoragePlanFresh(ctx, client, plan); err != nil {
		return nil, err
	}
	run := &GraphStorageRun{RunID: runID, RunNamespace: runNamespace, privatePlan: plan}
	grantName := graphStorageName("fixora-graph-grant", runID)
	if _, err := client.Dynamic.Resource(graphReferenceGrantGVR).Namespace(plan.Namespace).Get(ctx, grantName, metav1.GetOptions{}); err == nil {
		return nil, errors.New("temporary ReferenceGrant name already exists")
	} else if !apierrors.IsNotFound(err) {
		return nil, errors.New("temporary ReferenceGrant name cannot be checked")
	}
	for _, p := range plan.PVCs {
		if _, err := client.Dynamic.Resource(graphSnapshotGVR).Namespace(plan.Namespace).Get(ctx, graphSnapshotName(runID, p.Name), metav1.GetOptions{}); err == nil {
			return nil, errors.New("source VolumeSnapshot name already exists")
		} else if !apierrors.IsNotFound(err) {
			return nil, errors.New("source VolumeSnapshot name cannot be checked")
		}
	}
	to := make([]any, 0, len(plan.PVCs))
	for _, p := range plan.PVCs {
		to = append(to, map[string]any{"group": "snapshot.storage.k8s.io", "kind": "VolumeSnapshot", "name": graphSnapshotName(runID, p.Name)})
	}
	grant := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "gateway.networking.k8s.io/v1beta1", "kind": "ReferenceGrant", "metadata": map[string]any{"name": grantName, "namespace": plan.Namespace, "labels": map[string]any{"fixora.io/graph-run": runID}}, "spec": map[string]any{"from": []any{map[string]any{"group": "", "kind": "PersistentVolumeClaim", "namespace": runNamespace}}, "to": to}}}
	grantObj, err := client.Dynamic.Resource(graphReferenceGrantGVR).Namespace(plan.Namespace).Create(ctx, grant, metav1.CreateOptions{})
	if err != nil {
		if existing, getErr := client.Dynamic.Resource(graphReferenceGrantGVR).Namespace(plan.Namespace).Get(context.Background(), grantName, metav1.GetOptions{}); getErr == nil && existing.GetLabels()["fixora.io/graph-run"] == runID && existing.GetUID() != "" {
			run.privateGrants = append(run.privateGrants, graphStorageIdentity(graphReferenceGrantGVR, plan.Namespace, existing))
			_ = run.deleteGrant(context.Background(), client)
		}
		return nil, errors.New("temporary exact ReferenceGrant could not be created")
	}
	run.privateGrants = append(run.privateGrants, graphStorageIdentity(graphReferenceGrantGVR, plan.Namespace, grantObj))
	run.GrantIdentity = graphIdentityNode(graphStorageIdentity(graphReferenceGrantGVR, plan.Namespace, grantObj))
	if graphStorageUID(grantObj) == "" || graphStorageRV(grantObj) == "" {
		return run, errors.New("temporary ReferenceGrant was created without cleanup identity")
	}
	for _, p := range plan.PVCs {
		if err := checkGraphStoragePlanPVCFresh(ctx, client, plan.Namespace, p); err != nil {
			_ = run.deleteGrant(context.Background(), client)
			return nil, err
		}
		claim := graphRestoreClaim(p, runNamespace, runID, plan.Namespace)
		if _, err = client.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(runNamespace).Create(ctx, claim, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
			namespace, namespaceErr := client.GetNamespace(context.Background(), runNamespace)
			namespaceClean := namespaceErr == nil && namespace != nil && deleteGraphNamespace(context.Background(), client, namespace) == nil
			if cleanupErr := run.deleteGrant(context.Background(), client); cleanupErr != nil || !namespaceClean {
				return run, errors.New("restore dry-run failed and temporary storage cleanup is incomplete")
			}
			return nil, errors.New("API server rejected cross-namespace restore dry-run; no VolumeSnapshot was created")
		}
	}
	for _, p := range plan.PVCs {
		if err := checkGraphStoragePlanFresh(ctx, client, plan); err != nil {
			_ = run.CleanupSources(context.Background(), client, true)
			return nil, err
		}
		name := graphSnapshotName(runID, p.Name)
		snapshot := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshot", "metadata": map[string]any{"name": name, "namespace": plan.Namespace, "labels": map[string]any{"fixora.io/graph-run": runID}}, "spec": map[string]any{"volumeSnapshotClassName": p.SnapshotClassName, "source": map[string]any{"persistentVolumeClaimName": p.Name}}}}
		created, createErr := client.Dynamic.Resource(graphSnapshotGVR).Namespace(plan.Namespace).Create(ctx, snapshot, metav1.CreateOptions{})
		if createErr != nil {
			uncertain := graphStorageObjectIdentity{GVR: graphSnapshotGVR, Namespace: plan.Namespace, Name: name}
			run.privateSnapshots = append(run.privateSnapshots, uncertain)
			if existing, getErr := client.Dynamic.Resource(graphSnapshotGVR).Namespace(plan.Namespace).Get(context.Background(), name, metav1.GetOptions{}); getErr == nil && graphStorageUID(existing) != "" && existing.GetLabels()["fixora.io/graph-run"] == runID {
				run.privateSnapshots[len(run.privateSnapshots)-1] = graphStorageIdentity(graphSnapshotGVR, plan.Namespace, existing)
			}
			cleanup := run.CleanupSources(context.Background(), client, true)
			if !cleanup.Complete {
				return run, errors.New("source VolumeSnapshot creation outcome is ambiguous and cleanup is incomplete")
			}
			return nil, errors.New("source VolumeSnapshot creation failed")
		}
		identity := graphStorageIdentity(graphSnapshotGVR, plan.Namespace, created)
		run.privateSnapshots = append(run.privateSnapshots, identity)
		if identity.UID == "" || identity.ResourceVersion == "" {
			_ = run.CleanupSources(context.Background(), client, true)
			return run, errors.New("VolumeSnapshot was created without cleanup identity")
		}
		ready, content, waitErr := waitGraphSnapshot(ctx, client, identity, p.Driver)
		if waitErr != nil {
			if content.Name != "" {
				run.privateContents = append(run.privateContents, content)
			}
			cleanup := run.CleanupSources(context.Background(), client, true)
			if !cleanup.Complete {
				return run, errors.New("VolumeSnapshot readiness failed and cleanup is incomplete")
			}
			return nil, errors.New("source VolumeSnapshot did not become ready with a matching CSI content object")
		}
		run.privateContents = append(run.privateContents, content)
		if err := checkGraphStoragePlanFresh(ctx, client, plan); err != nil {
			cleanup := run.CleanupSources(context.Background(), client, true)
			if !cleanup.Complete {
				return run, errors.New("source storage changed during snapshot creation and cleanup is incomplete")
			}
			return nil, errors.New("source storage identity changed while the VolumeSnapshot was becoming ready")
		}
		run.SourceSnapshots = append(run.SourceSnapshots, GraphNode{APIVersion: "snapshot.storage.k8s.io/v1", Kind: "VolumeSnapshot", Namespace: plan.Namespace, Name: ready.GetName(), UID: string(ready.GetUID()), ResourceVersion: ready.GetResourceVersion()})
		run.SnapshotReceipts = append(run.SnapshotReceipts, GraphStorageSnapshotReceipt{SourcePVC: "PersistentVolumeClaim/" + plan.Namespace + "/" + p.Name, Snapshot: run.SourceSnapshots[len(run.SourceSnapshots)-1], Content: graphIdentityNode(content)})
	}
	return run, nil
}

func checkGraphStoragePlanFresh(ctx context.Context, client *kube.TypedClient, plan *GraphStoragePlan) error {
	if plan == nil || plan.Policy == nil {
		return errors.New("graph storage plan authority is missing")
	}
	if err := plan.Policy.CheckFresh(); err != nil {
		return err
	}
	for _, p := range plan.PVCs {
		if err := checkGraphStoragePlanPVCFresh(ctx, client, plan.Namespace, p); err != nil {
			return err
		}
		checks := []struct {
			gvr  schema.GroupVersionResource
			name string
			uid  types.UID
			rv   string
		}{
			{graphSnapshotClassGVR, p.SnapshotClassName, types.UID(p.SnapshotClassUID), p.SnapshotClassResourceVersion},
			{schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}, p.StorageClassName, types.UID(p.StorageClassUID), p.StorageClassResourceVersion},
			{schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}, p.RestoreStorageClassName, types.UID(p.RestoreStorageClassUID), p.RestoreStorageClassResourceVersion},
		}
		for _, check := range checks {
			obj, err := getGraphStorageObject(ctx, client.Dynamic, check.gvr, "", check.name)
			if err != nil || graphStorageUID(obj) != check.uid || graphStorageRV(obj) != check.rv {
				return errors.New("storage class identity changed after preflight")
			}
		}
	}
	return nil
}

func checkGraphStoragePlanPVCFresh(ctx context.Context, client *kube.TypedClient, namespace string, p graphStoragePVCPlan) error {
	pvc, err := getGraphStorageObject(ctx, client.Dynamic, schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, namespace, p.Name)
	if err != nil || graphStorageUID(pvc) != types.UID(p.UID) || graphStorageRV(pvc) != p.ResourceVersion {
		return fmt.Errorf("source PVC %s changed after preflight", p.Name)
	}
	pv, err := getGraphStorageObject(ctx, client.Dynamic, schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}, "", p.PVName)
	if err != nil || graphStorageUID(pv) != types.UID(p.PVUID) || graphStorageRV(pv) != p.PVResourceVersion {
		return fmt.Errorf("source PV for PVC %s changed after preflight", p.Name)
	}
	return nil
}

func graphRestoreClaim(p graphStoragePVCPlan, namespace, runID, sourceNamespace string) *unstructured.Unstructured {
	access := make([]any, 0, len(p.AccessModes))
	for _, m := range p.AccessModes {
		access = append(access, m)
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": graphRestoreClaimName(runID, p.Name), "namespace": namespace, "labels": map[string]any{"fixora.io/graph-run": runID}}, "spec": map[string]any{"storageClassName": p.RestoreStorageClassName, "accessModes": access, "volumeMode": p.VolumeMode, "resources": map[string]any{"requests": map[string]any{"storage": resource.NewQuantity(p.RequestedBytes, resource.BinarySI).String()}}, "dataSourceRef": map[string]any{"apiGroup": "snapshot.storage.k8s.io", "kind": "VolumeSnapshot", "name": graphSnapshotName(runID, p.Name), "namespace": sourceNamespace}}}}
}

func waitGraphSnapshot(ctx context.Context, client *kube.TypedClient, id graphStorageObjectIdentity, driver string) (*unstructured.Unstructured, graphStorageObjectIdentity, error) {
	var ready *unstructured.Unstructured
	var content graphStorageObjectIdentity
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		obj, err := client.Dynamic.Resource(id.GVR).Namespace(id.Namespace).Get(ctx, id.Name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		isReady, _, _ := unstructured.NestedBool(obj.Object, "status", "readyToUse")
		contentName, _, _ := unstructured.NestedString(obj.Object, "status", "boundVolumeSnapshotContentName")
		if !isReady || contentName == "" {
			return false, nil
		}
		c, err := client.Dynamic.Resource(graphSnapshotContentGVR).Get(ctx, contentName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		spec := graphStorageMap(c.Object, "spec")
		snapshotRef := graphStorageMap(spec, "volumeSnapshotRef")
		if graphStorageString(spec, "driver") != driver || graphStorageString(snapshotRef, "name") != id.Name || graphStorageString(snapshotRef, "namespace") != id.Namespace || graphStorageString(snapshotRef, "uid") != string(id.UID) || graphStorageUID(c) == "" || graphStorageRV(c) == "" {
			return false, nil
		}
		ready = obj
		content = graphStorageIdentity(graphSnapshotContentGVR, "", c)
		return true, nil
	})
	if err != nil {
		return nil, content, err
	}
	return ready, content, nil
}

// CleanupSources removes only this run's exact grants and snapshots. The caller
// must first confirm that shadow PVCs and Pods are gone.
func (r *GraphStorageRun) CleanupSources(ctx context.Context, client *kube.TypedClient, shadowCleanupComplete bool) GraphStorageCleanup {
	result := GraphStorageCleanup{Complete: false}
	if r == nil || client == nil || client.Dynamic == nil {
		return result
	}
	if !shadowCleanupComplete {
		result.Remaining = append(result.Remaining, graphIdentitiesAsNodes(r.privateGrants)...)
		result.Remaining = append(result.Remaining, graphIdentitiesAsNodes(r.privateSnapshots)...)
		return result
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, identity := range r.privateGrants {
		if deleteGraphStorageIdentity(cleanupCtx, client, identity) {
			result.Deleted = append(result.Deleted, graphIdentityNode(identity))
		} else {
			result.Remaining = append(result.Remaining, graphIdentityNode(identity))
		}
	}
	if len(result.Remaining) > 0 {
		result.Complete = false
		return result
	}
	for _, identity := range r.privateSnapshots {
		if deleteGraphStorageIdentity(cleanupCtx, client, identity) {
			result.Deleted = append(result.Deleted, graphIdentityNode(identity))
		} else {
			result.Remaining = append(result.Remaining, graphIdentityNode(identity))
		}
	}
	for _, identity := range r.privateContents {
		if !waitGraphStorageNotFound(cleanupCtx, client, identity) {
			result.Remaining = append(result.Remaining, graphIdentityNode(identity))
		}
	}
	result.Complete = len(result.Remaining) == 0
	return result
}

func deleteGraphStorageIdentity(ctx context.Context, client *kube.TypedClient, id graphStorageObjectIdentity) bool {
	current, err := getGraphStorageObject(ctx, client.Dynamic, id.GVR, id.Namespace, id.Name)
	if apierrors.IsNotFound(err) {
		return true
	}
	if err != nil || graphStorageUID(current) != id.UID {
		return false
	}
	uid := id.UID
	resource := client.Dynamic.Resource(id.GVR)
	if id.Namespace != "" {
		err = resource.Namespace(id.Namespace).Delete(ctx, id.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	} else {
		err = resource.Delete(ctx, id.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return false
	}
	return waitGraphStorageNotFound(ctx, client, id)
}

func waitGraphStorageNotFound(ctx context.Context, client *kube.TypedClient, id graphStorageObjectIdentity) bool {
	err := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := getGraphStorageObject(ctx, client.Dynamic, id.GVR, id.Namespace, id.Name)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, nil
		}
		return false, nil
	})
	return err == nil
}

func (r *GraphStorageRun) deleteGrant(ctx context.Context, client *kube.TypedClient) error {
	result := r.CleanupSources(ctx, client, true)
	if !result.Complete {
		return errors.New("grant cleanup incomplete")
	}
	r.privateSnapshots = nil
	r.privateContents = nil
	return nil
}

// CreateRestoredClaims creates every run-owned PVC from its exact source
// VolumeSnapshot. It returns before waiting for binding so WaitForFirstConsumer
// classes can bind after Pods exist.
func (r *GraphStorageRun) CreateRestoredClaims(ctx context.Context, client *kube.TypedClient, namespace, runID string) ([]GraphNode, error) {
	if r == nil || client == nil || client.Dynamic == nil || client.Clientset == nil || r.privatePlan == nil || namespace != r.RunNamespace || runID != r.RunID || len(r.privateSnapshots) != len(r.privatePlan.PVCs) {
		return nil, errors.New("prepared storage run and matching run namespace are required")
	}
	ns, err := client.GetNamespace(ctx, namespace)
	if err != nil || ns == nil || ns.UID == "" || ns.Labels[labelSandbox] != "true" || ns.Labels[labelSession] != runID {
		return nil, errors.New("run namespace identity changed before claim restore")
	}
	if err := r.privatePlan.Policy.CheckFresh(); err != nil {
		return nil, err
	}
	for i, p := range r.privatePlan.PVCs {
		source := r.privateSnapshots[i]
		if source.GVR != graphSnapshotGVR || source.Namespace != r.privatePlan.Namespace || source.Name != graphSnapshotName(runID, p.Name) {
			return append([]GraphNode(nil), r.RestoredClaims...), errors.New("source snapshot set changed before claim restore")
		}
		current, getErr := client.Dynamic.Resource(graphSnapshotGVR).Namespace(source.Namespace).Get(ctx, source.Name, metav1.GetOptions{})
		if getErr != nil || current.GetUID() != source.UID || current.GetResourceVersion() == "" {
			return append([]GraphNode(nil), r.RestoredClaims...), errors.New("source snapshot identity changed before claim restore")
		}
		claim := graphRestoreClaim(p, namespace, runID, r.privatePlan.Namespace)
		created, createErr := client.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(namespace).Create(ctx, claim, metav1.CreateOptions{})
		if createErr != nil {
			return append([]GraphNode(nil), r.RestoredClaims...), errors.New("restored PVC could not be created")
		}
		if created == nil || created.GetUID() == "" || created.GetResourceVersion() == "" {
			return append([]GraphNode(nil), r.RestoredClaims...), errors.New("restored PVC was created without cleanup identity")
		}
		identity := graphStorageIdentity(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, namespace, created)
		r.privateClaims = append(r.privateClaims, identity)
		r.RestoredClaims = append(r.RestoredClaims, GraphNode{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: namespace, Name: created.GetName(), UID: string(created.GetUID()), ResourceVersion: created.GetResourceVersion()})
	}
	return append([]GraphNode(nil), r.RestoredClaims...), nil
}

// RewriteGraphPodClaims replaces source PVC names only when the workload's
// volume name is bound to an authorized graph PVC edge.
func (r *GraphStorageRun) RewriteGraphPodClaims(pod *corev1.Pod, source GraphNode, graph DependencyGraph) error {
	if r == nil || pod == nil || r.privatePlan == nil {
		return errors.New("prepared graph storage run and candidate Pod are required")
	}
	bySource := map[string]string{}
	for _, restored := range r.RestoredClaims {
		for _, p := range r.privatePlan.PVCs {
			if restored.Name == graphRestoreClaimName(r.RunID, p.Name) {
				bySource[p.Name] = restored.Name
			}
		}
	}
	volumeClaims := map[string]string{}
	for _, edge := range graph.Edges {
		if edge.From != source.ID() || edge.Type != GraphPVCEdge {
			continue
		}
		pvc, ok := graphNodeByID(graph, edge.To)
		if !ok || pvc.Kind != "PersistentVolumeClaim" || edge.SourceVolumeName == "" || bySource[pvc.Name] == "" {
			return errors.New("graph PVC edge is incomplete")
		}
		if _, exists := volumeClaims[edge.SourceVolumeName]; exists {
			return errors.New("candidate PVC volume mapping is ambiguous")
		}
		volumeClaims[edge.SourceVolumeName] = bySource[pvc.Name]
	}
	for i := range pod.Spec.Volumes {
		volume := &pod.Spec.Volumes[i]
		if volume.PersistentVolumeClaim == nil {
			continue
		}
		restored, ok := volumeClaims[volume.Name]
		if !ok {
			return errors.New("candidate Pod added an unauthorized PVC volume")
		}
		volume.PersistentVolumeClaim.ClaimName = restored
	}
	return nil
}

func graphNodeByID(graph DependencyGraph, id string) (GraphNode, bool) {
	for _, node := range graph.Nodes {
		if node.ID() == id {
			return node, true
		}
	}
	return GraphNode{}, false
}

// VerifyClaimScheduling waits until each candidate Pod is scheduled and its
// restored claim is bound, then records the exact restore PV and node identities.
func (r *GraphStorageRun) VerifyClaimScheduling(ctx context.Context, client *kube.TypedClient, graph DependencyGraph, pods []corev1.Pod) ([]GraphClaimReceipt, error) {
	if r == nil || client == nil || client.Dynamic == nil || client.Clientset == nil || r.privatePlan == nil || len(r.RestoredClaims) != len(r.privatePlan.PVCs) {
		return nil, errors.New("all restored claims and typed clients are required")
	}
	var receipts []GraphClaimReceipt
	err := wait.PollUntilContextTimeout(ctx, 250*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := checkGraphStoragePlanFresh(ctx, client, r.privatePlan); err != nil {
			return false, err
		}
		current := make([]GraphClaimReceipt, 0, len(r.privatePlan.PVCs))
		scheduledPods := make(map[string]corev1.Pod)
		workloadCount := 0
		for _, node := range graph.Nodes {
			if graphWorkload(node.Kind) {
				workloadCount++
			}
		}
		for _, created := range pods {
			fresh, getErr := client.Clientset.CoreV1().Pods(r.RunNamespace).Get(ctx, created.Name, metav1.GetOptions{})
			if getErr != nil {
				return false, nil
			}
			source := fresh.Annotations["fixora.io/source-resource"]
			node, ok := graphNodeByID(graph, source)
			if !ok || !graphWorkload(node.Kind) {
				return false, errors.New("candidate Pod source identity is invalid")
			}
			if _, dup := scheduledPods[source]; dup {
				return false, errors.New("duplicate candidate Pod source identity")
			}
			if fresh.Spec.NodeName == "" {
				return false, nil
			}
			scheduledPods[source] = *fresh
		}
		if len(scheduledPods) != workloadCount {
			return false, nil
		}
		for _, edge := range graph.Edges {
			if edge.Type != GraphPVCEdge {
				continue
			}
			source, ok := scheduledPods[edge.From]
			if !ok {
				return false, errors.New("PVC consumer Pod is missing")
			}
			pvc, ok := graphNodeByID(graph, edge.To)
			if !ok {
				return false, errors.New("PVC graph node disappeared")
			}
			restored := graphRestoreClaimName(r.RunID, pvc.Name)
			found := false
			for _, volume := range source.Spec.Volumes {
				if volume.Name == edge.SourceVolumeName && volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == restored {
					found = true
				}
			}
			if !found {
				return false, errors.New("candidate Pod does not use the exact restored PVC")
			}
		}
		for i, p := range r.privatePlan.PVCs {
			claimNode := r.RestoredClaims[i]
			claim, err := getGraphStorageObject(ctx, client.Dynamic, schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, r.RunNamespace, claimNode.Name)
			if err != nil {
				return false, nil
			}
			if graphStorageUID(claim) != types.UID(claimNode.UID) {
				return false, errors.New("restored PVC UID changed")
			}
			claimSpec := graphStorageMap(claim.Object, "spec")
			dataSource := graphStorageMap(claimSpec, "dataSourceRef")
			if graphStorageString(claimSpec, "storageClassName") != p.RestoreStorageClassName || graphStorageString(claimSpec, "volumeMode") != p.VolumeMode || graphStorageString(dataSource, "apiGroup") != "snapshot.storage.k8s.io" || graphStorageString(dataSource, "kind") != "VolumeSnapshot" || graphStorageString(dataSource, "namespace") != r.privatePlan.Namespace || graphStorageString(dataSource, "name") != graphSnapshotName(r.RunID, p.Name) || graphStorageMap(claimSpec, "selector") != nil {
				return false, errors.New("restored PVC source or class changed")
			}
			accessModes, _, _ := unstructured.NestedStringSlice(claim.Object, "spec", "accessModes")
			if len(accessModes) != 1 || accessModes[0] != "ReadWriteOnce" {
				return false, errors.New("restored PVC access mode changed")
			}
			requestRaw, _, _ := unstructured.NestedString(claim.Object, "spec", "resources", "requests", "storage")
			request, requestErr := resource.ParseQuantity(requestRaw)
			if requestErr != nil {
				return false, errors.New("restored PVC request is invalid")
			}
			if requested, ok := request.AsInt64(); !ok || requested < p.RequestedBytes {
				return false, errors.New("restored PVC request is insufficient")
			}
			if graphStorageString(graphStorageMap(claim.Object, "status"), "phase") != "Bound" {
				return false, nil
			}
			pvName := graphStorageString(graphStorageMap(claim.Object, "spec"), "volumeName")
			if pvName == "" {
				return false, nil
			}
			pv, err := getGraphStorageObject(ctx, client.Dynamic, schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}, "", pvName)
			if err != nil {
				return false, nil
			}
			if pvName == p.PVName || graphStorageUID(pv) == types.UID(p.PVUID) {
				return false, errors.New("restored PVC reused the production PV")
			}
			pvSpec, claimRef := graphStorageMap(pv.Object, "spec"), graphStorageMap(graphStorageMap(pv.Object, "spec"), "claimRef")
			if graphStorageUID(pv) == "" || graphStorageRV(pv) == "" || graphStorageString(graphStorageMap(pvSpec, "csi"), "driver") != p.Driver || graphStorageString(claimRef, "namespace") != r.RunNamespace || graphStorageString(claimRef, "name") != claimNode.Name || graphStorageString(claimRef, "uid") != claimNode.UID || graphStorageString(pvSpec, "storageClassName") != p.RestoreStorageClassName {
				return false, errors.New("restored PV binding or CSI driver is incompatible")
			}
			capacityRaw, found, _ := unstructured.NestedString(pv.Object, "status", "capacity", "storage")
			if !found {
				return false, errors.New("restored PV capacity is missing")
			}
			capacity, err := resource.ParseQuantity(capacityRaw)
			if err != nil {
				return false, errors.New("restored PV capacity is invalid")
			}
			if bytes, ok := capacity.AsInt64(); !ok || bytes < p.RequestedBytes {
				return false, errors.New("restored PV capacity is insufficient")
			}
			var selectedNode *corev1.Node
			for _, pod := range scheduledPods {
				uses := false
				for _, volume := range pod.Spec.Volumes {
					if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == claimNode.Name {
						uses = true
					}
				}
				if !uses {
					continue
				}
				node, err := client.Clientset.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
				if err != nil {
					return false, nil
				}
				if node.UID == "" || !graphPVAllowsNode(pvSpec, node) {
					return false, errors.New("restored PV topology does not allow the scheduled node")
				}
				if selectedNode != nil && selectedNode.UID != node.UID {
					return false, errors.New("one RWO claim is scheduled across multiple nodes")
				}
				selectedNode = node
			}
			if selectedNode == nil {
				return false, nil
			}
			current = append(current, GraphClaimReceipt{SourcePVC: "PersistentVolumeClaim/" + r.privatePlan.Namespace + "/" + p.Name, SourcePVCUID: p.UID, SourcePVCResourceVersion: p.ResourceVersion, RestoredPVC: claimNode.Name, RestoredPVCUID: claimNode.UID, PV: pvName, PVUID: string(graphStorageUID(pv)), CSIDriver: p.Driver, Node: selectedNode.Name, NodeUID: string(selectedNode.UID)})
		}
		receipts = current
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return receipts, nil
}

func graphPVAllowsNode(pvSpec map[string]any, node *corev1.Node) bool {
	terms, found, _ := unstructured.NestedSlice(pvSpec, "nodeAffinity", "required", "nodeSelectorTerms")
	if !found || len(terms) == 0 {
		return true
	}
	for _, raw := range terms {
		term, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		matches := true
		expressions, _, _ := unstructured.NestedSlice(term, "matchExpressions")
		fields, _, _ := unstructured.NestedSlice(term, "matchFields")
		for _, rawExpr := range append(expressions, fields...) {
			expr, ok := rawExpr.(map[string]any)
			if !ok {
				matches = false
				break
			}
			key, op := graphStorageString(expr, "key"), graphStorageString(expr, "operator")
			values, _, _ := unstructured.NestedStringSlice(expr, "values")
			value, exists := node.Labels[key]
			if key == "metadata.name" {
				value, exists = node.Name, true
			}
			if !exists {
				value = ""
			}
			hit := false
			for _, v := range values {
				if value == v {
					hit = true
				}
			}
			switch op {
			case "In":
				matches = matches && hit
			case "NotIn":
				matches = matches && !hit
			case "Exists":
				matches = matches && exists
			case "DoesNotExist":
				matches = matches && !exists
			case "Gt", "Lt":
				if len(values) != 1 {
					matches = false
					break
				}
				numeric, err := strconv.Atoi(value)
				bound, boundErr := strconv.Atoi(values[0])
				if err != nil || boundErr != nil {
					matches = false
				} else if op == "Gt" {
					matches = matches && numeric > bound
				} else {
					matches = matches && numeric < bound
				}
			default:
				matches = false
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func getGraphStorageObject(ctx context.Context, client dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	if namespace != "" {
		return client.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	}
	return client.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
}
func graphStorageIdentity(gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) graphStorageObjectIdentity {
	return graphStorageObjectIdentity{GVR: gvr, Namespace: namespace, Name: obj.GetName(), UID: obj.GetUID(), ResourceVersion: obj.GetResourceVersion()}
}
func graphStorageUID(obj *unstructured.Unstructured) types.UID {
	if obj == nil {
		return ""
	}
	return obj.GetUID()
}
func graphStorageRV(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	return obj.GetResourceVersion()
}
func graphStorageMap(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}
func graphStorageString(m map[string]any, key string) string { v, _ := m[key].(string); return v }
func graphStorageName(prefix, runID string) string {
	digest := sha256.Sum256([]byte(runID))
	return prefix + "-" + hex.EncodeToString(digest[:8])
}
func graphSnapshotName(runID, pvc string) string {
	return graphStorageName("fixora-snapshot", runID) + "-" + shortGraphName(pvc)
}
func graphRestoreClaimName(runID, pvc string) string {
	return graphStorageName("fixora-restore", runID) + "-" + shortGraphName(pvc)
}
func shortGraphName(name string) string {
	clean := strings.ToLower(strings.ReplaceAll(name, "_", "-"))
	digest := sha256.Sum256([]byte(name))
	suffix := hex.EncodeToString(digest[:4])
	if len(clean) > 11 {
		clean = clean[:11]
	}
	return strings.Trim(clean, "-") + "-" + suffix
}
func graphIdentityNode(i graphStorageObjectIdentity) GraphNode {
	kind := map[string]string{"volumesnapshots": "VolumeSnapshot", "volumesnapshotcontents": "VolumeSnapshotContent", "referencegrants": "ReferenceGrant"}[i.GVR.Resource]
	if kind == "" {
		kind = i.GVR.Resource
	}
	return GraphNode{APIVersion: i.GVR.GroupVersion().String(), Kind: kind, Namespace: i.Namespace, Name: i.Name, UID: string(i.UID), ResourceVersion: i.ResourceVersion}
}
func graphIdentitiesAsNodes(ids []graphStorageObjectIdentity) []GraphNode {
	out := make([]GraphNode, 0, len(ids))
	for _, id := range ids {
		out = append(out, graphIdentityNode(id))
	}
	return out
}
