package analyzer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func TestLintFindsProductionRisks(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "deployment.yaml")
	err := os.WriteFile(manifest, []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
      - name: api
        image: ghcr.io/acme/api:latest
        securityContext:
          privileged: true
      volumes:
      - name: host
        hostPath:
          path: /var/run
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	results, err := Lint([]string{manifest})
	if err != nil {
		t.Fatal(err)
	}

	assertLintContains(t, results, "latest tag")
	assertLintContains(t, results, "privileged containers")
	assertLintContains(t, results, "hostPath volumes")
	assertLintContains(t, results, "resource requests")
	assertLintContains(t, results, "readiness probes")
}

func TestLintWalksDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("skip me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pod.yaml"), []byte(`apiVersion: v1
kind: Pod
metadata:
  name: pinned
spec:
  containers:
  - name: app
    image: ghcr.io/acme/app:v1.2.3
    resources:
      requests:
        cpu: "100m"
        memory: "128Mi"
      limits:
        memory: "256Mi"
    readinessProbe:
      httpGet:
        path: /healthz
        port: 8080
    livenessProbe:
      httpGet:
        path: /healthz
        port: 8080
`), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := Lint([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one lint result, got %d: %#v", len(results), results)
	}
	if results[0].Severity != "ok" {
		t.Fatalf("expected ok lint result, got %#v", results[0])
	}
}

func assertLintContains(t *testing.T, results []LintResult, want string) {
	t.Helper()
	for _, result := range results {
		if strings.Contains(result.Message, want) {
			return
		}
	}
	t.Fatalf("expected lint result containing %q, got %#v", want, results)
}

func TestPrecisionHelpersFindIngressBackends(t *testing.T) {
	services := ingressBackendServices(map[string]any{
		"defaultBackend": map[string]any{
			"service": map[string]any{"name": "api"},
		},
		"rules": []any{map[string]any{
			"http": map[string]any{
				"paths": []any{map[string]any{
					"backend": map[string]any{"serviceName": "legacy"},
				}},
			},
		}},
	})
	got := strings.Join(services, ",")
	if got != "api,legacy" {
		t.Fatalf("expected api,legacy, got %q", got)
	}
}

func TestScanReportEnvelopeStatus(t *testing.T) {
	report := ScanReport{Findings: []Finding{{ID: "ns/pod/fail"}}}
	envelope := report.Envelope()
	if envelope.APIVersion != "fixora.dev/v1alpha1" || envelope.Kind != "AnalysisReport" {
		t.Fatalf("unexpected envelope identity: %#v", envelope)
	}
	if envelope.Status != "ProblemDetected" || envelope.Problems != 1 {
		t.Fatalf("unexpected envelope status: %#v", envelope)
	}
}

func TestScanReportKeepsPodFindingsWhenEventsFail(t *testing.T) {
	reader := fakeReader{
		pods: kube.PodList{Items: []kube.Pod{{
			Metadata: kube.ObjectMeta{Name: "api", Namespace: "prod"},
			Status: kube.PodStatus{
				ContainerStatuses: []kube.ContainerStatus{{
					Name: "api",
					State: map[string]kube.StatusState{
						"waiting": {Reason: "CrashLoopBackOff"},
					},
				}},
			},
		}}},
		eventsErr: fmt.Errorf("events forbidden"),
	}
	report := New(reader, Options{Namespace: "prod"}).ScanReport(context.Background())
	if len(report.Findings) != 1 {
		t.Fatalf("expected pod finding despite events error, got %#v", report)
	}
	foundEventsSkip := false
	for _, skipped := range report.Skipped {
		if skipped.Name == "events" {
			foundEventsSkip = true
		}
	}
	if !foundEventsSkip {
		t.Fatalf("expected skipped events check, got %#v", report.Skipped)
	}
}

func TestTopOwnerKeepsJobIdentity(t *testing.T) {
	pod := kube.Pod{Metadata: kube.ObjectMeta{
		Name:      "worker",
		Namespace: "prod",
		OwnerRefs: []kube.OwnerReference{{
			Kind: "Job",
			Name: "data-migration-2026",
		}},
	}}
	if got := topOwnerKind(pod); got != "Job" {
		t.Fatalf("expected Job owner kind, got %q", got)
	}
	if got := topOwnerName(pod); got != "data-migration-2026" {
		t.Fatalf("expected full Job owner name, got %q", got)
	}
}

type fakeReader struct {
	pods      kube.PodList
	eventsErr error
}

func (f fakeReader) GetPods(context.Context, string, bool) (kube.PodList, error) {
	return f.pods, nil
}

func (f fakeReader) GetPod(context.Context, string, string) (kube.Pod, error) {
	return kube.Pod{}, fmt.Errorf("not implemented")
}

func (f fakeReader) GetResource(context.Context, string, string) (map[string]any, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f fakeReader) GetResourceItems(context.Context, string, bool, string) ([]map[string]any, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f fakeReader) GetEvents(context.Context, string) ([]kube.Event, error) {
	return nil, f.eventsErr
}

func (f fakeReader) GetNodes(context.Context) ([]kube.Node, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f fakeReader) Logs(context.Context, string, string, bool) (string, error) {
	return "", nil
}

func (f fakeReader) Run(context.Context, ...string) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}
