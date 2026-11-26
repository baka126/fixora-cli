package shadow

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

const labelGraph = "fixora.io/graph-run"

func graphNetworkPolicy(namespace, runID, dnsNamespace string, dnsSelector map[string]string) (*networkingv1.NetworkPolicy, error) {
	if namespace == "" || runID == "" || dnsNamespace == "" || len(dnsSelector) == 0 {
		return nil, fmt.Errorf("shadow namespace, run identity, and exact DNS namespace/pod selector are required")
	}
	sameRunPods := metav1.LabelSelector{MatchLabels: map[string]string{labelSandbox: "true", labelSession: runID, labelGraph: runID}}
	sameNamespace := metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace}}
	dnsNamespaceSelector := metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": dnsNamespace}}
	dnsPodSelector := metav1.LabelSelector{MatchLabels: cloneLabels(dnsSelector)}
	port := func(protocol corev1.Protocol) networkingv1.NetworkPolicyPort {
		port := intstr.FromInt(53)
		protocolCopy := protocol
		return networkingv1.NetworkPolicyPort{Protocol: &protocolCopy, Port: &port}
	}
	labels := map[string]string{labelSandbox: "true", labelSession: runID, "fixora.io/control": "graph-access"}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "fixora-graph-access", Namespace: namespace, Labels: labels},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: sameRunPods,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &sameNamespace, PodSelector: &sameRunPods}}}},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &sameNamespace, PodSelector: &sameRunPods}}},
				{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &dnsNamespaceSelector, PodSelector: &dnsPodSelector}}, Ports: []networkingv1.NetworkPolicyPort{port(corev1.ProtocolUDP), port(corev1.ProtocolTCP)}},
			},
		},
	}, nil
}

func ensureGraphNetworkPolicy(ctx context.Context, client *kube.TypedClient, expected *networkingv1.NetworkPolicy) error {
	if client == nil || client.Clientset == nil || expected == nil || expected.Namespace == "" || expected.Name == "" || expected.UID == "" {
		return fmt.Errorf("graph NetworkPolicy identity is incomplete")
	}
	current, err := client.Clientset.NetworkingV1().NetworkPolicies(expected.Namespace).Get(ctx, expected.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("graph NetworkPolicy disappeared")
		}
		return fmt.Errorf("inspect graph NetworkPolicy failed")
	}
	if current.UID != expected.UID || current.Namespace != expected.Namespace || current.Name != expected.Name || !apiequality.Semantic.DeepEqual(current.Labels, expected.Labels) || !apiequality.Semantic.DeepEqual(current.Spec, expected.Spec) {
		return fmt.Errorf("graph NetworkPolicy identity or rules changed")
	}
	return nil
}

func createGraphNetworkPolicy(ctx context.Context, client *kube.TypedClient, expected *networkingv1.NetworkPolicy) (*networkingv1.NetworkPolicy, error) {
	if client == nil || client.Clientset == nil || expected == nil {
		return nil, fmt.Errorf("typed Kubernetes client and graph NetworkPolicy are required")
	}
	created, err := client.Clientset.NetworkingV1().NetworkPolicies(expected.Namespace).Create(ctx, expected.DeepCopy(), metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create graph NetworkPolicy failed")
	}
	if created == nil || created.UID == "" {
		return nil, fmt.Errorf("API server did not return graph NetworkPolicy identity")
	}
	if err := ensureGraphNetworkPolicy(ctx, client, created); err != nil {
		return nil, err
	}
	return created.DeepCopy(), nil
}

func discoverGraphDNSTarget(ctx context.Context, client *kube.TypedClient) (string, map[string]string, error) {
	if client == nil || client.Clientset == nil {
		return "", nil, fmt.Errorf("typed Kubernetes client is required to identify cluster DNS")
	}
	const namespace, serviceName = "kube-system", "kube-dns"
	service, err := client.Clientset.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("cluster DNS Service could not be identified")
	}
	if service.Spec.Type == corev1.ServiceTypeExternalName || service.Spec.ClusterIP == corev1.ClusterIPNone || len(service.Spec.Selector) == 0 {
		return "", nil, fmt.Errorf("cluster DNS Service does not have a supported Pod selector")
	}
	udp, tcp := false, false
	for _, port := range service.Spec.Ports {
		if port.Port != 53 {
			continue
		}
		if port.Protocol == corev1.ProtocolUDP {
			udp = true
		}
		if port.Protocol == corev1.ProtocolTCP {
			tcp = true
		}
	}
	if !udp || !tcp {
		return "", nil, fmt.Errorf("cluster DNS Service does not expose both UDP and TCP port 53")
	}
	pods, err := client.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.Set(service.Spec.Selector).AsSelector().String()})
	if err != nil {
		return "", nil, fmt.Errorf("cluster DNS endpoints could not be inspected")
	}
	ready := 0
	for _, pod := range pods.Items {
		if podReady(&pod) && strings.TrimSpace(pod.Status.PodIP) != "" {
			ready++
		}
	}
	if ready == 0 {
		return "", nil, fmt.Errorf("cluster DNS has no Ready Pod endpoint")
	}
	return namespace, cloneLabels(service.Spec.Selector), nil
}

func proveGraphNetworkPolicy(ctx context.Context, client *kube.TypedClient, namespace, session, image string, denyPolicy *networkingv1.NetworkPolicy, dnsNamespace string, dnsSelector map[string]string) (report IsolationReport, graphPolicy *networkingv1.NetworkPolicy, retErr error) {
	report = IsolationReport{Status: string(VerificationInconclusive)}
	if client == nil || client.Clientset == nil || denyPolicy == nil {
		return report, nil, gateFailure{VerificationBlocked, fmt.Errorf("default-deny identity is required for graph isolation proof")}
	}
	if err := ensureSandboxNetworkPolicy(ctx, client, denyPolicy); err != nil {
		return report, nil, gateFailure{VerificationBlocked, fmt.Errorf("default-deny policy changed before graph isolation proof")}
	}
	resources, err := client.Discovery.ServerPreferredResources()
	if err != nil {
		return report, nil, gateFailure{VerificationBlocked, fmt.Errorf("NetworkPolicy API inventory is incomplete")}
	}
	witnessName := namespace + "-graph-witness"
	witness, err := client.CreateNamespace(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: witnessName, Labels: map[string]string{labelSandbox: "true", labelSession: session},
		Annotations: map[string]string{"fixora.io/control": "graph-network-witness"},
	}})
	if err != nil {
		return report, nil, gateFailure{VerificationBlocked, fmt.Errorf("create graph isolation witness namespace failed")}
	}
	if witness == nil || witness.UID == "" {
		return report, nil, gateFailure{VerificationBlocked, fmt.Errorf("graph witness namespace identity is incomplete")}
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cleanupErr := deleteOwnedNamespace(cleanupCtx, client, witness); cleanupErr != nil {
			report.Status = string(VerificationBlocked)
			report.Summary = "graph isolation witness cleanup failed"
			retErr = gateFailure{VerificationBlocked, fmt.Errorf("graph isolation witness cleanup failed")}
		}
	}()
	active, err := activeUnsupportedNetworkPolicies(ctx, client, resources, namespace, witness.Name)
	if err != nil || len(active) > 0 {
		return report, nil, gateFailure{VerificationBlocked, fmt.Errorf("NetworkPolicy inventory is incomplete or includes unsupported policy types")}
	}
	var graphListener, witnessListener *corev1.Pod
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, pod := range []*corev1.Pod{graphListener, witnessListener} {
			if pod != nil {
				if cleanupErr := deleteOwnedPod(cleanupCtx, client, pod); cleanupErr != nil && retErr == nil {
					report.Status = string(VerificationBlocked)
					report.Summary = "graph connectivity witness cleanup failed"
					retErr = gateFailure{VerificationBlocked, fmt.Errorf("graph connectivity witness cleanup failed")}
				}
			}
		}
	}()
	graphListener = isolationListenerPod(namespace, session, image)
	graphListener.Labels[labelGraph] = session
	graphListener, err = client.CreatePod(ctx, graphListener)
	if err != nil {
		return report, nil, gateFailure{VerificationBlocked, fmt.Errorf("create in-graph connectivity witness failed")}
	}
	witnessListener, err = client.CreatePod(ctx, isolationListenerPod(witness.Name, session, image))
	if err != nil {
		return report, nil, gateFailure{VerificationBlocked, fmt.Errorf("create external connectivity witness failed")}
	}
	if err := waitPodReady(ctx, client, graphListener); err != nil {
		return report, nil, gateFailure{VerificationInconclusive, fmt.Errorf("in-graph witness did not become Ready")}
	}
	if err := waitPodReady(ctx, client, witnessListener); err != nil {
		return report, nil, gateFailure{VerificationInconclusive, fmt.Errorf("external witness did not become Ready")}
	}
	expected, err := graphNetworkPolicy(namespace, session, dnsNamespace, dnsSelector)
	if err != nil {
		return report, nil, gateFailure{VerificationInconclusive, err}
	}
	graphPolicy, err = createGraphNetworkPolicy(ctx, client, expected)
	if err != nil {
		return report, nil, gateFailure{VerificationBlocked, err}
	}
	if err := runIsolationProbeScoped(ctx, client, namespace, session, image, graphListener, true, true); err != nil {
		return report, graphPolicy, gateFailure{VerificationInconclusive, fmt.Errorf("same-run connectivity was not proven")}
	}
	if err := runIsolationProbeScoped(ctx, client, namespace, session, image, witnessListener, false, true); err != nil {
		return report, graphPolicy, gateFailure{VerificationBlocked, fmt.Errorf("graph egress reached an external witness")}
	}
	if err := runIsolationProbeScoped(ctx, client, witness.Name, session, image, graphListener, false, false); err != nil {
		return report, graphPolicy, gateFailure{VerificationBlocked, fmt.Errorf("external ingress reached the graph")}
	}
	if err := ensureGraphPolicySet(ctx, client, denyPolicy, graphPolicy); err != nil {
		return report, graphPolicy, gateFailure{VerificationBlocked, fmt.Errorf("default-deny policy changed during graph isolation proof")}
	}
	if err := ensureGraphNetworkPolicy(ctx, client, graphPolicy); err != nil {
		return report, graphPolicy, gateFailure{VerificationBlocked, err}
	}
	report.Status = string(VerificationVerified)
	report.Summary = "same-run traffic is allowed while external egress and ingress remain denied"
	report.Checks = []string{"same-run TCP connectivity passed", "external egress denied", "external ingress denied", "DNS limited to verified cluster DNS Pods"}
	return report, graphPolicy, nil
}

func ensureGraphPolicySet(ctx context.Context, client *kube.TypedClient, deny, allow *networkingv1.NetworkPolicy) error {
	if client == nil || client.Clientset == nil || deny == nil || allow == nil {
		return fmt.Errorf("graph NetworkPolicy set is incomplete")
	}
	items, err := client.Clientset.NetworkingV1().NetworkPolicies(deny.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("inspect graph NetworkPolicy set failed")
	}
	if len(items.Items) != 2 {
		return fmt.Errorf("shadow namespace does not contain exactly the default-deny and graph NetworkPolicies")
	}
	if err := ensureSandboxNetworkPolicy(ctx, client, deny); err != nil {
		// Once the graph policy exists, validate the deny policy's exact object
		// directly because the legacy helper intentionally requires a single policy.
		var found *networkingv1.NetworkPolicy
		for i := range items.Items {
			if items.Items[i].Name == deny.Name {
				found = &items.Items[i]
				break
			}
		}
		if found == nil || found.UID != deny.UID || !isShadowObject(found.Labels) || found.Labels[labelSession] != deny.Labels[labelSession] || len(found.Spec.Ingress) != 0 || len(found.Spec.Egress) != 0 || !apiequality.Semantic.DeepEqual(found.Spec.PolicyTypes, deny.Spec.PolicyTypes) || !apiequality.Semantic.DeepEqual(found.Spec.PodSelector, deny.Spec.PodSelector) {
			return fmt.Errorf("default-deny NetworkPolicy identity or rules changed")
		}
	}
	if err := ensureGraphNetworkPolicy(ctx, client, allow); err != nil {
		return err
	}
	return nil
}
