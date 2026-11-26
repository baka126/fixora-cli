//go:build e2e

package e2e_graphstorage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/clientcmd"
)

const graphStorageTestImage = "public.ecr.aws/docker/library/busybox:1.36"

func TestCombinedGraphPVCSnapshotRestoreAndCleanup(t *testing.T) {
	if os.Getenv("FIXORA_GRAPH_STORAGE_E2E") != "1" {
		t.Skip("set FIXORA_GRAPH_STORAGE_E2E=1 to run disposable-cluster CSI acceptance")
	}
	contextName := strings.TrimSpace(os.Getenv("FIXORA_GRAPH_STORAGE_CONTEXT"))
	snapshotClassName := strings.TrimSpace(os.Getenv("FIXORA_GRAPH_STORAGE_SNAPSHOT_CLASS"))
	restoreClassName := strings.TrimSpace(os.Getenv("FIXORA_GRAPH_STORAGE_RESTORE_CLASS"))
	probeImage := strings.TrimSpace(os.Getenv("FIXORA_GRAPH_STORAGE_PROBE_IMAGE"))
	for name, value := range map[string]string{"FIXORA_GRAPH_STORAGE_CONTEXT": contextName, "FIXORA_GRAPH_STORAGE_SNAPSHOT_CLASS": snapshotClassName, "FIXORA_GRAPH_STORAGE_RESTORE_CLASS": restoreClassName, "FIXORA_GRAPH_STORAGE_PROBE_IMAGE": probeImage} {
		if value == "" {
			t.Fatalf("%s is required when disposable-cluster acceptance is enabled", name)
		}
	}
	rawConfig, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil || rawConfig.CurrentContext != contextName {
		t.Fatalf("explicit context %q must be the current kube context for this acceptance run", contextName)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	client, err := kube.NewRequiredTypedClient(contextName, "graph PVC snapshot acceptance")
	if err != nil {
		t.Fatal(err)
	}
	if client.Config == nil || client.Dynamic == nil || client.Clientset == nil || client.Discovery == nil {
		t.Fatal("the explicitly selected context did not initialize all required Kubernetes clients")
	}

	// Check every cluster-wide prerequisite before creating a namespace, PVC, or
	// any other acceptance resource. A failed prerequisite should have no
	// Kubernetes side effects.
	storageClass, err := client.Clientset.StorageV1().StorageClasses().Get(ctx, restoreClassName, metav1.GetOptions{})
	if err != nil || storageClass.Provisioner == "" {
		t.Fatalf("read restore CSI StorageClass %q before creating acceptance resources: %v", restoreClassName, err)
	}
	requiredAPIs := map[string][]string{
		"snapshot.storage.k8s.io/v1":        {"volumesnapshots", "volumesnapshotcontents", "volumesnapshotclasses"},
		"gateway.networking.k8s.io/v1beta1": {"referencegrants"},
	}
	for groupVersion, requiredResources := range requiredAPIs {
		resourceList, err := client.Discovery.ServerResourcesForGroupVersion(groupVersion)
		if err != nil {
			t.Fatalf("required API %s is unavailable; acceptance stopped before creating resources: %v", groupVersion, err)
		}
		available := make(map[string]bool, len(resourceList.APIResources))
		for _, resource := range resourceList.APIResources {
			available[resource.Name] = true
		}
		for _, requiredResource := range requiredResources {
			if !available[requiredResource] {
				t.Fatalf("required API resource %s/%s is unavailable; acceptance stopped before creating resources", groupVersion, requiredResource)
			}
		}
	}
	snapshotClassGVR := schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotclasses"}
	snapshotClass, err := client.Dynamic.Resource(snapshotClassGVR).Get(ctx, snapshotClassName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read VolumeSnapshotClass %q before creating acceptance resources: %v", snapshotClassName, err)
	}
	if snapshotClass.Object["driver"] != storageClass.Provisioner || snapshotClass.Object["deletionPolicy"] != "Delete" {
		t.Fatalf("snapshot class must match CSI driver %q and use Delete policy", storageClass.Provisioner)
	}

	namespace := fmt.Sprintf("fixora-graph-storage-%d", time.Now().UnixNano())
	ns, err := client.Clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{"fixora.io/e2e": "graph-storage"}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create disposable namespace: %v", err)
	}
	cleanupSourceNamespace := func() error {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cleanupCancel()
		uid := ns.UID
		err := client.Clientset.CoreV1().Namespaces().Delete(cleanupCtx, namespace, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		if err != nil && !apiNotFound(err) {
			return err
		}
		return wait.PollUntilContextTimeout(cleanupCtx, time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
			_, err := client.Clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
			return apiNotFound(err), nil
		})
	}
	t.Cleanup(func() {
		if err := cleanupSourceNamespace(); err != nil {
			t.Errorf("delete disposable source namespace: %v", err)
		}
	})

	volumeMode := corev1.PersistentVolumeFilesystem
	claim, err := client.Clientset.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "graph-data"}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &restoreClassName, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, VolumeMode: &volumeMode, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create disposable source PVC: %v", err)
	}
	if err := waitForPVCBound(ctx, client, namespace, claim.Name); err != nil {
		t.Fatal(err)
	}
	claim, err = client.Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, claim.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writerName := "graph-data-seed"
	seedPod := dataPod(namespace, writerName, claim.Name, "echo fixora-storage-sentinel > /data/sentinel")
	if _, err := client.Clientset.CoreV1().Pods(namespace).Create(ctx, seedPod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create sentinel writer Pod: %v", err)
	}
	if err := waitForPodPhase(ctx, client, namespace, writerName, corev1.PodSucceeded); err != nil {
		t.Fatal(err)
	}
	_ = client.Clientset.CoreV1().Pods(namespace).Delete(ctx, writerName, metav1.DeleteOptions{})
	if err := waitForPodGone(ctx, client, namespace, writerName); err != nil {
		t.Fatal(err)
	}

	shared := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "graph-shared"}, Data: map[string]string{"MODE": "acceptance"}}
	shared, err = client.Clientset.CoreV1().ConfigMaps(namespace).Create(ctx, shared, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	api := sourceDeployment(namespace, "graph-api", "api", "exit 1", &claim.Name, shared.Name)
	api.Spec.Replicas = &zero
	api, err = client.Clientset.AppsV1().Deployments(namespace).Create(ctx, api, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	worker := sourceDeployment(namespace, "graph-worker", "worker", "exit 1", nil, shared.Name)
	worker.Spec.Replicas = &zero
	worker, err = client.Clientset.AppsV1().Deployments(namespace).Create(ctx, worker, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	pv, err := client.Clientset.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	graphStorageClass := storageClass

	policyText := fmt.Sprintf(`apiVersion: fixora.io/v1alpha1
kind: GraphStorageShadowPolicy
metadata:
  name: graph-storage-acceptance
  namespace: %s
spec:
  maxTotalLogicalCapacity: 2Gi
  pvcs:
  - name: %s
    volumeSnapshotClassName: %s
    restoreStorageClassName: %s
    maxLogicalCapacity: 2Gi
    allowCrashConsistent: true
`, namespace, claim.Name, snapshotClassName, restoreClassName)
	policyPath := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(policyPath, []byte(policyText), 0o600); err != nil {
		t.Fatal(err)
	}
	authority, err := shadow.LoadGraphStoragePolicy(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	pvcNode := graphNodeFromPVC(claim, pv, graphStorageClass, snapshotClassName, restoreClassName, authority)
	apiNode := graphNodeFromDeployment(api)
	workerNode := graphNodeFromDeployment(worker)
	sharedNode := graphNodeFromConfigMap(shared)
	graph, err := shadow.BuildDependencyGraphWithOptions(apiNode.ID(), []shadow.GraphNode{apiNode, workerNode, sharedNode, pvcNode}, shadow.GraphBuildOptions{StoragePolicy: authority})
	if err != nil || len(graph.Blockers) != 0 {
		t.Fatalf("build connected acceptance graph: graph=%#v err=%v", graph, err)
	}
	apiPatch := "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        command: [\"sh\", \"-c\", \"test -s /data/sentinel && sleep 3600\"]\n"
	workerPatch := "spec:\n  template:\n    spec:\n      containers:\n      - name: worker\n        command: [\"sh\", \"-c\", \"sleep 3600\"]\n"
	candidates := map[string]shadow.GraphCandidate{}
	for i := range graph.Nodes {
		switch graph.Nodes[i].Name {
		case "graph-api":
			graph.Nodes[i].CandidatePatch = apiPatch
			candidates[graph.Nodes[i].ID()] = shadow.GraphCandidate{Patch: apiPatch}
		case "graph-worker":
			graph.Nodes[i].CandidatePatch = workerPatch
			candidates[graph.Nodes[i].ID()] = shadow.GraphCandidate{Patch: workerPatch}
		}
	}
	storagePlan, err := shadow.PreflightGraphStorage(ctx, client, graph, authority)
	if err != nil {
		t.Fatalf("read-only storage preflight: %v", err)
	}
	if err := shadow.BindGraphStoragePlan(&graph, storagePlan); err != nil {
		t.Fatal(err)
	}
	result, err := shadow.RunGraph(ctx, client, shadow.GraphRequest{Graph: graph, Candidates: candidates, Timeout: 12 * time.Minute, ProbeImage: probeImage, StoragePlan: storagePlan, StorageConfirmed: true})
	if err != nil || !result.Verified || result.Status != shadow.VerificationVerified {
		t.Fatalf("connected snapshot/restore graph verification failed: verified=%v status=%s blockers=%v err=%v", result.Verified, result.Status, result.Blockers, err)
	}
	if result.CleanupStatus != "deleted" || result.StorageCleanup == nil || !result.StorageCleanup.Complete || len(result.StorageCleanup.Remaining) != 0 {
		t.Fatalf("PVC graph left incomplete cleanup: %#v", result.StorageCleanup)
	}
	if len(result.ClaimReceipts) != 1 || result.ClaimReceipts[0].PV == pv.Name || result.ClaimReceipts[0].Node == "" {
		t.Fatalf("restore receipt must prove a distinct PV and selected node: %#v", result.ClaimReceipts)
	}
	if _, err := client.Clientset.CoreV1().Namespaces().Get(ctx, result.Namespace, metav1.GetOptions{}); !apiNotFound(err) {
		t.Fatalf("PVC-backed shadow namespace %q remains after run cleanup", result.Namespace)
	}
	grantGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1beta1", Resource: "referencegrants"}
	if result.StorageGrant == nil {
		t.Fatal("run did not return a temporary ReferenceGrant receipt")
	}
	if _, err := client.Dynamic.Resource(grantGVR).Namespace(namespace).Get(ctx, result.StorageGrant.Name, metav1.GetOptions{}); !apiNotFound(err) {
		t.Fatalf("temporary ReferenceGrant %q remains after run cleanup", result.StorageGrant.Name)
	}
	if err := verifySourceSentinel(ctx, client, namespace, claim.Name); err != nil {
		t.Fatalf("source snapshot test data changed: %v", err)
	}
	for _, receipt := range result.StorageSnapshotReceipts {
		if _, err := client.Dynamic.Resource(schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshots"}).Namespace(namespace).Get(ctx, receipt.Snapshot.Name, metav1.GetOptions{}); !apiNotFound(err) {
			t.Fatalf("source VolumeSnapshot %q remains after cleanup", receipt.Snapshot.Name)
		}
		_, err := client.Dynamic.Resource(schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotcontents"}).Get(ctx, receipt.Content.Name, metav1.GetOptions{})
		if !apiNotFound(err) {
			t.Fatalf("CSI snapshot content %q remains after cleanup", receipt.Content.Name)
		}
	}
	remaining, err := client.Dynamic.Resource(schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshots"}).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: "fixora.io/graph-run"})
	if err != nil || len(remaining.Items) != 0 {
		t.Fatalf("run-owned source snapshots remain: count=%d err=%v", len(remaining.Items), err)
	}
	if err := cleanupSourceNamespace(); err != nil {
		t.Fatalf("delete disposable source namespace and its test workloads/claim: %v", err)
	}
}

func graphNodeFromDeployment(deployment *appsv1.Deployment) shadow.GraphNode {
	obj, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(deployment)
	return shadow.GraphNode{APIVersion: "apps/v1", Kind: "Deployment", Namespace: deployment.Namespace, Name: deployment.Name, UID: string(deployment.UID), ResourceVersion: deployment.ResourceVersion, Object: obj}
}

func graphNodeFromConfigMap(configMap *corev1.ConfigMap) shadow.GraphNode {
	obj, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(configMap)
	return shadow.GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: configMap.Namespace, Name: configMap.Name, UID: string(configMap.UID), ResourceVersion: configMap.ResourceVersion, Object: obj}
}

func graphNodeFromPVC(pvc *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume, sc *storagev1.StorageClass, snapshotClass, restoreClass string, authority *shadow.GraphStorageAuthority) shadow.GraphNode {
	obj, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(pvc)
	requested := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	return shadow.GraphNode{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: pvc.Namespace, Name: pvc.Name, UID: string(pvc.UID), ResourceVersion: pvc.ResourceVersion, Object: obj, Storage: &shadow.GraphStorageSource{PVName: pv.Name, PVUID: string(pv.UID), PVResourceVersion: pv.ResourceVersion, StorageClassName: sc.Name, StorageClassUID: string(sc.UID), StorageClassResourceVersion: sc.ResourceVersion, CSIDriver: sc.Provisioner, AccessModes: []string{"ReadWriteOnce"}, VolumeMode: "Filesystem", RequestedBytes: requested.Value(), Bound: true, VolumeSnapshotClassName: snapshotClass, RestoreStorageClassName: restoreClass, MaxLogicalBytes: 2 << 30}}
}

func sourceDeployment(namespace, name, container, command string, claimName *string, configMapName string) *appsv1.Deployment {
	labels := map[string]string{"app": name}
	containerSpec := corev1.Container{Name: container, Image: graphStorageTestImage, Command: []string{"sh", "-c", command}, EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: configMapName}}}}}
	volumes := []corev1.Volume{}
	if claimName != nil {
		containerSpec.VolumeMounts = []corev1.VolumeMount{{Name: "data", MountPath: "/data"}}
		volumes = append(volumes, corev1.Volume{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: *claimName}}})
	}
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{containerSpec}, Volumes: volumes}}}}
}

func dataPod(namespace, name, claim, command string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "data", Image: graphStorageTestImage, Command: []string{"sh", "-c", command}, VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}}}}, Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}}}}}
}

func verifySourceSentinel(ctx context.Context, client *kube.TypedClient, namespace, claim string) error {
	name := "graph-data-verify"
	pod := dataPod(namespace, name, claim, "test \"$(cat /data/sentinel)\" = fixora-storage-sentinel")
	pod.Spec.Containers[0].VolumeMounts[0].ReadOnly = true
	if _, err := client.Clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create source read-only verifier: %w", err)
	}
	return waitForPodPhase(ctx, client, namespace, name, corev1.PodSucceeded)
}

func waitForPVCBound(ctx context.Context, client *kube.TypedClient, namespace, name string) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		pvc, err := client.Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		return err == nil && pvc.Status.Phase == corev1.ClaimBound, nil
	})
}

func waitForPodPhase(ctx context.Context, client *kube.TypedClient, namespace, name string, phase corev1.PodPhase) error {
	return wait.PollUntilContextTimeout(ctx, 3*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		pod, err := client.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		return err == nil && pod.Status.Phase == phase, nil
	})
}

func waitForPodGone(ctx context.Context, client *kube.TypedClient, namespace, name string) error {
	return wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := client.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		return apiNotFound(err), nil
	})
}

func apiNotFound(err error) bool { return apierrors.IsNotFound(err) }
