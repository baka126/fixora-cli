package kube

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestResourceCandidateHandlesShortcutsAndGroups(t *testing.T) {
	tests := map[string]string{
		"hpa":                                  "autoscaling/v2, Resource=horizontalpodautoscalers",
		"pdb":                                  "policy/v1, Resource=poddisruptionbudgets",
		"pvc":                                  "/, Resource=persistentvolumeclaims",
		"httproutes.gateway.networking.k8s.io": "gateway.networking.k8s.io/, Resource=httproutes",
		"pods":                                 "/, Resource=pods",
	}
	for input, want := range tests {
		if got := resourceCandidate(input).String(); got != want {
			t.Fatalf("resourceCandidate(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNewRequiredTypedClientFailsLoudlyWithoutConfig(t *testing.T) {
	old := os.Getenv("KUBECONFIG")
	t.Cleanup(func() { _ = os.Setenv("KUBECONFIG", old) })
	_ = os.Setenv("KUBECONFIG", "/tmp/fixora-missing-kubeconfig")
	client, err := NewRequiredTypedClient("missing-context", "shadow verification")
	if err == nil || client != nil {
		t.Fatalf("expected required typed client failure, client=%v err=%v", client, err)
	}
}

func TestNewTypedClientAllowsOpportunisticFallback(t *testing.T) {
	old := os.Getenv("KUBECONFIG")
	t.Cleanup(func() { _ = os.Setenv("KUBECONFIG", old) })
	_ = os.Setenv("KUBECONFIG", "/tmp/fixora-missing-kubeconfig")
	client, err := NewTypedClient("missing-context")
	if err != nil {
		t.Fatalf("unexpected fallback error: %v", err)
	}
	if client == nil || client.Clientset != nil {
		t.Fatalf("expected fallback-only typed client, got %#v", client)
	}
}

func TestSleepContextReturnsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := sleepContext(ctx, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("context-aware sleep did not return promptly")
	}
}

func TestNamespaceOwnershipPrimitives(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	client := &TypedClient{Clientset: clientset}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fixora-shadow-run", Labels: map[string]string{"fixora.io/run": "run-1"}}}
	created, err := client.CreateNamespace(context.Background(), ns)
	if err != nil || created.Name != ns.Name {
		t.Fatalf("CreateNamespace() = %#v, %v", created, err)
	}
	got, err := client.GetNamespace(context.Background(), ns.Name)
	if err != nil || got.UID != created.UID {
		t.Fatalf("GetNamespace() = %#v, %v", got, err)
	}
	var deleteOptions metav1.DeleteOptions
	clientset.PrependReactor("delete", "namespaces", func(action ktesting.Action) (bool, runtime.Object, error) {
		deleteOptions = action.(ktesting.DeleteAction).GetDeleteOptions()
		return false, nil, nil
	})
	if err := client.DeleteNamespaceUID(context.Background(), ns.Name, types.UID("expected-uid")); err != nil {
		t.Fatal(err)
	}
	if deleteOptions.Preconditions == nil || deleteOptions.Preconditions.UID == nil || *deleteOptions.Preconditions.UID != types.UID("expected-uid") {
		t.Fatalf("delete did not carry expected UID precondition: %#v", deleteOptions)
	}
}
