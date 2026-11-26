package shadow

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	corefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestGraphIsolationIsInconclusiveWithoutVerifiedDNSOrCNI(t *testing.T) {
	for _, enforce := range []bool{true, false} {
		t.Run("cni-enforces="+strconv.FormatBool(enforce), func(t *testing.T) {
			clientset, discoveryClient := graphIsolationRuntimeFake(enforce)
			sandbox, err := (&kube.TypedClient{Clientset: clientset}).CreateNamespace(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fixora-shadow-test", UID: "sandbox-uid", Labels: map[string]string{labelSandbox: "true", labelSession: "session-123"}}})
			if err != nil {
				t.Fatal(err)
			}
			session := "session-123"
			deny := sandboxNetworkPolicy(sandbox.Name, "deny-all", session)
			deny.UID = "uid-policy-deny-all"
			if _, err := clientset.NetworkingV1().NetworkPolicies(sandbox.Name).Create(context.Background(), deny, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			client := &kube.TypedClient{Clientset: clientset, Discovery: discoveryClient, Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
			if err := ensureSandboxNetworkPolicy(context.Background(), client, deny); err != nil {
				t.Fatalf("test setup default-deny check failed: %v", err)
			}
			report, _, err := proveGraphNetworkPolicy(context.Background(), client, sandbox.Name, session, testIsolationProbeImage, deny, "kube-system", map[string]string{"k8s-app": "kube-dns"})
			if enforce {
				if err != nil || report.Status != string(VerificationVerified) {
					t.Fatalf("enforced graph policy should pass: report=%#v err=%v", report, err)
				}
				if len(report.Checks) != 4 {
					t.Fatalf("expected internal, external egress, external ingress, DNS checks; got %#v", report.Checks)
				}
			} else if err == nil || report.Status != string(VerificationInconclusive) {
				t.Fatalf("unenforced graph policy must be inconclusive: report=%#v err=%v", report, err)
			}
		})
	}

	clientset, discoveryClient := graphIsolationRuntimeFake(true)
	sandbox, err := (&kube.TypedClient{Clientset: clientset}).CreateNamespace(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fixora-shadow-test", UID: "sandbox-uid", Labels: map[string]string{labelSandbox: "true", labelSession: "session-123"}}})
	if err != nil {
		t.Fatal(err)
	}
	deny := sandboxNetworkPolicy(sandbox.Name, "deny-all", "session-123")
	deny.UID = "uid-policy-deny-all"
	if _, err := clientset.NetworkingV1().NetworkPolicies(sandbox.Name).Create(context.Background(), deny, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	client := &kube.TypedClient{Clientset: clientset, Discovery: discoveryClient, Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
	report, _, err := proveGraphNetworkPolicy(context.Background(), client, sandbox.Name, "session-123", testIsolationProbeImage, deny, "", nil)
	if err == nil || report.Status != string(VerificationInconclusive) {
		t.Fatalf("missing verified DNS target must block graph isolation: report=%#v err=%v", report, err)
	}
}

func graphIsolationRuntimeFake(enforce bool) (*corefake.Clientset, discovery.DiscoveryInterface) {
	clientset := corefake.NewSimpleClientset()
	clientset.PrependReactor("create", "namespaces", func(action ktesting.Action) (bool, runtime.Object, error) {
		ns := action.(ktesting.CreateAction).GetObject().(*corev1.Namespace).DeepCopy()
		if ns.UID == "" {
			ns.UID = types.UID("uid-" + ns.Name)
		}
		if err := clientset.Tracker().Create(corev1.SchemeGroupVersion.WithResource("namespaces"), ns, ""); err != nil {
			return true, nil, err
		}
		return true, ns, nil
	})
	clientset.PrependReactor("create", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
		pod.UID = types.UID("uid-" + pod.Namespace + "-" + pod.Name)
		if pod.Labels["fixora.io/control"] == "listener" {
			ip := "10.2.0.5"
			if pod.Labels[labelGraph] != "" {
				ip = "10.1.0.5"
			}
			pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "probe", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}
		} else {
			connected := enforce && pod.Labels[labelGraph] != "" && strings.Contains(strings.Join(pod.Spec.Containers[0].Args, " "), "10.1.0.5")
			exitCode, phase := int32(1), corev1.PodFailed
			if connected {
				exitCode, phase = 0, corev1.PodSucceeded
			}
			pod.Status = corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{{Name: "probe", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exitCode}}}}}
		}
		if err := clientset.Tracker().Create(corev1.SchemeGroupVersion.WithResource("pods"), pod, pod.Namespace); err != nil {
			return true, nil, err
		}
		return true, pod, nil
	})
	clientset.PrependReactor("create", "networkpolicies", func(action ktesting.Action) (bool, runtime.Object, error) {
		policy := action.(ktesting.CreateAction).GetObject().(*networkingv1.NetworkPolicy).DeepCopy()
		policy.UID = types.UID("uid-policy-" + policy.Name)
		if err := clientset.Tracker().Create(networkingv1.SchemeGroupVersion.WithResource("networkpolicies"), policy, policy.Namespace); err != nil {
			return true, nil, err
		}
		return true, policy, nil
	})
	discoveryClient := &discoveryfake.FakeDiscovery{Fake: &ktesting.Fake{}}
	discoveryClient.Resources = []*metav1.APIResourceList{{GroupVersion: "networking.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "networkpolicies", Kind: "NetworkPolicy", Namespaced: true}}}}
	return clientset, discoveryClient
}
