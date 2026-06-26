package cli

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
)

func TestInteractiveFixDetection(t *testing.T) {
	o := options{output: "text", promptInput: bufio.NewReader(strings.NewReader(""))}
	if !interactiveFix(o) {
		t.Fatal("expected interactive when promptInput set and output=text")
	}
	o.yes = true
	if interactiveFix(o) {
		t.Fatal("expected non-interactive when --yes set")
	}
	o.yes = false
	o.output = "json"
	if interactiveFix(o) {
		t.Fatal("expected non-interactive when output != text")
	}
}

func TestWalkthroughQuitAtRootCause(t *testing.T) {
	finding := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Summary: "no cpu request"}
	plan := fix.Plan{Resource: "deployment/api"}
	opts := options{output: "text", promptInput: bufio.NewReader(strings.NewReader("q\n"))}
	var stdout, stderr bytes.Buffer
	code := runFixWalkthrough(context.Background(), &stdout, &stderr, opts, kube.Kubectl{}, finding, plan, "deployment/api")
	if code != 0 {
		t.Fatalf("quit should exit 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Root cause") {
		t.Fatalf("expected Step 1 header, got %q", stdout.String())
	}
}

func TestWalkthroughReviewOnlyPatchFile(t *testing.T) {
	finding := analyzer.Finding{ResourceKind: "Service", ResourceName: "web", Summary: "selector mismatch"}
	// Non-apply-eligible plan with a concrete, safe review patch so
	// hasConcreteReviewPatch returns true (non-empty PatchTemplate, no TODO_ / leading #).
	plan := fix.Plan{
		Resource:       "service/web",
		Strategy:       "repair-selector",
		PatchTemplate:  "spec:\n  selector:\n    app: web\n",
		BlockedReasons: []string{"selector change is review-only"},
	}
	// Step 1 continue, then delivery choice 3 (patch file).
	opts := options{
		output:      "text",
		promptInput: bufio.NewReader(strings.NewReader("\n3\n")),
		outFile:     t.TempDir() + "/patch.yaml",
	}
	var stdout, stderr bytes.Buffer
	code := runFixWalkthrough(context.Background(), &stdout, &stderr, opts, kube.Kubectl{}, finding, plan, "service/web")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "review-only") {
		t.Fatalf("expected review-only messaging, got %q", stdout.String())
	}
}
