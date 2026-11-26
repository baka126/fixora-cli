package shadow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// GraphRequest describes a bounded connected shadow run. Graph node objects and
// patches are process-local inputs and are never included in GraphResult.
type GraphRequest struct {
	Graph            DependencyGraph
	Candidates       map[string]GraphCandidate
	Timeout          time.Duration
	ProbeImage       string
	Keep             bool
	StoragePlan      *GraphStoragePlan
	StorageConfirmed bool
	Progress         ProgressFunc
}

type GraphCandidate struct {
	Strategy string
	Patch    string
	Proof    *repairpolicy.Proof
	Before   repairpolicy.Snapshot
}

type GraphWorkloadResult struct {
	Source             string                `json:"source"`
	Pod                string                `json:"pod"`
	PodUID             string                `json:"podUid,omitempty"`
	PodResourceVersion string                `json:"podResourceVersion,omitempty"`
	Verified           bool                  `json:"verified"`
	Status             VerificationStatus    `json:"status"`
	Message            string                `json:"message,omitempty"`
	PolicyReceipt      *repairpolicy.Receipt `json:"-"`
}

// GraphResult is metadata-only. It intentionally contains no rendered object,
// patch, logs, ConfigMap values, or Secret values.
type GraphResult struct {
	Verified                 bool                          `json:"verified"`
	Status                   VerificationStatus            `json:"status"`
	Namespace                string                        `json:"namespace,omitempty"`
	NamespaceUID             string                        `json:"namespaceUid,omitempty"`
	NamespaceResourceVersion string                        `json:"namespaceResourceVersion,omitempty"`
	GraphDigest              string                        `json:"graphDigest,omitempty"`
	Workloads                []GraphWorkloadResult         `json:"workloads,omitempty"`
	Dependencies             []GraphNode                   `json:"dependencies,omitempty"`
	SourceDependencies       []GraphNode                   `json:"sourceDependencies,omitempty"`
	Services                 []GraphNode                   `json:"services,omitempty"`
	NetworkPolicies          []GraphNode                   `json:"networkPolicies,omitempty"`
	Blockers                 []string                      `json:"blockers,omitempty"`
	Cleanup                  []string                      `json:"cleanup,omitempty"`
	CleanupStatus            string                        `json:"cleanupStatus,omitempty"`
	HasSecrets               bool                          `json:"hasSecrets"`
	StorageSnapshots         []GraphNode                   `json:"storageSnapshots,omitempty"`
	StorageSnapshotReceipts  []GraphStorageSnapshotReceipt `json:"storageSnapshotReceipts,omitempty"`
	StorageGrant             *GraphNode                    `json:"storageGrant,omitempty"`
	StorageSources           []GraphNode                   `json:"storageSources,omitempty"`
	StoragePolicyDigest      string                        `json:"storagePolicyDigest,omitempty"`
	RestoredClaims           []GraphNode                   `json:"restoredClaims,omitempty"`
	ClaimReceipts            []GraphClaimReceipt           `json:"claimReceipts,omitempty"`
	StorageCleanup           *GraphStorageCleanup          `json:"storageCleanup,omitempty"`
	HasPersistentData        bool                          `json:"hasPersistentData"`
}

// RunGraph builds one connected candidate graph in one run-owned namespace.
// Certification is withheld unless every cloned workload and Service route
// passes. Secret-bearing runs always clean up, regardless of Keep.
func RunGraph(ctx context.Context, client *kube.TypedClient, req GraphRequest) (result GraphResult, retErr error) {
	result.Status = VerificationBlocked
	if client == nil || client.Clientset == nil {
		return result, fmt.Errorf("combined shadow requires typed Kubernetes client access")
	}
	if len(req.Graph.Blockers) != 0 {
		result.Blockers = append([]string(nil), req.Graph.Blockers...)
		return result, nil
	}
	graphPVCs := 0
	for _, node := range req.Graph.Nodes {
		if node.Kind == "PersistentVolumeClaim" {
			graphPVCs++
		}
	}
	if graphPVCs > 0 && (!req.StorageConfirmed || req.StoragePlan == nil) {
		result.Blockers = []string{"PVC-backed shadow requires a separately confirmed storage plan"}
		return result, nil
	}
	if graphPVCs == 0 && (req.StoragePlan != nil || req.StorageConfirmed) {
		result.Blockers = []string{"storage snapshot plan does not match a PVC-backed graph"}
		return result, nil
	}
	if graphPVCs > 0 {
		if client.Dynamic == nil || req.StoragePlan.Policy == nil || len(req.StoragePlan.PVCs) != graphPVCs || req.StoragePlan.Namespace != graphSourceNamespace(req.Graph) {
			result.Blockers = []string{"storage plan does not match the complete source PVC graph"}
			return result, nil
		}
		if err := req.StoragePlan.Policy.CheckFresh(); err != nil {
			result.Blockers = []string{"storage policy changed before the graph shadow began"}
			return result, nil
		}
		if err := checkGraphStoragePlanFresh(ctx, client, req.StoragePlan); err != nil {
			result.Blockers = []string{"source PVC or storage class changed before the graph shadow began"}
			return result, nil
		}
		for _, node := range req.Graph.Nodes {
			if node.Kind != "PersistentVolumeClaim" {
				continue
			}
			matched := false
			for _, p := range req.StoragePlan.PVCs {
				if p.Name == node.Name && p.UID == node.UID && p.ResourceVersion == node.ResourceVersion {
					matched = true
				}
			}
			if !matched {
				result.Blockers = []string{"storage plan source identities changed"}
				return result, nil
			}
		}
	}
	if req.Timeout <= 0 {
		req.Timeout = 10 * time.Minute
	}
	runCtx, cancelRun := context.WithTimeout(ctx, req.Timeout)
	defer cancelRun()
	ctx = runCtx
	if strings.TrimSpace(req.ProbeImage) == "" {
		req.ProbeImage = os.Getenv("FIXORA_SHADOW_PROBE_IMAGE")
	}
	if err := validateProbeImage(req.ProbeImage); err != nil {
		result.Status = VerificationInconclusive
		result.Blockers = []string{err.Error()}
		return result, nil
	}
	if err := validateGraphRequest(req.Graph); err != nil {
		result.Blockers = []string{err.Error()}
		return result, nil
	}
	for _, node := range req.Graph.Nodes {
		if !graphWorkload(node.Kind) {
			continue
		}
		candidate, provided := req.Candidates[node.ID()]
		if len(req.Candidates) > 0 && (!provided || candidate.Patch != node.CandidatePatch) {
			result.Blockers = []string{"graph candidate set changed before shadow"}
			return result, nil
		}
		if candidate.Proof != nil {
			if err := candidate.Proof.Check(candidate.Before, candidate.Patch); err != nil {
				return result, fmt.Errorf("policy proof for %s: %w", node.ID(), err)
			}
			if err := repairpolicy.Revalidate(ctx, client, candidate.Proof, candidate.Patch); err != nil {
				return result, fmt.Errorf("policy proof for %s is stale: %w", node.ID(), err)
			}
		}
	}
	if err := validateGraphSourceFreshness(ctx, client, req.Graph); err != nil {
		result.Blockers = []string{err.Error()}
		return result, nil
	}
	deps, err := SnapshotGraphDependencies(ctx, client, req.Graph)
	if err != nil {
		result.Blockers = []string{err.Error()}
		return result, nil
	}
	result.HasSecrets = deps.HasSecrets

	runID := uuid.NewString()
	nsName := shadowNamespaceName(runID)
	ns, err := client.CreateNamespace(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        nsName,
		Labels:      map[string]string{labelSandbox: "true", labelSession: runID},
		Annotations: map[string]string{shadowSourceNamespaceAnnotation: graphSourceNamespace(req.Graph)},
	}})
	if err != nil {
		if recovered, getErr := client.GetNamespace(context.Background(), nsName); getErr == nil && recovered.Labels[labelSandbox] == "true" && recovered.Labels[labelSession] == runID {
			if recovered.UID == "" {
				return result, fmt.Errorf("namespace create response was ambiguous and cleanup identity has no UID")
			}
			if cleanupErr := deleteGraphNamespace(context.Background(), client, recovered); cleanupErr != nil {
				return result, fmt.Errorf("namespace create failed and cleanup could not be confirmed")
			}
		}
		return result, fmt.Errorf("create combined shadow namespace: %w", err)
	}
	if ns == nil || ns.UID == "" || ns.ResourceVersion == "" {
		return result, fmt.Errorf("shadow namespace identity is incomplete")
	}
	result.Namespace, result.NamespaceUID, result.NamespaceResourceVersion = ns.Name, string(ns.UID), ns.ResourceVersion
	var storageRun *GraphStorageRun
	storageUsed := graphPVCs > 0
	defer func() {
		if shouldRetainGraphNamespace(req.Keep, deps.HasSecrets, result.Verified, retErr, storageUsed) {
			result.CleanupStatus = "retained"
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if err := deleteGraphNamespace(cleanupCtx, client, ns); err != nil {
			result.CleanupStatus = "failed"
			result.Verified = false
			result.Status = VerificationBlocked
			result.Blockers = appendUnique(result.Blockers, "shadow namespace cleanup could not be confirmed")
			if retErr == nil {
				retErr = fmt.Errorf("combined shadow cleanup failed")
			}
			if storageRun != nil {
				cleanup := storageRun.CleanupSources(context.Background(), client, false)
				result.StorageCleanup = &cleanup
			}
			return
		}
		result.Cleanup = appendUnique(result.Cleanup, "deleted namespace")
		result.CleanupStatus = "deleted"
		if storageRun != nil {
			cleanup := storageRun.CleanupSources(context.Background(), client, true)
			result.StorageCleanup = &cleanup
			if !cleanup.Complete {
				result.CleanupStatus = "failed"
				result.Verified = false
				result.Status = VerificationBlocked
				result.Blockers = appendUnique(result.Blockers, "source storage snapshot cleanup could not be confirmed")
				if retErr == nil {
					retErr = fmt.Errorf("combined storage cleanup failed")
				}
			}
		}
	}()

	// The graph runner fails closed until both policy behavior and the DNS path
	// are proven for this cluster/CNI.
	basePolicy := sandboxNetworkPolicy(ns.Name, "fixora-default-deny", runID)
	basePolicy.Spec.PodSelector = metav1.LabelSelector{MatchLabels: map[string]string{labelSandbox: "true", labelSession: runID}}
	basePolicy, err = client.Clientset.NetworkingV1().NetworkPolicies(ns.Name).Create(ctx, basePolicy, metav1.CreateOptions{})
	if err != nil || basePolicy == nil || basePolicy.UID == "" || basePolicy.ResourceVersion == "" {
		return graphBlocked(result, "could not create default-deny NetworkPolicy"), nil
	}
	result.NetworkPolicies = append(result.NetworkPolicies, graphObjectIdentity("networking.k8s.io/v1", "NetworkPolicy", basePolicy))
	dnsNamespace, dnsSelector, err := discoverGraphDNSTarget(ctx, client)
	if err != nil {
		return graphBlocked(result, "cluster DNS could not be verified"), nil
	}
	// Prove the policy pair before creating candidate pods. Runtime graph probes
	// include a connected in-namespace test and blocked outside probes.
	if _, _, err := proveGraphNetworkPolicy(ctx, client, ns.Name, runID, req.ProbeImage, basePolicy, dnsNamespace, dnsSelector); err != nil {
		return graphBlocked(result, "graph network isolation could not be proven"), nil
	}
	graphPolicy, err := client.Clientset.NetworkingV1().NetworkPolicies(ns.Name).Get(ctx, "fixora-graph-access", metav1.GetOptions{})
	if err != nil || graphPolicy == nil || graphPolicy.UID == "" || graphPolicy.ResourceVersion == "" {
		return graphBlocked(result, "graph traffic policy identity could not be read"), nil
	}
	result.NetworkPolicies = append(result.NetworkPolicies, graphObjectIdentity("networking.k8s.io/v1", "NetworkPolicy", graphPolicy))
	if graphPVCs > 0 {
		storageRun, err = PrepareGraphStorage(ctx, client, req.StoragePlan, ns.Name, runID)
		if err != nil {
			return graphBlocked(result, "CSI snapshot preparation could not be safely completed"), nil
		}
		result.StorageSnapshots = append([]GraphNode(nil), storageRun.SourceSnapshots...)
		result.StorageSnapshotReceipts = append([]GraphStorageSnapshotReceipt(nil), storageRun.SnapshotReceipts...)
		result.StorageGrant = &storageRun.GrantIdentity
		result.StoragePolicyDigest = req.StoragePlan.PolicyDigest
		result.HasPersistentData = true
		for _, node := range req.Graph.Nodes {
			if node.Kind == "PersistentVolumeClaim" {
				result.StorageSources = append(result.StorageSources, node)
			}
		}
		claims, claimErr := storageRun.CreateRestoredClaims(ctx, client, ns.Name, runID)
		if claimErr != nil {
			result.RestoredClaims = append([]GraphNode(nil), claims...)
			return graphBlocked(result, "restored PVC could not be safely created"), nil
		}
		result.RestoredClaims = claims
	}

	created, err := CreateGraphDependencies(ctx, client, ns.Name, runID, deps)
	if err != nil {
		return graphBlocked(result, err.Error()), nil
	}
	result.SourceDependencies = append([]GraphNode(nil), deps.Dependencies...)
	result.Dependencies = created.Dependencies
	for _, dependency := range result.Dependencies {
		if dependency.UID == "" || dependency.ResourceVersion == "" {
			return graphBlocked(result, "isolated dependency identity is incomplete"), nil
		}
	}
	for _, node := range req.Graph.Nodes {
		switch node.Kind {
		case "Service":
			service, err := shadowService(node, ns.Name, runID, req.Graph)
			if err != nil {
				return graphBlocked(result, err.Error()), nil
			}
			createdService, err := client.Clientset.CoreV1().Services(ns.Name).Create(ctx, service, metav1.CreateOptions{})
			if err != nil || createdService == nil || createdService.UID == "" || createdService.ResourceVersion == "" {
				return graphBlocked(result, "shadow Service could not be created"), nil
			}
			result.Services = append(result.Services, GraphNode{APIVersion: "v1", Kind: "Service", Namespace: ns.Name, Name: createdService.Name, UID: string(createdService.UID), ResourceVersion: createdService.ResourceVersion})
		}
	}

	type pendingWorkload struct {
		source  string
		pod     *corev1.Pod
		created *corev1.Pod
	}
	var pending []pendingWorkload
	for i := range req.Graph.Nodes {
		node := req.Graph.Nodes[i]
		if !graphWorkload(node.Kind) {
			continue
		}
		pod, sourceName, err := graphWorkloadPod(node, runID, ns.Name, req.Timeout)
		if err != nil {
			return graphBlocked(result, "workload candidate could not be prepared"), nil
		}
		if err := applyPatchToPod(pod, node.CandidatePatch); err != nil {
			return graphBlocked(result, "workload candidate patch is invalid"), nil
		}
		allowedSecrets := graphSecretNames(req.Graph)
		if err := validateGraphCandidatePod(pod, allowedSecrets); err != nil {
			return graphBlocked(result, "workload candidate uses an unsupported or unsafe runtime dependency"), nil
		}
		pod.Labels[labelGraph] = runID
		pod.Labels[labelSession] = runID
		pod.Labels[labelSandbox] = "true"
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations["fixora.io/source-resource"] = node.ID()
		if storageRun != nil {
			if err := storageRun.RewriteGraphPodClaims(pod, node, req.Graph); err != nil {
				return graphBlocked(result, "candidate PVC references are not authorized by the source graph"), nil
			}
		}
		for _, service := range req.Graph.Nodes {
			if service.Kind == "Service" && graphHasEdge(req.Graph, service.ID(), node.ID(), GraphServiceEdge) {
				pod.Labels[graphServiceLabel(service)] = runID
			}
		}
		pod.Name = cloneName(sourceName, runID)
		pod.Namespace = ns.Name
		if _, err := client.Clientset.CoreV1().Pods(ns.Name).Create(ctx, pod.DeepCopy(), metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
			return graphBlocked(result, "candidate Pod failed API admission dry-run"), nil
		}
		createdPod, err := client.CreatePod(ctx, pod)
		if err != nil || createdPod == nil || createdPod.UID == "" || createdPod.ResourceVersion == "" {
			return graphBlocked(result, "shadow workload could not be created"), nil
		}
		pending = append(pending, pendingWorkload{source: node.ID(), pod: pod, created: createdPod})
	}
	if storageRun != nil {
		scheduledPods := make([]corev1.Pod, 0, len(pending))
		for _, item := range pending {
			scheduledPods = append(scheduledPods, *item.created)
		}
		receipts, verifyErr := storageRun.VerifyClaimScheduling(ctx, client, req.Graph, scheduledPods)
		if verifyErr != nil {
			return graphBlocked(result, "restored PVC binding or topology could not be verified after Pod scheduling"), nil
		}
		result.ClaimReceipts = receipts
	}
	for _, pendingPod := range pending {
		checkCtx, cancel := context.WithTimeout(ctx, req.Timeout)
		attempt := verifyCloneWithSourceProgress(checkCtx, client, ns.Name, pendingPod.created.Name, req.Timeout, 1, false, pendingPod.pod, nil, req.Progress)
		cancel()
		workload := GraphWorkloadResult{Source: pendingPod.source, Pod: pendingPod.created.Name, PodUID: string(pendingPod.created.UID), PodResourceVersion: pendingPod.created.ResourceVersion, Verified: attempt.Ready, Status: statusForAttempt(attempt), Message: conciseGraphAttempt(attempt)}
		stabilityPassed := attempt.Ready
		candidate := req.Candidates[pendingPod.source]
		if workload.Verified && candidate.Proof != nil {
			podNow, getErr := client.GetPod(ctx, ns.Name, pendingPod.created.Name)
			if getErr == nil {
				switch candidate.Strategy {
				case "restore-command":
					workload.PolicyReceipt, getErr = repairpolicy.VerifyStartup(ctx, client, candidate.Proof, &podNow, candidate.Patch)
				case "restore-placement":
					workload.PolicyReceipt, getErr = repairpolicy.VerifyPlacement(ctx, client, candidate.Proof, &podNow, candidate.Patch)
				case "security-strengthen":
					workload.PolicyReceipt, getErr = repairpolicy.VerifySecurity(ctx, client, candidate.Proof, &podNow, candidate.Patch)
				default:
					workload.PolicyReceipt, getErr = repairpolicy.VerifyCommon(ctx, client, candidate.Proof, &podNow, candidate.Patch)
				}
			}
			if getErr != nil || workload.PolicyReceipt == nil {
				workload.Verified = false
				workload.Status = VerificationFailed
				workload.Message = "candidate policy checks did not pass"
			}
		}
		if req.Progress != nil {
			attempt.Ready = workload.Verified
			attempt.Inconclusive = workload.Status == VerificationInconclusive
			phase := ProgressFailed
			if attempt.Inconclusive {
				phase = ProgressInconclusive
			} else if attempt.Ready {
				phase = ProgressVerified
			}
			event := progressEventForAttempt(1, phase, 0, soakWindow(false), soakWindow(false), attempt, attempt.Evidence, nil)
			event.StabilityPassed = stabilityPassed
			req.Progress(event)
		}
		result.Workloads = append(result.Workloads, workload)
		if !attempt.Ready {
			return graphBlocked(result, "one or more shadow workloads did not remain Ready"), nil
		}
	}
	if err := verifyGraphServiceEndpoints(ctx, client, ns.Name, runID, req.Graph, result.Workloads, req.Timeout); err != nil {
		return graphBlocked(result, "one or more shadow Service routes did not reach the expected Pods"), nil
	}
	if err := ensureGraphPolicySet(ctx, client, basePolicy, graphPolicy); err != nil {
		return graphBlocked(result, "NetworkPolicy identity changed during graph verification"), nil
	}
	if err := verifyGraphNamespaceInventory(ctx, client, ns.Name, runID, result.Workloads, result.Dependencies, result.Services, result.RestoredClaims); err != nil {
		return graphBlocked(result, "shadow namespace contains unexpected or changed objects"), nil
	}
	result.Verified = true
	result.Status = VerificationVerified
	result.GraphDigest = graphDigest(req.Graph)
	return result, nil
}

func shouldRetainGraphNamespace(keep, hasSecrets, verified bool, runErr error, hasPersistentData bool) bool {
	if hasPersistentData {
		return false
	}
	return shouldKeepGraphNamespace(keep, hasSecrets, verified, runErr)
}

func shouldKeepGraphNamespace(keep, hasSecrets, verified bool, runErr error) bool {
	return keep && !hasSecrets && verified && runErr == nil
}

func validateGraphRequest(graph DependencyGraph) error {
	workloads := 0
	for _, node := range graph.Nodes {
		if graphWorkload(node.Kind) {
			workloads++
			if strings.TrimSpace(node.CandidatePatch) == "" {
				return fmt.Errorf("workload %s has no candidate patch", node.ID())
			}
			if node.Object == nil || node.UID == "" || node.ResourceVersion == "" {
				return fmt.Errorf("workload %s has incomplete source snapshot", node.ID())
			}
		} else if node.Kind == "PersistentVolumeClaim" {
			if !eligibleGraphPVC(node, "source-volume") {
				return fmt.Errorf("PVC %s has incomplete or unsupported storage identity", node.Name)
			}
		} else if node.Kind != "ConfigMap" && node.Kind != "Secret" && node.Kind != "Service" {
			return fmt.Errorf("unsupported graph resource %s", node.Kind)
		}
	}
	for _, node := range graph.Nodes {
		if node.Kind == "Service" {
			if _, err := shadowService(node, "fixora-shadow-validate", graphSessionForValidation(), graph); err != nil {
				return fmt.Errorf("Service %s is unsupported for combined shadow", node.Name)
			}
		}
	}
	if workloads < 2 {
		return fmt.Errorf("combined shadow requires at least two workload candidates")
	}
	return nil
}

func graphSessionForValidation() string { return "12345678-1234-4234-8234-123456789abc" }

func graphWorkloadPod(node GraphNode, session, namespace string, timeout time.Duration) (*corev1.Pod, string, error) {
	var pod corev1.Pod
	b, err := json.Marshal(node.Object)
	if err != nil {
		return nil, "", err
	}
	switch node.Kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		var obj struct {
			Spec struct {
				Template corev1.PodTemplateSpec `json:"template"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(b, &obj); err != nil {
			return nil, "", err
		}
		pod = *podFromTemplate(node.Namespace, node.Name, node.Kind, obj.Spec.Template)
	default:
		return nil, "", fmt.Errorf("unsupported workload kind")
	}
	clone := sanitizePod(&pod, session, timeout)
	clone.Labels[labelGraph] = session
	clone.Namespace = namespace
	clone.Name = cloneName(node.Name, session)
	clone.Spec.ServiceAccountName = ""
	return clone, node.Name, nil
}

func shadowService(node GraphNode, namespace, runID string, graph DependencyGraph) (*corev1.Service, error) {
	var source corev1.Service
	b, err := json.Marshal(node.Object)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &source); err != nil {
		return nil, err
	}
	if source.Spec.Type != "" && source.Spec.Type != corev1.ServiceTypeClusterIP {
		return nil, fmt.Errorf("unsupported Service type")
	}
	if source.Spec.ClusterIP == corev1.ClusterIPNone || len(source.Spec.Selector) == 0 || len(source.Spec.Ports) == 0 || source.Spec.PublishNotReadyAddresses {
		return nil, fmt.Errorf("unsupported selectorless or headless Service")
	}
	for i := range source.Spec.Ports {
		protocol := source.Spec.Ports[i].Protocol
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		if protocol != corev1.ProtocolTCP {
			return nil, fmt.Errorf("combined graph shadow supports TCP Service ports only")
		}
		source.Spec.Ports[i].NodePort = 0
	}
	selector := map[string]string{graphServiceLabel(node): runID}
	backendCount := 0
	for _, backend := range graph.Nodes {
		if !graphWorkload(backend.Kind) || !graphSelectorMatches(anyLabels(source.Spec.Selector), graphWorkloadLabels(backend)) {
			continue
		}
		backendCount++
	}
	if backendCount == 0 {
		return nil, fmt.Errorf("Service has no graph backend")
	}
	source.ObjectMeta = metav1.ObjectMeta{Name: node.Name, Namespace: namespace, Labels: map[string]string{labelSandbox: "true", labelSession: runID}}
	source.Spec.ClusterIP = ""
	source.Spec.ClusterIPs = nil
	source.Spec.HealthCheckNodePort = 0
	source.Spec.ExternalName = ""
	source.Spec.Selector = selector
	return &source, nil
}

func anyLabels(source map[string]string) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func verifyGraphServiceEndpoints(ctx context.Context, client *kube.TypedClient, namespace, runID string, graph DependencyGraph, workloads []GraphWorkloadResult, timeout time.Duration) error {
	serviceCount := 0
	for _, n := range graph.Nodes {
		if n.Kind == "Service" {
			serviceCount++
		}
	}
	if serviceCount == 0 {
		return nil
	}
	for _, node := range graph.Nodes {
		if node.Kind != "Service" {
			continue
		}
		service, err := client.Clientset.CoreV1().Services(namespace).Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if service.Namespace != namespace || service.Spec.Selector[graphServiceLabel(node)] != runID || len(service.Spec.Selector) != 1 {
			return fmt.Errorf("shadow Service selector changed")
		}
		want := map[string]bool{}
		for _, edge := range graph.Edges {
			if edge.From == node.ID() && edge.Type == GraphServiceEdge {
				want[edge.To] = true
			}
		}
		if len(want) == 0 {
			return fmt.Errorf("Service endpoint set is incomplete")
		}
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		pollCtx, cancel := context.WithTimeout(ctx, timeout)
		for {
			slices, listErr := client.Clientset.DiscoveryV1().EndpointSlices(namespace).List(pollCtx, metav1.ListOptions{LabelSelector: labels.Set{"kubernetes.io/service-name": service.Name}.AsSelector().String()})
			if listErr != nil {
				cancel()
				return fmt.Errorf("read shadow Service endpoints failed")
			}
			ready := map[string]bool{}
			for _, slice := range slices.Items {
				for _, endpoint := range slice.Endpoints {
					if endpoint.TargetRef == nil || endpoint.TargetRef.Kind != "Pod" || endpoint.TargetRef.Namespace != namespace || endpoint.Conditions.Ready == nil || !*endpoint.Conditions.Ready {
						continue
					}
					pod, podErr := client.GetTypedPod(pollCtx, namespace, endpoint.TargetRef.Name)
					if podErr != nil || string(pod.UID) != string(endpoint.TargetRef.UID) || !graphServiceSlicePortsMatch(service.Spec.Ports, slice.Ports, pod) {
						continue
					}
					for _, workload := range workloads {
						if workload.Pod == endpoint.TargetRef.Name && workload.PodUID == string(endpoint.TargetRef.UID) && want[workload.Source] {
							ready[workload.Source] = true
						}
					}
				}
			}
			if len(ready) == len(want) {
				cancel()
				break
			}
			select {
			case <-pollCtx.Done():
				cancel()
				return fmt.Errorf("Service endpoint set is incomplete")
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	return nil
}

func graphServiceSlicePortsMatch(servicePorts []corev1.ServicePort, endpointPorts []discoveryv1.EndpointPort, pod *corev1.Pod) bool {
	if len(servicePorts) == 0 || len(endpointPorts) != len(servicePorts) || pod == nil {
		return false
	}
	want := map[string]bool{}
	for _, servicePort := range servicePorts {
		protocol := servicePort.Protocol
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		port := servicePort.Port
		switch servicePort.TargetPort.Type {
		case intstr.String:
			resolved := int32(0)
			for _, container := range pod.Spec.Containers {
				for _, containerPort := range container.Ports {
					containerProtocol := containerPort.Protocol
					if containerProtocol == "" {
						containerProtocol = corev1.ProtocolTCP
					}
					if containerPort.Name == servicePort.TargetPort.StrVal && containerProtocol == protocol {
						if resolved != 0 && resolved != containerPort.ContainerPort {
							return false
						}
						resolved = containerPort.ContainerPort
					}
				}
			}
			if resolved == 0 {
				return false
			}
			port = resolved
		case intstr.Int:
			if servicePort.TargetPort.IntVal != 0 {
				port = servicePort.TargetPort.IntVal
			}
		}
		if port <= 0 {
			return false
		}
		want[endpointPortKey(servicePort.Name, protocol, port)] = true
	}
	got := map[string]bool{}
	for _, endpointPort := range endpointPorts {
		if endpointPort.Port == nil || *endpointPort.Port <= 0 {
			return false
		}
		name := ""
		if endpointPort.Name != nil {
			name = *endpointPort.Name
		}
		protocol := corev1.ProtocolTCP
		if endpointPort.Protocol != nil && *endpointPort.Protocol != "" {
			protocol = *endpointPort.Protocol
		}
		got[endpointPortKey(name, protocol, *endpointPort.Port)] = true
	}
	if len(want) != len(got) {
		return false
	}
	for key := range want {
		if !got[key] {
			return false
		}
	}
	return true
}

func endpointPortKey(name string, protocol corev1.Protocol, port int32) string {
	return fmt.Sprintf("%s/%s/%d", name, protocol, port)
}

func conciseGraphAttempt(attempt Attempt) string {
	if attempt.Ready {
		return "Ready and stable"
	}
	if attempt.Inconclusive {
		return "verification inconclusive"
	}
	return "not Ready or unstable"
}

func graphBlocked(result GraphResult, blocker string) GraphResult {
	result.Verified = false
	result.Status = VerificationBlocked
	result.Blockers = appendUnique(result.Blockers, blocker)
	return result
}

func graphSourceNamespace(graph DependencyGraph) string {
	for _, n := range graph.Nodes {
		if graphWorkload(n.Kind) {
			return n.Namespace
		}
	}
	return ""
}

func graphObjectIdentity(apiVersion, kind string, object metav1.Object) GraphNode {
	return GraphNode{APIVersion: apiVersion, Kind: kind, Namespace: object.GetNamespace(), Name: object.GetName(), UID: string(object.GetUID()), ResourceVersion: object.GetResourceVersion()}
}

func graphHasEdge(graph DependencyGraph, from, to string, edgeType GraphEdgeType) bool {
	for _, edge := range graph.Edges {
		if edge.From == from && edge.To == to && edge.Type == edgeType {
			return true
		}
	}
	return false
}

func graphSecretNames(graph DependencyGraph) map[string]bool {
	result := map[string]bool{}
	for _, node := range graph.Nodes {
		if node.Kind == "Secret" {
			result[node.Name] = true
		}
	}
	return result
}

func validateGraphCandidatePod(pod *corev1.Pod, allowedSecrets map[string]bool) error {
	if err := validateShadowSourceWithSecretDependencies(pod, allowedSecrets); err != nil {
		return err
	}
	if len(pod.Spec.ImagePullSecrets) != 0 {
		return fmt.Errorf("imagePullSecrets are outside combined shadow scope")
	}
	if pod.Spec.AutomountServiceAccountToken != nil && *pod.Spec.AutomountServiceAccountToken {
		return fmt.Errorf("service-account token automount is not allowed in graph shadows")
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.Projected == nil {
			continue
		}
		for _, source := range volume.Projected.Sources {
			if source.ServiceAccountToken != nil || source.DownwardAPI != nil {
				return fmt.Errorf("projected identity and downward API sources are outside graph shadow scope")
			}
		}
	}
	return nil
}

func graphServiceLabel(service GraphNode) string {
	sum := sha256.Sum256([]byte(service.Namespace + "/" + service.Name))
	return "fixora.io/svc-" + hex.EncodeToString(sum[:6])
}

func graphDigest(graph DependencyGraph) string {
	var parts []string
	for _, n := range graph.Nodes {
		parts = append(parts, n.ID()+"@"+n.UID+":"+n.ResourceVersion+"#"+n.CandidatePatch)
		if n.Storage != nil {
			parts = append(parts, n.Storage.DigestString())
		}
	}
	for _, e := range graph.Edges {
		parts = append(parts, e.From+">"+string(e.Type)+">"+e.To+"#"+e.SourceVolumeName)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

func verifyGraphNamespaceInventory(ctx context.Context, client *kube.TypedClient, namespace, runID string, workloads []GraphWorkloadResult, dependencies, services []GraphNode, storageClaims ...[]GraphNode) error {
	if client == nil || client.Clientset == nil {
		return fmt.Errorf("typed client unavailable")
	}
	expectedPods := map[string]string{}
	for _, workload := range workloads {
		expectedPods[workload.Pod] = workload.PodUID
	}
	pods, err := client.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(pods.Items) != len(expectedPods) {
		return fmt.Errorf("Pod inventory mismatch")
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Labels[labelSandbox] != "true" || pod.Labels[labelSession] != runID || pod.Labels[labelGraph] != runID || expectedPods[pod.Name] == "" || expectedPods[pod.Name] != string(pod.UID) {
			return fmt.Errorf("unexpected Pod identity")
		}
	}
	expectedServices := map[string]string{}
	for _, service := range services {
		expectedServices[service.Name] = service.UID
	}
	actualServices, err := client.Clientset.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(actualServices.Items) != len(expectedServices) {
		return fmt.Errorf("Service inventory mismatch")
	}
	for i := range actualServices.Items {
		item := &actualServices.Items[i]
		if item.Labels[labelSession] != runID || expectedServices[item.Name] == "" || expectedServices[item.Name] != string(item.UID) {
			return fmt.Errorf("unexpected Service identity")
		}
	}
	expectedConfigMaps, expectedSecrets := map[string]string{}, map[string]string{}
	for _, dep := range dependencies {
		if dep.Kind == "ConfigMap" {
			expectedConfigMaps[dep.Name] = dep.UID
		}
		if dep.Kind == "Secret" {
			expectedSecrets[dep.Name] = dep.UID
		}
	}
	configMaps, err := client.Clientset.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(configMaps.Items) != len(expectedConfigMaps) {
		return fmt.Errorf("ConfigMap inventory mismatch")
	}
	for i := range configMaps.Items {
		item := &configMaps.Items[i]
		if item.Labels[labelSession] != runID || expectedConfigMaps[item.Name] == "" || expectedConfigMaps[item.Name] != string(item.UID) {
			return fmt.Errorf("unexpected ConfigMap identity")
		}
	}
	secrets, err := client.Clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(secrets.Items) != len(expectedSecrets) {
		return fmt.Errorf("Secret inventory mismatch")
	}
	for i := range secrets.Items {
		item := &secrets.Items[i]
		if item.Labels[labelSession] != runID || expectedSecrets[item.Name] == "" || expectedSecrets[item.Name] != string(item.UID) {
			return fmt.Errorf("unexpected Secret identity")
		}
	}
	if len(storageClaims) > 0 && len(storageClaims[0]) > 0 {
		expectedClaims := map[string]string{}
		for _, claim := range storageClaims[0] {
			expectedClaims[claim.Name] = claim.UID
		}
		claims, err := client.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil || len(claims.Items) != len(expectedClaims) {
			return fmt.Errorf("restored PVC inventory mismatch")
		}
		for i := range claims.Items {
			item := &claims.Items[i]
			if item.GetLabels()["fixora.io/graph-run"] != runID || expectedClaims[item.GetName()] == "" || expectedClaims[item.GetName()] != string(item.GetUID()) {
				return fmt.Errorf("unexpected restored PVC identity")
			}
		}
	}
	return nil
}

func deleteGraphNamespace(ctx context.Context, client *kube.TypedClient, expected *corev1.Namespace) error {
	if expected == nil || expected.UID == "" {
		return fmt.Errorf("namespace UID is required")
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return DeleteCleanupCandidate(cleanupCtx, client, CleanupCandidate{
		Namespace: expected.Name, UID: string(expected.UID), Session: expected.Labels[labelSession],
		SourceNamespace: expected.Annotations[shadowSourceNamespaceAnnotation],
	})
}

func validateGraphSourceFreshness(ctx context.Context, client *kube.TypedClient, graph DependencyGraph) error {
	for _, node := range graph.Nodes {
		var uid, rv string
		switch node.Kind {
		case "Deployment":
			obj, err := client.Clientset.AppsV1().Deployments(node.Namespace).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("could not recheck workload %s", node.Name)
			}
			uid, rv = string(obj.UID), obj.ResourceVersion
		case "StatefulSet":
			obj, err := client.Clientset.AppsV1().StatefulSets(node.Namespace).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("could not recheck workload %s", node.Name)
			}
			uid, rv = string(obj.UID), obj.ResourceVersion
		case "DaemonSet":
			obj, err := client.Clientset.AppsV1().DaemonSets(node.Namespace).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("could not recheck workload %s", node.Name)
			}
			uid, rv = string(obj.UID), obj.ResourceVersion
		case "PersistentVolumeClaim":
			obj, err := client.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(node.Namespace).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("could not recheck source PVC %s", node.Name)
			}
			uid, rv = string(obj.GetUID()), obj.GetResourceVersion()
		case "Service":
			obj, err := client.Clientset.CoreV1().Services(node.Namespace).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("could not recheck Service %s", node.Name)
			}
			uid, rv = string(obj.UID), obj.ResourceVersion
		default:
			continue // ConfigMaps and Secrets are checked by SnapshotGraphDependencies.
		}
		if uid != node.UID || rv != node.ResourceVersion {
			return fmt.Errorf("source %s changed after graph discovery", node.ID())
		}
	}
	return nil
}
