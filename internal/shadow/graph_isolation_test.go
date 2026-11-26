package shadow

import (
	"context"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corefake "k8s.io/client-go/kubernetes/fake"
)

func TestGraphNetworkPolicyAllowsSameRunOnly(t *testing.T) {
	policy, err := graphNetworkPolicy("shadow", "run-123", "kube-system", map[string]string{"k8s-app": "kube-dns"})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Spec.PodSelector.MatchLabels[labelSession] != "run-123" || policy.Spec.PodSelector.MatchLabels[labelGraph] != "run-123" {
		t.Fatalf("policy must select only the current run's graph pods: %#v", policy.Spec.PodSelector)
	}
	if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 1 || len(policy.Spec.Egress) != 2 {
		t.Fatalf("expected one same-run ingress rule and same-run plus DNS egress rules: %#v", policy.Spec)
	}
	peer := policy.Spec.Ingress[0].From[0]
	if peer.NamespaceSelector == nil || peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "shadow" || peer.PodSelector == nil || peer.PodSelector.MatchLabels[labelGraph] != "run-123" {
		t.Fatalf("ingress peer must be same namespace and same run: %#v", peer)
	}
	for _, rule := range policy.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock != nil {
				t.Fatalf("graph policy must not allow an IP block: %#v", peer)
			}
		}
	}
	if len(policy.Spec.Egress[1].Ports) != 2 || policy.Spec.Egress[1].Ports[0].Port.IntVal != 53 || policy.Spec.Egress[1].Ports[1].Port.IntVal != 53 {
		t.Fatalf("DNS rule must be limited to TCP/UDP 53: %#v", policy.Spec.Egress[1].Ports)
	}
}

func TestDiscoverGraphDNSTargetRequiresReadyServiceEndpoints(t *testing.T) {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-dns", Namespace: "kube-system"},
		Spec: corev1.ServiceSpec{ClusterIP: "10.96.0.10", Selector: map[string]string{"k8s-app": "kube-dns"}, Ports: []corev1.ServicePort{
			{Port: 53, Protocol: corev1.ProtocolUDP}, {Port: 53, Protocol: corev1.ProtocolTCP},
		}},
	}
	readyDNS := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "coredns-0", Namespace: "kube-system", Labels: map[string]string{"k8s-app": "kube-dns"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.10", Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(service, readyDNS)}
	namespace, selector, err := discoverGraphDNSTarget(context.Background(), client)
	if err != nil || namespace != "kube-system" || selector["k8s-app"] != "kube-dns" {
		t.Fatalf("expected exact ready cluster DNS selector, namespace=%q selector=%v err=%v", namespace, selector, err)
	}
	client.Clientset = corefake.NewSimpleClientset(service)
	if _, _, err := discoverGraphDNSTarget(context.Background(), client); err == nil {
		t.Fatal("DNS Service without a Ready Pod endpoint must not enable graph egress")
	}
	service.Spec.Ports = service.Spec.Ports[:1]
	client.Clientset = corefake.NewSimpleClientset(service, readyDNS)
	if _, _, err := discoverGraphDNSTarget(context.Background(), client); err == nil {
		t.Fatal("DNS Service without TCP and UDP port 53 must not enable graph egress")
	}
}

func TestGraphNetworkPolicyRejectsBroadSelectors(t *testing.T) {
	if _, err := graphNetworkPolicy("shadow", "run-123", "kube-system", nil); err == nil {
		t.Fatal("missing DNS pod selector must be refused")
	}
	if _, err := graphNetworkPolicy("shadow", "run-123", "", map[string]string{"k8s-app": "kube-dns"}); err == nil {
		t.Fatal("missing DNS namespace must be refused")
	}
	if _, err := graphNetworkPolicy("shadow", "", "kube-system", map[string]string{"k8s-app": "kube-dns"}); err == nil {
		t.Fatal("missing run identity must be refused")
	}
}

func TestGraphIsolationRejectsPolicyIdentityDrift(t *testing.T) {
	expected, err := graphNetworkPolicy("shadow", "run-123", "kube-system", map[string]string{"k8s-app": "kube-dns"})
	if err != nil {
		t.Fatal(err)
	}
	expected.UID = "policy-uid"
	clientset := corefake.NewSimpleClientset(expected)
	client := &kube.TypedClient{Clientset: clientset}
	if err := ensureGraphNetworkPolicy(context.Background(), client, expected); err != nil {
		t.Fatalf("exact owned graph policy rejected: %v", err)
	}
	changed := expected.DeepCopy()
	changed.UID = "replacement-uid"
	if err := ensureGraphNetworkPolicy(context.Background(), client, changed); err == nil {
		t.Fatal("replacement NetworkPolicy UID must be rejected")
	}
	changed = expected.DeepCopy()
	changed.Spec.Egress[0].To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] = "default"
	if err := ensureGraphNetworkPolicy(context.Background(), client, changed); err == nil {
		t.Fatal("broadened NetworkPolicy selector must be rejected")
	}
}

func TestGraphIsolationBlocksExternalEgress(t *testing.T) {
	policy, err := graphNetworkPolicy("shadow", "run-123", "kube-system", map[string]string{"k8s-app": "kube-dns"})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Spec.Egress[0].To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "shadow" {
		t.Fatalf("graph traffic must remain inside the shadow namespace: %#v", policy.Spec.Egress[0])
	}
	if policy.Spec.Egress[0].To[0].PodSelector.MatchLabels[labelGraph] != "run-123" {
		t.Fatalf("graph egress must target only same-run Pods: %#v", policy.Spec.Egress[0])
	}
	if policy.Spec.Egress[1].To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" || policy.Spec.Egress[1].To[0].PodSelector.MatchLabels["k8s-app"] != "kube-dns" {
		t.Fatalf("DNS exception must use exact namespace and pod selector: %#v", policy.Spec.Egress[1])
	}
	if len(policy.Spec.Egress[0].Ports) != 0 {
		t.Fatalf("same-run service traffic must be constrained by in-graph endpoints at runtime: %#v", policy.Spec.Egress[0].Ports)
	}
}
