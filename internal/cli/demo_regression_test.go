package cli

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"gopkg.in/yaml.v3"
)

// Protect the independently approved crashloop-demo recovery across the
// baseline loader, AI selection and shadow policy. Ready is not proof that an
// arbitrary replacement command is the intended application.
func TestDemoStartupBaselineRegression(t *testing.T) {
	path, reader, finding, plan := startupFixture(t, validStartupBaseline)
	got, err := withStartupBaseline(context.Background(), reader, path, finding, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ApplyEligible || got.Strategy != "restore-command" || !hasString(got.Guardrails, "shadow-required") {
		t.Fatalf("lost verified-baseline path or shadow requirement: %+v", got)
	}
	if err := shadow.ValidateCandidatePatch(got.PatchYAML(), got.Strategy, got.Resource, got.Namespace); err != nil {
		t.Fatalf("approved baseline cannot reach shadow: %v", err)
	}
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(got.PatchYAML()), &obj); err != nil {
		t.Fatal(err)
	}
	pod := obj["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	containers := pod["containers"].([]any)
	want := map[string]any{"name": "app", "command": []any{"httpd", "-f", "-p", "8080"}, "args": []any{}}
	if len(pod) != 1 || len(containers) != 1 || !reflect.DeepEqual(containers[0], want) {
		t.Fatalf("baseline must restore only the approved process: %#v", pod)
	}
	finding.AI = &analyzer.AIResult{PatchYAML: aiProbeCandidate, Confidence: 99}
	selected := selectAIRepair(context.Background(), reader, got, finding, &bytes.Buffer{}, false)
	if selected.PatchYAML() != got.PatchYAML() || !hasString(selected.Guardrails, "shadow-required") {
		t.Fatal("AI substituted a different repair for the approved baseline")
	}
	if err := shadow.ValidateRevisedPatch(got.PatchYAML(), got.PatchYAML(), "restore-command"); err == nil {
		t.Fatal("AI shadow retries must not revise an approved startup baseline")
	}
}
