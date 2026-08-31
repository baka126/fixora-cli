package kube

import (
	"context"
	"os"
	"testing"
)

// TestTypedClientLogsFallsBackWhenClientsetNil pins the fix for a latent panic:
// every other *TypedClient method guards `Clientset == nil` and delegates to
// the Kubectl fallback, but Logs went straight to
// `c.Clientset.CoreV1()` — a nil dereference whenever NewTypedClient took its
// opportunistic fallback path (bad or missing kubeconfig). The analyzer calls
// Logs on exactly that client when `--include-logs` is set with the default
// `--typed-client`.
func TestTypedClientLogsFallsBackWhenClientsetNil(t *testing.T) {
	old := os.Getenv("KUBECONFIG")
	t.Cleanup(func() { _ = os.Setenv("KUBECONFIG", old) })
	_ = os.Setenv("KUBECONFIG", "/tmp/fixora-missing-kubeconfig")

	client, err := NewTypedClient("missing-context")
	if err != nil {
		t.Fatalf("unexpected fallback error: %v", err)
	}
	if client.Clientset != nil {
		t.Skip("environment produced a real typed client; nothing to test")
	}

	// Before the fix this panics with a nil pointer dereference. After it,
	// Logs routes through the Kubectl fallback, which returns an ordinary error
	// because there is no cluster — never a panic.
	_, err = client.Logs(context.Background(), "default", "some-pod", false)
	if err == nil {
		t.Fatal("expected an error from the kubectl fallback with no cluster, got nil")
	}
}
