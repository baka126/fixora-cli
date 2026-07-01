package analyzer

import (
	"context"
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

func stuckPodFixture(name string, extra map[string]any) map[string]any {
	meta := map[string]any{"namespace": "prod", "name": name, "deletionTimestamp": "2000-01-01T00:00:00Z"}
	pod := map[string]any{"metadata": meta, "spec": map[string]any{}}
	for k, v := range extra {
		pod[k] = v
	}
	return pod
}

func podTermScanContext(pods, nodes []map[string]any, events []kube.Event) *ScanContext {
	return NewScanContext(context.Background(), fakeReader{
		items:  map[string][]map[string]any{"pods": pods, "nodes": nodes},
		events: events,
	}, Options{})
}

func TestAnalyzePodsTerminatingFinalizerHigh(t *testing.T) {
	pod := stuckPodFixture("stuck", map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "stuck", "deletionTimestamp": "2000-01-01T00:00:00Z", "finalizers": []any{"example.com/f1"}},
	})
	ctx := podTermScanContext([]map[string]any{pod}, nil, nil)
	findings, err := New(fakeReader{}, Options{}).analyzePodsTerminating(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "PodStuckTerminating")
	for _, f := range findings {
		if f.Status == "PodStuckTerminating" {
			if f.Severity != "high" {
				t.Fatalf("finalizer-blocked should be high severity, got %q", f.Severity)
			}
			if f.Recommendations[0].SafeByDefault {
				t.Fatal("recommendation must be review-only (SafeByDefault=false)")
			}
		}
	}
}

func TestAnalyzePodsTerminatingNotTerminating(t *testing.T) {
	// No deletionTimestamp => not terminating => no finding.
	pod := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "live"}, "spec": map[string]any{}}
	ctx := podTermScanContext([]map[string]any{pod}, nil, nil)
	findings, err := New(fakeReader{}, Options{}).analyzePodsTerminating(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Status == "PodStuckTerminating" {
			t.Fatalf("live pod must not be flagged: %#v", f)
		}
	}
}

func TestAnalyzePodsTerminatingWithinGrace(t *testing.T) {
	// deletionTimestamp very recent => within grace+buffer => no finding.
	recent := time.Now().Add(-3 * time.Second).UTC().Format(time.RFC3339)
	pod := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "shutting", "deletionTimestamp": recent}, "spec": map[string]any{}}
	ctx := podTermScanContext([]map[string]any{pod}, nil, nil)
	findings, err := New(fakeReader{}, Options{}).analyzePodsTerminating(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Status == "PodStuckTerminating" {
			t.Fatalf("pod within grace must not be flagged: %#v", f)
		}
	}
}

func TestAnalyzePodsTerminatingNodeUnreachable(t *testing.T) {
	pod := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "stuck", "deletionTimestamp": "2000-01-01T00:00:00Z"}, "spec": map[string]any{"nodeName": "node-1"}}
	node := map[string]any{"metadata": map[string]any{"name": "node-1"}, "status": map[string]any{"conditions": []any{
		map[string]any{"type": "Ready", "status": "Unknown"},
	}}}
	ctx := podTermScanContext([]map[string]any{pod}, []map[string]any{node}, nil)
	findings, err := New(fakeReader{}, Options{}).analyzePodsTerminating(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if f.Status == "PodStuckTerminating" {
			for _, e := range f.Evidence {
				if strings.Contains(e.Value, "node-1") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("expected node-unreachable evidence, got %#v", findings)
	}
}

func TestPodTerminatingCollisionGuard(t *testing.T) {
	buildPlanKeys := []string{
		"ImagePull", "OOMKilled", "CrashLoopBackOff", "CreateContainerConfigError",
		"ExecFormatError", "PermissionDenied", "NoEndpoints", "ConnectionRefused",
		"HPA", "Evicted", "Webhook",
	}
	const status = "PodStuckTerminating"
	for _, k := range buildPlanKeys {
		if strings.Contains(status, k) || strings.Contains(k, status) {
			t.Fatalf("status %q collides with BuildPlan key %q", status, k)
		}
	}
}
