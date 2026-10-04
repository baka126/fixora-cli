package analyzer

import (
	"context"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"strings"
	"testing"
	"time"
)

func TestDeterministicProbeDiagnosisNamesEndpointAndObservedFailure(t *testing.T) {
	pod := kube.Pod{Metadata: kube.ObjectMeta{Name: "api", Namespace: "demo", CreationTimestamp: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}, Spec: kube.PodSpec{Containers: []kube.Container{{Name: "web", ReadinessProbe: map[string]any{"httpGet": map[string]any{"path": "/not-ready", "port": 9090}}}}}, Status: kube.PodStatus{Phase: "Running", ContainerStatuses: []kube.ContainerStatus{{Name: "web", Ready: false, State: map[string]kube.StatusState{"running": {}}}}}}
	events := []kube.Event{{Reason: "Unhealthy", Message: "Readiness probe failed: HTTP probe failed with statuscode: 404"}}
	f, ok := New(fakeReader{}, Options{}).findingForPod(context.Background(), nil, pod, events)
	if !ok || f.AI != nil {
		t.Fatal("expected deterministic diagnosis")
	}
	for _, want := range []string{"web", "/not-ready", "9090", "404"} {
		if !strings.Contains(f.Summary, want) {
			t.Fatalf("diagnosis omits proven detail %q: %s", want, f.Summary)
		}
	}
}

func TestDeterministicProbeDiagnosisDoesNotAttributeAmbiguousFailure(t *testing.T) {
	pod := kube.Pod{Spec: kube.PodSpec{Containers: []kube.Container{{Name: "a", ReadinessProbe: map[string]any{"httpGet": map[string]any{"path": "/a", "port": 80}}}, {Name: "b", ReadinessProbe: map[string]any{"httpGet": map[string]any{"path": "/b", "port": 90}}}}}, Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{Name: "a", Ready: false}, {Name: "b", Ready: false}}}}
	if got := probeFailureDiagnosis(pod, "ProbeFailure", []kube.Event{{Message: "Readiness probe failed: HTTP probe failed with statuscode: 404"}}); got != "" {
		t.Fatalf("ambiguous event assigned to a container: %s", got)
	}
}

func TestProbeDiagnosisDoesNotUseCurrentReadinessToAttributeHistoricalEvents(t *testing.T) {
	pod := kube.Pod{Spec: kube.PodSpec{Containers: []kube.Container{{Name: "a", ReadinessProbe: map[string]any{"httpGet": map[string]any{"path": "/a", "port": 80}}}, {Name: "b", ReadinessProbe: map[string]any{"httpGet": map[string]any{"path": "/b", "port": 90}}}}}, Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{Name: "a", Ready: true}, {Name: "b", Ready: false}}}}
	got := probeFailureDiagnosis(pod, "ProbeFailure", []kube.Event{{Message: "Readiness probe failed: HTTP probe failed with statuscode: 404"}})
	if got != "" {
		t.Fatalf("historical anonymous event attributed using current readiness: %s", got)
	}
}
