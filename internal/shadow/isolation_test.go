package shadow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

const testIsolationProbeImage = "registry.example/fixora/service-probe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestValidateIsolationProbeImageRequiresDigest(t *testing.T) {
	good := "registry.example/fixora/service-probe@sha256:" + strings.Repeat("a", 64)
	if err := validateProbeImage(good); err != nil {
		t.Fatalf("digest-pinned probe image rejected: %v", err)
	}
	for _, image := range []string{"", "busybox:latest", "registry.example/probe@sha256:bad"} {
		if err := validateProbeImage(image); err == nil {
			t.Errorf("expected image %q to be rejected", image)
		}
	}
}

func TestUnsupportedNetworkPolicyResourcesOnlyAllowStandardNamespacedPolicy(t *testing.T) {
	resources := []*metav1.APIResourceList{
		{GroupVersion: "networking.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "networkpolicies", Kind: "NetworkPolicy", Namespaced: true}}},
		{GroupVersion: "cilium.io/v2", APIResources: []metav1.APIResource{{Name: "ciliumclusterwidenetworkpolicies", Kind: "CiliumClusterwideNetworkPolicy", Namespaced: false}}},
	}
	got := unsupportedNetworkPolicyResources(resources)
	if len(got) != 1 || !strings.Contains(got[0], "CiliumClusterwideNetworkPolicy") {
		t.Fatalf("unsupported network-policy resources = %#v", got)
	}
}

func TestSandboxDependencyLimitationsDetectOnlyRequiredNamespacedInputs(t *testing.T) {
	optional := true
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry"}},
		Volumes:          []corev1.Volume{{Name: "settings", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}}}},
		Containers:       []corev1.Container{{Name: "app", Env: []corev1.EnvVar{{Name: "OPTIONAL", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "optional-config"}, Optional: &optional}}}}}},
	}}
	got := sandboxDependencyLimitations(pod)
	if len(got) != 2 || !strings.Contains(strings.Join(got, " "), "image pull secret") || !strings.Contains(strings.Join(got, " "), "ConfigMap") {
		t.Fatalf("sandbox dependency limitations = %#v", got)
	}
}

func TestInspectSourceServiceAccountFindsPullSecretReferences(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "prod"}, ImagePullSecrets: []corev1.LocalObjectReference{{Name: "private-registry"}}})
	found, err := inspectSourceServiceAccount(context.Background(), &kube.TypedClient{Clientset: clientset}, "prod", &corev1.Pod{})
	if err != nil || !found {
		t.Fatalf("expected source service-account pull secret limitation, found=%t err=%v", found, err)
	}
}

func TestEnsureSandboxNetworkPolicyRequiresExactOwnedDenyPolicy(t *testing.T) {
	policy := sandboxNetworkPolicy("sandbox", "deny-all", "session-1")
	policy.UID = "policy-uid"
	clientset := fake.NewSimpleClientset(policy)
	client := &kube.TypedClient{Clientset: clientset}
	if err := ensureSandboxNetworkPolicy(context.Background(), client, policy); err != nil {
		t.Fatalf("exact owned deny policy rejected: %v", err)
	}
	policy.UID = "stale-uid"
	if err := ensureSandboxNetworkPolicy(context.Background(), client, policy); err == nil {
		t.Fatal("replacement policy UID accepted")
	}
}

func TestActiveUnsupportedPoliciesAreListedAndIncompleteInventoryFails(t *testing.T) {
	gvk := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"}
	resources := []*metav1.APIResourceList{{GroupVersion: "cilium.io/v2", APIResources: []metav1.APIResource{{Name: gvk.Resource, Kind: "CiliumClusterwideNetworkPolicy", Namespaced: false}}}}
	listKinds := map[schema.GroupVersionResource]string{gvk: "CiliumClusterwideNetworkPolicyList"}
	client := &kube.TypedClient{Dynamic: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)}
	active, err := activeUnsupportedNetworkPolicies(context.Background(), client, resources, "sandbox", "witness")
	if err != nil || len(active) != 0 {
		t.Fatalf("empty extension API should not block: active=%v err=%v", active, err)
	}
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cilium.io/v2", "kind": "CiliumClusterwideNetworkPolicy", "metadata": map[string]any{"name": "allow-egress"}}}
	client.Dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, object)
	active, err = activeUnsupportedNetworkPolicies(context.Background(), client, resources, "sandbox", "witness")
	if err != nil || len(active) != 1 {
		t.Fatalf("active extension policy must be detected: active=%v err=%v", active, err)
	}
	client.Dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	client.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list", gvk.Resource, func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, fmt.Errorf("forbidden") })
	if _, err = activeUnsupportedNetworkPolicies(context.Background(), client, resources, "sandbox", "witness"); err == nil {
		t.Fatal("forbidden policy inventory must fail closed")
	}
}

func TestProveNetworkPolicyRunsBidirectionalControlsBeforePassing(t *testing.T) {
	for _, enforce := range []bool{true, false} {
		t.Run(fmt.Sprintf("enforce=%t", enforce), func(t *testing.T) {
			clientset, discoveryClient := isolationFake(enforce)
			client, err := (&kube.TypedClient{Clientset: clientset}).CreateNamespace(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fixora-shadow-test", UID: "sandbox-uid", Labels: map[string]string{labelSandbox: "true", labelSession: "session-test"}}})
			if err != nil {
				t.Fatal(err)
			}
			k := &kube.TypedClient{Clientset: clientset, Discovery: discoveryClient, Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
			session := "session-test-123"
			policy := sandboxNetworkPolicy(client.Name, "deny-all", session)
			report, err := proveNetworkPolicy(context.Background(), k, client, policy, session, testIsolationProbeImage)
			if enforce {
				if err != nil || report.Status != string(VerificationVerified) {
					t.Fatalf("expected enforced policy to pass: report=%#v err=%v", report, err)
				}
				if len(report.Checks) != 4 {
					t.Fatalf("expected positive and negative controls, got %#v", report.Checks)
				}
			} else if err == nil || report.Status != string(VerificationBlocked) {
				t.Fatalf("unenforced policy must block candidate: report=%#v err=%v", report, err)
			}
		})
	}
}

func isolationFake(enforce bool) (*fake.Clientset, discovery.DiscoveryInterface) {
	clientset := fake.NewSimpleClientset()
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
			pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.1.0.5", Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "probe", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}
		} else {
			// The fake probe does not open a real TCP socket, so model the effect
			// of the sandbox policy on both directions: egress probes originate in
			// the sandbox and ingress probes originate in the witness namespace.
			policies, _ := clientset.Tracker().List(schema.GroupVersionResource{Group: networkingv1.GroupName, Version: "v1", Resource: "networkpolicies"}, schema.GroupVersionKind{Group: networkingv1.GroupName, Version: "v1", Kind: "NetworkPolicy"}, "")
			blocked := len(policies.(*networkingv1.NetworkPolicyList).Items) != 0 && enforce
			exitCode, phase := int32(0), corev1.PodSucceeded
			if blocked {
				exitCode, phase = 1, corev1.PodFailed
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
