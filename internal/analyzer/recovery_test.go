package analyzer

import (
	"context"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func recoveredOOMPod() kube.Pod {
	return kube.Pod{
		Metadata: kube.ObjectMeta{Name: "api-0", Namespace: "prod"},
		Status: kube.PodStatus{
			Phase: "Running",
			ContainerStatuses: []kube.ContainerStatus{{
				Name:         "api",
				Ready:        true,
				RestartCount: 3,
				State:        map[string]kube.StatusState{"running": {}},
				LastState:    map[string]kube.StatusState{"terminated": {Reason: "OOMKilled"}},
			}},
		},
	}
}

func TestOOMKilledRecoveredPodIsMarkedAndDowngraded(t *testing.T) {
	reader := fakeReader{pods: kube.PodList{Items: []kube.Pod{recoveredOOMPod()}}}
	report := New(reader, Options{Namespace: "prod"}).ScanReport(context.Background())
	var found *Finding
	for i := range report.Findings {
		if report.Findings[i].Status == "OOMKilled" {
			found = &report.Findings[i]
		}
	}
	if found == nil {
		t.Fatalf("expected an OOMKilled finding, got %+v", report.Findings)
	}
	if !found.Recovered {
		t.Fatal("expected Recovered=true for a Running, all-Ready pod")
	}
	if found.Severity != "low" {
		t.Fatalf("expected severity lowered to low, got %q", found.Severity)
	}
	var sawEvidence bool
	for _, e := range found.Evidence {
		if e.Label == "Observed recovered" {
			sawEvidence = true
			if !strings.Contains(e.Value, "restarts=3") {
				t.Fatalf("evidence should report restart count, got %q", e.Value)
			}
		}
	}
	if !sawEvidence {
		t.Fatal("expected an Observed recovered evidence row")
	}
}

func TestActiveCrashLoopIsNotRecovered(t *testing.T) {
	pod := kube.Pod{
		Metadata: kube.ObjectMeta{Name: "api-1", Namespace: "prod"},
		Status: kube.PodStatus{
			Phase: "Running",
			ContainerStatuses: []kube.ContainerStatus{{
				Name:  "api",
				Ready: false,
				State: map[string]kube.StatusState{"waiting": {Reason: "CrashLoopBackOff"}},
			}},
		},
	}
	reader := fakeReader{pods: kube.PodList{Items: []kube.Pod{pod}}}
	report := New(reader, Options{Namespace: "prod"}).ScanReport(context.Background())
	for _, f := range report.Findings {
		if f.Status == "CrashLoopBackOff" {
			if f.Recovered {
				t.Fatal("a not-Ready crashlooping pod must not be marked recovered")
			}
			if f.Severity == "low" {
				t.Fatal("active crashloop severity must not be downgraded")
			}
			return
		}
	}
	t.Fatal("expected a CrashLoopBackOff finding")
}

func TestTotalRestarts(t *testing.T) {
	pod := kube.Pod{Status: kube.PodStatus{
		InitStatuses:      []kube.ContainerStatus{{RestartCount: 2}},
		ContainerStatuses: []kube.ContainerStatus{{RestartCount: 3}, {RestartCount: 1}},
	}}
	if got := totalRestarts(pod); got != 6 {
		t.Fatalf("totalRestarts = %d, want 6", got)
	}
}
