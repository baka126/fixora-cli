package analyzer

import (
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func TestDeletionAge(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	age, ok := deletionAge("2026-07-01T11:59:00Z", now)
	if !ok || age != time.Minute {
		t.Fatalf("age=%v ok=%v", age, ok)
	}
	if _, ok := deletionAge("", now); ok {
		t.Fatal("empty ts must be ok=false")
	}
	if _, ok := deletionAge("not-a-time", now); ok {
		t.Fatal("unparseable ts must be ok=false")
	}
}

func TestIsStuckTerminating(t *testing.T) {
	// grace defaults to 30s; buffer 30s => stuck when age > 60s.
	if isStuckTerminating(45*time.Second, 0) {
		t.Fatal("45s with default grace is within grace+buffer, not stuck")
	}
	if !isStuckTerminating(90*time.Second, 0) {
		t.Fatal("90s with default grace should be stuck")
	}
	// explicit grace 120s => stuck when age > 150s.
	if isStuckTerminating(140*time.Second, 120) {
		t.Fatal("140s with grace 120 is within grace+buffer")
	}
	if !isStuckTerminating(200*time.Second, 120) {
		t.Fatal("200s with grace 120 should be stuck")
	}
}

func TestTerminatingCausesFinalizer(t *testing.T) {
	pod := map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "p", "finalizers": []any{"example.com/f1"}},
		"spec":     map[string]any{},
	}
	causes, blocked := terminatingCauses(pod, nil, nil)
	if !blocked {
		t.Fatal("finalizer present must set finalizerBlocked")
	}
	if !containsSubstr(causes, "example.com/f1") {
		t.Fatalf("expected finalizer name in causes, got %v", causes)
	}
}

func TestTerminatingCausesPreStop(t *testing.T) {
	pod := map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "p"},
		"spec": map[string]any{"containers": []any{
			map[string]any{"name": "app", "lifecycle": map[string]any{"preStop": map[string]any{"exec": map[string]any{}}}},
		}},
	}
	causes, _ := terminatingCauses(pod, nil, nil)
	if !containsSubstr(causes, "preStop") {
		t.Fatalf("expected preStop cause, got %v", causes)
	}
}

func TestTerminatingCausesVolumeDetach(t *testing.T) {
	pod := map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "p"},
		"spec":     map[string]any{},
	}
	events := []kube.Event{{
		Reason:         "FailedUnMount",
		Message:        "Unable to detach volume",
		InvolvedObject: kube.ObjectReference{Namespace: "prod", Name: "p"},
	}}
	causes, _ := terminatingCauses(pod, events, nil)
	if !containsSubstr(causes, "detach") && !containsSubstr(causes, "FailedUnMount") {
		t.Fatalf("expected volume-detach cause, got %v", causes)
	}
}

func TestTerminatingCausesNodeUnreachable(t *testing.T) {
	pod := map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "p"},
		"spec":     map[string]any{"nodeName": "node-1"},
	}
	causes, _ := terminatingCauses(pod, nil, map[string]bool{"node-1": false})
	if !containsSubstr(causes, "node-1") {
		t.Fatalf("expected node-unreachable cause, got %v", causes)
	}
	// Ready node => no node cause.
	causes, _ = terminatingCauses(pod, nil, map[string]bool{"node-1": true})
	if containsSubstr(causes, "node-1") {
		t.Fatalf("ready node must not be flagged, got %v", causes)
	}
}

// containsSubstr reports whether any element of xs contains sub.
func containsSubstr(xs []string, sub string) bool {
	for _, x := range xs {
		if len(sub) > 0 && len(x) >= len(sub) && strings.Contains(x, sub) {
			return true
		}
	}
	return false
}
