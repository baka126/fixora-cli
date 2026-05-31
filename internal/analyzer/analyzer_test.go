package analyzer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
