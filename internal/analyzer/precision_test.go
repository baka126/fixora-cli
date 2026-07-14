package analyzer

import (
	"reflect"
	"testing"
)

// Two analyzers registered in a fixed order (pvc before configmap) each produce
// a finding; the concurrent runner must return them in that registration order,
// stably across repeated runs.
//
// Real statuses confirmed by reading pvc.go and configmap.go:
//   - A Pending PVC with no ProvisioningFailed event → status "Pending"
//   - A ConfigMap with a key ending in .json containing invalid JSON → status "ConfigMapInvalidFormat"
//
// Registration order confirmed in precision.go: pvc (index 11) is registered
// before configmap (index 13).
func TestRunPrecisionAnalyzersDeterministicOrder(t *testing.T) {
	items := map[string][]map[string]any{
		"pvc": {pvcFixture("prod", "stuck-pvc", "Pending", map[string]any{})},
		"configmaps": {configMapFixture("prod", "bad-cfg", map[string]any{
			"app.json": "{bad",
		}, nil, nil, nil)},
		"pods": {},
	}
	a := New(fakeReader{items: items}, Options{Namespace: "prod"})

	first, _ := a.runPrecisionAnalyzers(scanContextWithItems(items))
	firstStatuses := statusesOf(first)
	pvcIdx, cmIdx := indexOfStatus(firstStatuses, "Pending"), indexOfStatus(firstStatuses, "ConfigMapInvalidFormat")
	if pvcIdx < 0 || cmIdx < 0 {
		t.Fatalf("expected both a pvc (Pending) and a configmap (ConfigMapInvalidFormat) finding, got %v", firstStatuses)
	}
	if pvcIdx > cmIdx {
		t.Fatalf("pvc analyzer registered before configmap; findings out of order: %v", firstStatuses)
	}

	// Determinism: repeated runs produce identical ordering.
	for i := 0; i < 8; i++ {
		got, _ := a.runPrecisionAnalyzers(scanContextWithItems(items))
		if !reflect.DeepEqual(statusesOf(got), firstStatuses) {
			t.Fatalf("run %d produced different order: %v vs %v", i, statusesOf(got), firstStatuses)
		}
	}
}

func statusesOf(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Status)
	}
	return out
}

func indexOfStatus(statuses []string, status string) int {
	for i, s := range statuses {
		if s == status {
			return i
		}
	}
	return -1
}
