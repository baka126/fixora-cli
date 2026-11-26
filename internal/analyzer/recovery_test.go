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

// A crash-looping container spends much of the back-off period reported as
// terminated (Error, non-zero exit) rather than waiting: CrashLoopBackOff on
// current Kubernetes. A scan that lands in that window must still classify it.
func TestCrashLoopDetectedFromTerminatedState(t *testing.T) {
	pod := kube.Pod{
		Metadata: kube.ObjectMeta{Name: "api-2", Namespace: "prod"},
		Status: kube.PodStatus{
			Phase: "Running",
			ContainerStatuses: []kube.ContainerStatus{{
				Name:         "api",
				Ready:        false,
				RestartCount: 4,
				State:        map[string]kube.StatusState{"terminated": {Reason: "Error", ExitCode: 1}},
				LastState:    map[string]kube.StatusState{"terminated": {Reason: "Error", ExitCode: 1}},
			}},
		},
	}
	status, category, severity := podProblem(pod)
	if status != "CrashLoopBackOff" || category != "runtime" || severity != "critical" {
		t.Fatalf("podProblem = (%q, %q, %q), want (CrashLoopBackOff, runtime, critical)", status, category, severity)
	}
}

// A one-shot Pod that failed once (restartPolicy: Never) is Failed, not
// crash-looping — restartCount 0 must not trip the terminated-state branch.
func TestSingleFailedRunIsNotCrashLoop(t *testing.T) {
	pod := kube.Pod{
		Metadata: kube.ObjectMeta{Name: "job-x", Namespace: "prod"},
		Status: kube.PodStatus{
			Phase: "Failed",
			ContainerStatuses: []kube.ContainerStatus{{
				Name:         "runner",
				RestartCount: 0,
				State:        map[string]kube.StatusState{"terminated": {Reason: "Error", ExitCode: 1}},
			}},
		},
	}
	if status, _, _ := podProblem(pod); status == "CrashLoopBackOff" {
		t.Fatalf("a single failed run must not be classified CrashLoopBackOff (got %q)", status)
	}
}
