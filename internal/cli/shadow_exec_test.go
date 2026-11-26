package cli

import (
	"bufio"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	corefake "k8s.io/client-go/kubernetes/fake"
)

const shadowShellSession = "11111111-2222-4333-8444-555555555555"

func shadowShellFixture() (*kube.TypedClient, shadow.Result) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "fixora-shadow-1111111122224333", UID: types.UID("namespace-uid"), ResourceVersion: "1",
		Labels:      map[string]string{"fixora.io/sandbox": "true", "fixora.io/session": shadowShellSession},
		Annotations: map[string]string{"fixora.io/source-namespace": "prod"},
	}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "api-candidate", Namespace: ns.Name, UID: types.UID("pod-uid"),
		Labels: map[string]string{"fixora.io/sandbox": "true", "fixora.io/session": shadowShellSession, "fixora.io/original-pod": "api-123"},
	}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "example/api:test"}}}}
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(ns, pod)}
	result := shadow.Result{Verified: true, Status: shadow.VerificationVerified, IsolationNamespace: ns.Name, IsolationUID: string(ns.UID), CloneName: pod.Name, CloneUID: string(pod.UID)}
	return client, result
}

func TestShadowShellDeclineCleansRunNamespace(t *testing.T) {
	client, result := shadowShellFixture()
	var out, errOut strings.Builder
	runnerCalls := 0
	err := inspectShadowAfterVerdict(context.Background(), &out, &errOut, options{output: "text", interactive: true, promptInput: bufio.NewReader(strings.NewReader("n\n"))}, kube.Kubectl{}, client, result, true,
		func(context.Context, kube.Kubectl, string) (bool, error) {
			t.Fatal("declined shell must not check pods/exec")
			return false, nil
		},
		func(context.Context, kube.Kubectl, string, string, string) error { runnerCalls++; return nil })
	if err != nil || runnerCalls != 0 || !strings.Contains(out.String(), "Shell skipped") {
		t.Fatalf("declining shell should skip exec and clean up, err=%v calls=%d out=%q", err, runnerCalls, out.String())
	}
	if _, err := client.GetNamespace(context.Background(), result.IsolationNamespace); err == nil {
		t.Fatal("declining shell should clean the run-owned namespace")
	}
}

func TestShadowShellRefusesIdentityDriftAndCleansUp(t *testing.T) {
	client, result := shadowShellFixture()
	result.CloneUID = "replacement-pod-uid"
	var out, errOut strings.Builder
	permissionCalls, runnerCalls := 0, 0
	err := inspectShadowAfterVerdict(context.Background(), &out, &errOut, options{output: "text", interactive: true, promptInput: bufio.NewReader(strings.NewReader("yes\n"))}, kube.Kubectl{}, client, result, true,
		func(context.Context, kube.Kubectl, string) (bool, error) { permissionCalls++; return true, nil },
		func(context.Context, kube.Kubectl, string, string, string) error { runnerCalls++; return nil })
	if err == nil || !strings.Contains(err.Error(), "identity or ownership changed") || permissionCalls != 0 || runnerCalls != 0 {
		t.Fatalf("identity drift must refuse before authorization/exec, err=%v permission=%d runner=%d", err, permissionCalls, runnerCalls)
	}
	if _, getErr := client.GetNamespace(context.Background(), result.IsolationNamespace); getErr == nil {
		t.Fatal("identity drift refusal must clean the generated namespace")
	}
}

func TestShadowShellRequiresExecPermissionAndUsesExactTarget(t *testing.T) {
	client, result := shadowShellFixture()
	var out, errOut strings.Builder
	called := false
	err := inspectShadowAfterVerdict(context.Background(), &out, &errOut, options{output: "text", interactive: true, promptInput: bufio.NewReader(strings.NewReader("y\n"))}, kube.Kubectl{}, client, result, true,
		func(_ context.Context, _ kube.Kubectl, namespace string) (bool, error) {
			return namespace == result.IsolationNamespace, nil
		},
		func(_ context.Context, _ kube.Kubectl, namespace, pod, container string) error {
			called = true
			if namespace != result.IsolationNamespace || pod != result.CloneName || container != "api" {
				t.Fatalf("exec target must match verified identity: %s/%s container=%s", namespace, pod, container)
			}
			return nil
		})
	if err != nil || !called || !strings.Contains(out.String(), "Cleaned up namespace/") {
		t.Fatalf("authorized shell should use exact target and then clean up, called=%t err=%v out=%q", called, err, out.String())
	}
}

func TestShadowShellRefusesMissingPodsExecPermissionAndCleans(t *testing.T) {
	client, result := shadowShellFixture()
	var out, errOut strings.Builder
	runnerCalls := 0
	err := inspectShadowAfterVerdict(context.Background(), &out, &errOut, options{output: "text", interactive: true, promptInput: bufio.NewReader(strings.NewReader("y\n"))}, kube.Kubectl{}, client, result, true,
		func(context.Context, kube.Kubectl, string) (bool, error) { return false, nil },
		func(context.Context, kube.Kubectl, string, string, string) error { runnerCalls++; return nil })
	if err == nil || !strings.Contains(err.Error(), "pods/exec permission is not allowed") || runnerCalls != 0 {
		t.Fatalf("missing RBAC must refuse shell before exec, err=%v runnerCalls=%d", err, runnerCalls)
	}
	if _, getErr := client.GetNamespace(context.Background(), result.IsolationNamespace); getErr == nil {
		t.Fatal("permission refusal should remove the temporary namespace")
	}
}

func TestShadowShellRefusesNamespaceIdentityDrift(t *testing.T) {
	client, result := shadowShellFixture()
	result.IsolationUID = "replacement-namespace-uid"
	_, err := validateShadowShellTarget(context.Background(), client, result, "")
	if err == nil || !strings.Contains(err.Error(), "namespace identity") {
		t.Fatalf("namespace replacement must be refused: %v", err)
	}
}

func TestShadowShellRetainsOnlyWhenRequestedAndCleansAfterRunnerError(t *testing.T) {
	client, result := shadowShellFixture()
	var out, errOut strings.Builder
	opts := options{output: "text", interactive: true, keepShadow: true, promptInput: bufio.NewReader(strings.NewReader("yes\n"))}
	err := inspectShadowAfterVerdict(context.Background(), &out, &errOut, opts, kube.Kubectl{}, client, result, true,
		func(context.Context, kube.Kubectl, string) (bool, error) { return true, nil },
		func(context.Context, kube.Kubectl, string, string, string) error { return errors.New("exec denied") })
	if err == nil || !strings.Contains(err.Error(), "kubectl exec failed") {
		t.Fatalf("exec error should be returned: %v", err)
	}
	if _, getErr := client.GetNamespace(context.Background(), result.IsolationNamespace); getErr != nil {
		t.Fatalf("--keep-shadow should retain the exact namespace after shell failure: %v", getErr)
	}
}

func TestShadowShellRefusesNonInteractiveOutput(t *testing.T) {
	client, result := shadowShellFixture()
	var out, errOut strings.Builder
	err := inspectShadowAfterVerdict(context.Background(), &out, &errOut, options{output: "json", interactive: false}, kube.Kubectl{}, client, result, true, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "requires interactive text output") {
		t.Fatalf("non-interactive shell must be rejected: %v", err)
	}
}
