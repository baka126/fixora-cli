package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	corefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestGraphStorageLifecyclePreflightRequiresDiscoveryAndClassIdentity(t *testing.T) {
	authority := loadGraphStoragePolicyForTest(t, validGraphStoragePolicy)
	client := &kube.TypedClient{Dynamic: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshots"}:        "VolumeSnapshotList",
		{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotclasses"}:  "VolumeSnapshotClassList",
		{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotcontents"}: "VolumeSnapshotContentList",
		{Group: "gateway.networking.k8s.io", Version: "v1beta1", Resource: "referencegrants"}: "ReferenceGrantList",
	})}
	graph := storageGraphFixture()
	if _, err := PreflightGraphStorage(context.Background(), client, graph, authority); err == nil {
		t.Fatal("preflight must refuse absent source objects and discovery")
	}
}

type graphStorageDiscovery struct {
	discovery.DiscoveryInterface
	resources map[string]*metav1.APIResourceList
}

func (d graphStorageDiscovery) ServerResourcesForGroupVersion(version string) (*metav1.APIResourceList, error) {
	list, ok := d.resources[version]
	if !ok {
		return nil, fmt.Errorf("missing API")
	}
	return list, nil
}

func TestGraphStorageLifecyclePreflightBindsPVCAndClassIdentitiesReadOnly(t *testing.T) {
	authority := loadGraphStoragePolicyForTest(t, validGraphStoragePolicy)
	client, dyn := newGraphStorageClient(t)
	graph := storageGraphFixture()
	plan, err := PreflightGraphStorage(context.Background(), client, graph, authority)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.PVCs) != 1 || plan.PVCs[0].UID != "pvc-uid" || plan.PVCs[0].PVUID != "pv-uid" || plan.PVCs[0].SnapshotClassUID != "vsc-uid" || plan.PVCs[0].RestoreStorageClassName != "gp3-shadow" {
		t.Fatalf("preflight did not bind exact storage identities: %#v", plan)
	}
	consent := plan.ConsentItems()
	if len(consent) != 1 || consent[0].MaximumBytes != 4<<30 || consent[0].UID != "pvc-uid" {
		t.Fatalf("consent view must report the operator policy maximum and exact PVC identity: %#v", consent)
	}
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "patch" || action.GetVerb() == "delete" {
			t.Fatalf("preflight wrote to the cluster: %#v", action)
		}
	}
}

func TestGraphStorageLifecycleRejectsClassDriftDriverMismatchAndRetain(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*unstructured.Unstructured)
	}{
		{"class driver mismatch", func(o *unstructured.Unstructured) { o.Object["driver"] = "other.csi.example" }},
		{"retained backend snapshot", func(o *unstructured.Unstructured) { o.Object["deletionPolicy"] = "Retain" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, dyn := newGraphStorageClient(t)
			class, err := dyn.Resource(graphSnapshotClassGVR).Get(context.Background(), "ebs-snapshots", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(class)
			if _, err = dyn.Resource(graphSnapshotClassGVR).Update(context.Background(), class, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = PreflightGraphStorage(context.Background(), client, storageGraphFixture(), loadGraphStoragePolicyForTest(t, validGraphStoragePolicy)); err == nil {
				t.Fatal("preflight accepted unsafe VolumeSnapshotClass")
			}
		})
	}
	client, dyn := newGraphStorageClient(t)
	plan, err := PreflightGraphStorage(context.Background(), client, storageGraphFixture(), loadGraphStoragePolicyForTest(t, validGraphStoragePolicy))
	if err != nil {
		t.Fatal(err)
	}
	dyn.PrependReactor("get", "storageclasses", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.(ktesting.GetAction).GetName() != "gp3-shadow" {
			return false, nil, nil
		}
		obj, getErr := dyn.Tracker().Get(schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}, "", "gp3-shadow")
		if getErr != nil {
			return true, nil, getErr
		}
		class := obj.(*unstructured.Unstructured).DeepCopy()
		class.SetUID("replacement-restore-sc-uid")
		return true, class, nil
	})
	if err := checkGraphStoragePlanFresh(context.Background(), client, plan); err == nil {
		t.Fatal("preflight accepted restore StorageClass resourceVersion drift")
	}
}

func TestGraphStorageLifecycleDryRunRejectionCleansGrantBeforeSnapshot(t *testing.T) {
	client, dyn := newGraphStorageClient(t)
	client.Clientset = corefake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: shadowNamespaceName(graphTestRunID), UID: "ns-uid", ResourceVersion: "1", Labels: map[string]string{labelSandbox: "true", labelSession: graphTestRunID}, Annotations: map[string]string{shadowSourceNamespaceAnnotation: "payments"}}})
	dyn.PrependReactor("create", "referencegrants", func(action ktesting.Action) (bool, runtime.Object, error) {
		obj := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		obj.SetUID("grant-uid")
		obj.SetResourceVersion("1")
		return true, obj, nil
	})
	dyn.PrependReactor("create", "persistentvolumeclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "persistentvolumeclaims"}, "fixora-restore", fmt.Errorf("feature gate disabled"))
	})
	plan, err := PreflightGraphStorage(context.Background(), client, storageGraphFixture(), loadGraphStoragePolicyForTest(t, validGraphStoragePolicy))
	if err != nil {
		t.Fatal(err)
	}
	_, err = PrepareGraphStorage(context.Background(), client, plan, shadowNamespaceName(graphTestRunID), graphTestRunID)
	if err == nil {
		t.Fatal("expected API-server dry-run rejection")
	}
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "create" && action.GetResource().Resource == "volumesnapshots" {
			t.Fatal("snapshot was created after dry-run rejection")
		}
	}
	if _, err := dyn.Resource(graphReferenceGrantGVR).Namespace("payments").Get(context.Background(), graphStorageName("fixora-graph-grant", graphTestRunID), metav1.GetOptions{}); err == nil {
		t.Fatal("temporary grant remained after rejected dry-run")
	}
}

func TestGraphStorageLifecycleCreatesExactGrantAndReadySnapshotThenCleansInOrder(t *testing.T) {
	client, dyn := newGraphStorageClient(t)
	client.Clientset = corefake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: shadowNamespaceName(graphTestRunID), UID: "ns-uid", ResourceVersion: "1", Labels: map[string]string{labelSandbox: "true", labelSession: graphTestRunID}, Annotations: map[string]string{shadowSourceNamespaceAnnotation: "payments"}}})
	tracker := dyn.Tracker()
	var snapshotUID types.UID
	dyn.PrependReactor("create", "referencegrants", func(action ktesting.Action) (bool, runtime.Object, error) {
		obj := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		obj.SetUID("grant-uid")
		obj.SetResourceVersion("1")
		if err := tracker.Create(graphReferenceGrantGVR, obj, obj.GetNamespace()); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})
	dyn.PrependReactor("create", "persistentvolumeclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, action.(ktesting.CreateAction).GetObject(), nil
	})
	dyn.PrependReactor("create", "volumesnapshots", func(action ktesting.Action) (bool, runtime.Object, error) {
		obj := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		obj.SetUID("snapshot-uid")
		obj.SetResourceVersion("1")
		snapshotUID = obj.GetUID()
		if err := tracker.Create(graphSnapshotGVR, obj, obj.GetNamespace()); err != nil {
			return true, nil, err
		}
		content := storageUnstructured("snapshot.storage.k8s.io/v1", "VolumeSnapshotContent", "", "content-1", "content-uid", "1", map[string]any{"spec": map[string]any{"driver": "ebs.csi.aws.com", "volumeSnapshotRef": map[string]any{"name": obj.GetName(), "namespace": "payments", "uid": string(snapshotUID)}}})
		if err := tracker.Create(graphSnapshotContentGVR, content, ""); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})
	dyn.PrependReactor("get", "volumesnapshots", func(action ktesting.Action) (bool, runtime.Object, error) {
		obj, err := tracker.Get(graphSnapshotGVR, "payments", action.(ktesting.GetAction).GetName())
		if err != nil {
			return true, nil, err
		}
		snap := obj.(*unstructured.Unstructured).DeepCopy()
		snap.Object["status"] = map[string]any{"readyToUse": true, "boundVolumeSnapshotContentName": "content-1"}
		return true, snap, nil
	})
	dyn.PrependReactor("delete", "volumesnapshots", func(action ktesting.Action) (bool, runtime.Object, error) {
		err := tracker.Delete(graphSnapshotGVR, "payments", action.(ktesting.DeleteAction).GetName())
		_ = tracker.Delete(graphSnapshotContentGVR, "", "content-1")
		return true, nil, err
	})
	plan, err := PreflightGraphStorage(context.Background(), client, storageGraphFixture(), loadGraphStoragePolicyForTest(t, validGraphStoragePolicy))
	if err != nil {
		t.Fatal(err)
	}
	run, err := PrepareGraphStorage(context.Background(), client, plan, shadowNamespaceName(graphTestRunID), graphTestRunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.SourceSnapshots) != 1 || run.SourceSnapshots[0].UID != "snapshot-uid" || len(run.privateContents) != 1 || run.privateContents[0].Name != "content-1" {
		t.Fatalf("snapshot readiness/content identity not recorded: %#v", run)
	}
	grant, err := tracker.Get(graphReferenceGrantGVR, "payments", graphStorageName("fixora-graph-grant", graphTestRunID))
	if err != nil {
		t.Fatal(err)
	}
	spec := graphStorageMap(grant.(*unstructured.Unstructured).Object, "spec")
	from := spec["from"].([]any)
	to := spec["to"].([]any)
	if len(from) != 1 || len(to) != 1 || graphStorageString(from[0].(map[string]any), "namespace") != shadowNamespaceName(graphTestRunID) || graphStorageString(to[0].(map[string]any), "name") != graphSnapshotName(graphTestRunID, "ledger-data") {
		t.Fatalf("ReferenceGrant is not exact: %#v", spec)
	}
	cleanup := run.CleanupSources(context.Background(), client, true)
	if !cleanup.Complete {
		t.Fatalf("cleanup did not confirm removal: %#v", cleanup)
	}
	actions := dyn.Actions()
	grantDelete, snapshotDelete := -1, -1
	for i, action := range actions {
		if action.GetVerb() == "delete" && action.GetResource().Resource == "referencegrants" {
			grantDelete = i
		}
		if action.GetVerb() == "delete" && action.GetResource().Resource == "volumesnapshots" {
			snapshotDelete = i
		}
	}
	if grantDelete < 0 || snapshotDelete < 0 || grantDelete > snapshotDelete {
		t.Fatalf("cleanup order was not grant before snapshot: %#v", actions)
	}
}

func TestGraphStorageLifecycleRejectsSourcePVCDriftWhileSnapshotBecomesReady(t *testing.T) {
	client, dyn := newGraphStorageClient(t)
	client.Clientset = corefake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: shadowNamespaceName(graphTestRunID), UID: "ns-uid", ResourceVersion: "1", Labels: map[string]string{labelSandbox: "true", labelSession: graphTestRunID}, Annotations: map[string]string{shadowSourceNamespaceAnnotation: "payments"}}})
	tracker := dyn.Tracker()
	var snapshotUID types.UID
	dyn.PrependReactor("create", "referencegrants", func(action ktesting.Action) (bool, runtime.Object, error) {
		obj := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		obj.SetUID("grant-uid")
		obj.SetResourceVersion("1")
		if err := tracker.Create(graphReferenceGrantGVR, obj, obj.GetNamespace()); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})
	dyn.PrependReactor("create", "persistentvolumeclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, action.(ktesting.CreateAction).GetObject(), nil
	})
	dyn.PrependReactor("create", "volumesnapshots", func(action ktesting.Action) (bool, runtime.Object, error) {
		obj := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		obj.SetUID("snapshot-uid")
		obj.SetResourceVersion("1")
		snapshotUID = obj.GetUID()
		if err := tracker.Create(graphSnapshotGVR, obj, obj.GetNamespace()); err != nil {
			return true, nil, err
		}
		content := storageUnstructured("snapshot.storage.k8s.io/v1", "VolumeSnapshotContent", "", "content-1", "content-uid", "1", map[string]any{"spec": map[string]any{"driver": "ebs.csi.aws.com", "volumeSnapshotRef": map[string]any{"name": obj.GetName(), "namespace": "payments", "uid": string(snapshotUID)}}})
		if err := tracker.Create(graphSnapshotContentGVR, content, ""); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})
	dyn.PrependReactor("get", "volumesnapshots", func(action ktesting.Action) (bool, runtime.Object, error) {
		obj, err := tracker.Get(graphSnapshotGVR, "payments", action.(ktesting.GetAction).GetName())
		if err != nil {
			return true, nil, err
		}
		// Model a source claim update after snapshot creation but before CSI
		// reports the snapshot ready.
		pvc, err := tracker.Get(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, "payments", "ledger-data")
		if err != nil {
			return true, nil, err
		}
		updatedPVC := pvc.(*unstructured.Unstructured).DeepCopy()
		updatedPVC.SetResourceVersion("99")
		if err := tracker.Update(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, updatedPVC, "payments"); err != nil {
			return true, nil, err
		}
		snap := obj.(*unstructured.Unstructured).DeepCopy()
		snap.Object["status"] = map[string]any{"readyToUse": true, "boundVolumeSnapshotContentName": "content-1"}
		return true, snap, nil
	})
	dyn.PrependReactor("delete", "volumesnapshots", func(action ktesting.Action) (bool, runtime.Object, error) {
		err := tracker.Delete(graphSnapshotGVR, "payments", action.(ktesting.DeleteAction).GetName())
		_ = tracker.Delete(graphSnapshotContentGVR, "", "content-1")
		return true, nil, err
	})
	plan, err := PreflightGraphStorage(context.Background(), client, storageGraphFixture(), loadGraphStoragePolicyForTest(t, validGraphStoragePolicy))
	if err != nil {
		t.Fatal(err)
	}
	run, err := PrepareGraphStorage(context.Background(), client, plan, shadowNamespaceName(graphTestRunID), graphTestRunID)
	if err == nil {
		t.Fatal("snapshot became ready after source PVC drift, but preparation accepted it")
	}
	if run != nil {
		t.Fatalf("drifted source snapshot should be fully cleaned before returning: %#v", run)
	}
	if _, err := tracker.Get(graphSnapshotGVR, "payments", graphSnapshotName(graphTestRunID, "ledger-data")); !apierrors.IsNotFound(err) {
		t.Fatalf("source snapshot remains after drift refusal: %v", err)
	}
	if _, err := tracker.Get(graphReferenceGrantGVR, "payments", graphStorageName("fixora-graph-grant", graphTestRunID)); !apierrors.IsNotFound(err) {
		t.Fatalf("temporary ReferenceGrant remains after drift refusal: %v", err)
	}
}

func TestGraphStorageRunCreatesDistinctRestoredClaimWithExplicitSnapshotSource(t *testing.T) {
	client, dyn := newGraphStorageClient(t)
	client.Clientset = corefake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: shadowNamespaceName(graphTestRunID), UID: "ns-uid", Labels: map[string]string{labelSandbox: "true", labelSession: graphTestRunID}, Annotations: map[string]string{shadowSourceNamespaceAnnotation: "payments"}}})
	snapshot := storageUnstructured("snapshot.storage.k8s.io/v1", "VolumeSnapshot", "payments", graphSnapshotName(graphTestRunID, "ledger-data"), "snap-uid", "4", nil)
	if err := dyn.Tracker().Create(graphSnapshotGVR, snapshot, "payments"); err != nil {
		t.Fatal(err)
	}
	dyn.PrependReactor("create", "persistentvolumeclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
		obj := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		obj.SetUID("restored-pvc-uid")
		obj.SetResourceVersion("1")
		if err := dyn.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, obj, shadowNamespaceName(graphTestRunID)); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})
	plan := &GraphStoragePlan{Namespace: "payments", Policy: loadGraphStoragePolicyForTest(t, validGraphStoragePolicy), PVCs: []graphStoragePVCPlan{{Name: "ledger-data", UID: "source-pvc-uid", PVName: "source-pv", PVUID: "source-pv-uid", SnapshotClassName: "ebs-snapshots", RestoreStorageClassName: "gp3-shadow", Driver: "ebs.csi.aws.com", CapacityBytes: 2 << 30, RequestedBytes: 2 << 30, AccessModes: []string{"ReadWriteOnce"}, VolumeMode: "Filesystem"}}}
	run := &GraphStorageRun{RunID: graphTestRunID, RunNamespace: shadowNamespaceName(graphTestRunID), privatePlan: plan, privateSnapshots: []graphStorageObjectIdentity{{GVR: graphSnapshotGVR, Namespace: "payments", Name: snapshot.GetName(), UID: "snap-uid", ResourceVersion: "4"}}}
	claims, err := run.CreateRestoredClaims(context.Background(), client, shadowNamespaceName(graphTestRunID), graphTestRunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].UID != "restored-pvc-uid" {
		t.Fatalf("restored claim identity missing: %#v", claims)
	}
	obj, err := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(shadowNamespaceName(graphTestRunID)).Get(context.Background(), claims[0].Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec := graphStorageMap(obj.Object, "spec")
	source := graphStorageMap(spec, "dataSourceRef")
	if graphStorageString(source, "name") != snapshot.GetName() || graphStorageString(source, "namespace") != "payments" || graphStorageString(spec, "storageClassName") != "gp3-shadow" || graphStorageString(spec, "volumeMode") != "Filesystem" {
		t.Fatalf("restored claim source or class is wrong: %#v", spec)
	}
	if graphStorageString(spec, "volumeName") != "" || graphStorageString(source, "name") == "source-pv" {
		t.Fatalf("restored PVC reused source PV: %#v", spec)
	}
	if graphStorageString(obj.Object, "status") == "Bound" {
		t.Fatal("WaitForFirstConsumer claim was waited on before Pods existed")
	}
}

func TestGraphStorageRunRewritesOnlyAuthorizedPodPVCVolumesAndReceiptIsSanitized(t *testing.T) {
	root := graphRunWorkload("api", map[string]string{"app": "api"})
	pvc := GraphNode{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "payments", Name: "ledger-data", UID: "pvc-uid", ResourceVersion: "1", Storage: &GraphStorageSource{PVName: "prod-pv", PVUID: "prod-pv-uid", PVResourceVersion: "1", StorageClassName: "gp3", StorageClassUID: "sc", StorageClassResourceVersion: "1", CSIDriver: "ebs.csi.aws.com", AccessModes: []string{"ReadWriteOnce"}, VolumeMode: "Filesystem", RequestedBytes: 1 << 30, Bound: true}}
	plan := &GraphStoragePlan{PVCs: []graphStoragePVCPlan{{Name: "ledger-data"}}}
	run := &GraphStorageRun{RunID: graphTestRunID, privatePlan: plan, RestoredClaims: []GraphNode{{Kind: "PersistentVolumeClaim", Name: graphRestoreClaimName(graphTestRunID, "ledger-data")}}}
	root.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["volumes"] = []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "ledger-data"}}}
	graph := DependencyGraph{Nodes: []GraphNode{root, pvc}, Edges: []GraphEdge{{From: root.ID(), To: pvc.ID(), Type: GraphPVCEdge, SourceVolumeName: "data"}}}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "ledger-data"}}}}}}
	if err := run.RewriteGraphPodClaims(pod, root, graph); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != graphRestoreClaimName(graphTestRunID, "ledger-data") {
		t.Fatalf("pod was not rewritten to the restored claim: %#v", pod.Spec.Volumes)
	}
	bad := pod.DeepCopy()
	bad.Spec.Volumes = append(bad.Spec.Volumes, corev1.Volume{Name: "extra", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "unapproved"}}})
	if err := run.RewriteGraphPodClaims(bad, root, graph); err == nil {
		t.Fatal("candidate-added PVC reference was accepted")
	}
	encoded, err := json.Marshal(GraphClaimReceipt{SourcePVC: "ledger-data", RestoredPVC: "shadow-claim", PV: "pv-shadow", CSIDriver: "ebs.csi.aws.com", Node: "node-a"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "snapshotHandle") || strings.Contains(string(encoded), "volumeData") {
		t.Fatalf("claim receipt leaked backend or storage data: %s", encoded)
	}
}

func TestGraphStorageRunWaitsForScheduledPodAndDelayedPVCBinding(t *testing.T) {
	client, dyn := newGraphStorageClient(t)
	namespace := shadowNamespaceName(graphTestRunID)
	claimName := graphRestoreClaimName(graphTestRunID, "ledger-data")
	root := graphRunWorkload("api", map[string]string{"app": "api"})
	pvcSource := storageGraphFixture().Nodes[1]
	graph := DependencyGraph{Nodes: []GraphNode{root, pvcSource}, Edges: []GraphEdge{{From: root.ID(), To: pvcSource.ID(), Type: GraphPVCEdge, SourceVolumeName: "data"}}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-shadow", Namespace: namespace, UID: "pod-uid", Annotations: map[string]string{"fixora.io/source-resource": root.ID()}}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName}}}}}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "node-uid", Labels: map[string]string{"topology.kubernetes.io/zone": "zone-a"}}}
	client.Clientset = corefake.NewSimpleClientset(&pod, node)
	pvc := storageUnstructured("v1", "PersistentVolumeClaim", namespace, claimName, "restore-pvc-uid", "1", map[string]any{"metadata": map[string]any{"name": claimName, "namespace": namespace, "uid": "restore-pvc-uid", "resourceVersion": "1", "labels": map[string]any{"fixora.io/graph-run": graphTestRunID}}, "spec": map[string]any{"storageClassName": "gp3-shadow", "accessModes": []any{"ReadWriteOnce"}, "volumeMode": "Filesystem", "resources": map[string]any{"requests": map[string]any{"storage": "2Gi"}}, "dataSourceRef": map[string]any{"apiGroup": "snapshot.storage.k8s.io", "kind": "VolumeSnapshot", "name": graphSnapshotName(graphTestRunID, "ledger-data"), "namespace": "payments"}}, "status": map[string]any{"phase": "Pending"}})
	if err := dyn.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, pvc, namespace); err != nil {
		t.Fatal(err)
	}
	plan := &GraphStoragePlan{Namespace: "payments", Policy: loadGraphStoragePolicyForTest(t, validGraphStoragePolicy), PVCs: []graphStoragePVCPlan{{Name: "ledger-data", UID: "pvc-uid", ResourceVersion: "2", PVName: "pv-ledger", PVUID: "pv-uid", PVResourceVersion: "4", StorageClassName: "gp3", StorageClassUID: "sc-uid", StorageClassResourceVersion: "3", SnapshotClassName: "ebs-snapshots", SnapshotClassUID: "vsc-uid", SnapshotClassResourceVersion: "6", RestoreStorageClassName: "gp3-shadow", RestoreStorageClassUID: "restore-sc-uid", RestoreStorageClassResourceVersion: "5", Driver: "ebs.csi.aws.com", RequestedBytes: 2 << 30, CapacityBytes: 2 << 30, AccessModes: []string{"ReadWriteOnce"}, VolumeMode: "Filesystem"}}}
	run := &GraphStorageRun{RunID: graphTestRunID, RunNamespace: namespace, privatePlan: plan, RestoredClaims: []GraphNode{{Kind: "PersistentVolumeClaim", Namespace: namespace, Name: claimName, UID: "restore-pvc-uid", ResourceVersion: "1"}}}
	go func() {
		time.Sleep(40 * time.Millisecond)
		scheduled, _ := client.Clientset.CoreV1().Pods(namespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
		scheduled.Spec.NodeName = node.Name
		_, _ = client.Clientset.CoreV1().Pods(namespace).Update(context.Background(), scheduled, metav1.UpdateOptions{})
		bound := pvc.DeepCopy()
		bound.SetResourceVersion("2")
		bound.Object["spec"].(map[string]any)["volumeName"] = "restored-pv"
		bound.Object["status"] = map[string]any{"phase": "Bound"}
		_ = dyn.Tracker().Update(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, bound, namespace)
		pv := storageUnstructured("v1", "PersistentVolume", "", "restored-pv", "restored-pv-uid", "4", map[string]any{"spec": map[string]any{"storageClassName": "gp3-shadow", "csi": map[string]any{"driver": "ebs.csi.aws.com"}, "claimRef": map[string]any{"namespace": namespace, "name": claimName, "uid": "restore-pvc-uid"}, "nodeAffinity": map[string]any{"required": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{map[string]any{"key": "topology.kubernetes.io/zone", "operator": "In", "values": []any{"zone-a"}}}}}}}}, "status": map[string]any{"capacity": map[string]any{"storage": "2Gi"}}})
		_ = dyn.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}, pv, "")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	receipts, err := run.VerifyClaimScheduling(ctx, client, graph, []corev1.Pod{pod})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].Node != "node-a" || receipts[0].PV != "restored-pv" {
		t.Fatalf("wrong delayed binding receipt: %#v", receipts)
	}
}

func unstructuredPtrs(objects []*unstructured.Unstructured) []runtime.Object {
	out := make([]runtime.Object, len(objects))
	for i, obj := range objects {
		out[i] = obj
	}
	return out
}

func newGraphStorageClient(t *testing.T) (*kube.TypedClient, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	objects := []*unstructured.Unstructured{
		storageUnstructured("v1", "PersistentVolumeClaim", "payments", "ledger-data", "pvc-uid", "2", map[string]any{"spec": map[string]any{"volumeName": "pv-ledger", "storageClassName": "gp3", "accessModes": []any{"ReadWriteOnce"}, "volumeMode": "Filesystem", "resources": map[string]any{"requests": map[string]any{"storage": "2Gi"}}}, "status": map[string]any{"phase": "Bound"}}),
		storageUnstructured("v1", "PersistentVolume", "", "pv-ledger", "pv-uid", "4", map[string]any{"spec": map[string]any{"storageClassName": "gp3", "csi": map[string]any{"driver": "ebs.csi.aws.com"}, "claimRef": map[string]any{"namespace": "payments", "name": "ledger-data", "uid": "pvc-uid"}}, "status": map[string]any{"phase": "Bound", "capacity": map[string]any{"storage": "2Gi"}}}),
		storageUnstructured("storage.k8s.io/v1", "StorageClass", "", "gp3", "sc-uid", "3", map[string]any{"provisioner": "ebs.csi.aws.com"}),
		storageUnstructured("storage.k8s.io/v1", "StorageClass", "", "gp3-shadow", "restore-sc-uid", "5", map[string]any{"provisioner": "ebs.csi.aws.com"}),
		storageUnstructured("snapshot.storage.k8s.io/v1", "VolumeSnapshotClass", "", "ebs-snapshots", "vsc-uid", "6", map[string]any{"driver": "ebs.csi.aws.com", "deletionPolicy": "Delete"}),
	}
	lists := map[schema.GroupVersionResource]string{{Version: "v1", Resource: "persistentvolumeclaims"}: "PersistentVolumeClaimList", {Version: "v1", Resource: "persistentvolumes"}: "PersistentVolumeList", {Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}: "StorageClassList", graphSnapshotClassGVR: "VolumeSnapshotClassList", graphSnapshotGVR: "VolumeSnapshotList", graphSnapshotContentGVR: "VolumeSnapshotContentList", graphReferenceGrantGVR: "ReferenceGrantList"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, unstructuredPtrs(objects)...)
	resources := map[string]*metav1.APIResourceList{"snapshot.storage.k8s.io/v1": {APIResources: []metav1.APIResource{{Name: "volumesnapshots"}, {Name: "volumesnapshotclasses"}, {Name: "volumesnapshotcontents"}}}, "gateway.networking.k8s.io/v1beta1": {APIResources: []metav1.APIResource{{Name: "referencegrants"}}}}
	return &kube.TypedClient{Dynamic: dyn, Discovery: graphStorageDiscovery{resources: resources}}, dyn
}

func storageGraphFixture() DependencyGraph {
	return DependencyGraph{Root: "Deployment/payments/api", Nodes: []GraphNode{{Kind: "Deployment", Namespace: "payments", Name: "api"}, {Kind: "PersistentVolumeClaim", Namespace: "payments", Name: "ledger-data", UID: "pvc-uid", ResourceVersion: "2", Storage: &GraphStorageSource{PVName: "pv-ledger", PVUID: "pv-uid", PVResourceVersion: "4", StorageClassName: "gp3", StorageClassUID: "sc-uid", StorageClassResourceVersion: "3", CSIDriver: "ebs.csi.aws.com", AccessModes: []string{"ReadWriteOnce"}, VolumeMode: "Filesystem", RequestedBytes: 2 << 30, Bound: true}}}}
}

func storageUnstructured(apiVersion, kind, namespace, name, uid, rv string, fields map[string]any) *unstructured.Unstructured {
	obj := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name, "uid": uid, "resourceVersion": rv}}
	if namespace != "" {
		obj["metadata"].(map[string]any)["namespace"] = namespace
	}
	for key, value := range fields {
		obj[key] = value
	}
	return &unstructured.Unstructured{Object: obj}
}
