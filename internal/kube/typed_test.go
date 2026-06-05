package kube

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestResourceCandidateHandlesShortcutsAndGroups(t *testing.T) {
	tests := map[string]string{
		"hpa":                                  "autoscaling/v2, Resource=horizontalpodautoscalers",
		"pdb":                                  "policy/v1, Resource=poddisruptionbudgets",
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
