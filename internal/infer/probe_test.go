package infer

import (
	"context"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

// probeFinding mirrors the evidence shape internal/analyzer's probePortEvidence
// really attaches for a ProbeFailure pod: the container image (for the failing
// container name), the ports the not-ready container declares, and the port its
// readiness probe targets.
func probeFinding(declared, probePort string) analyzer.Finding {
	return analyzer.Finding{
		Status: "ProbeFailure",
		Evidence: []analyzer.Evidence{
			{Label: "Container image web-app", Value: "python:3.9-slim"},
			{Label: "Container ports", Value: declared},
			{Label: "Readiness probe port", Value: probePort},
		},
	}
}

func TestProbeInferrerCorrectsPort(t *testing.T) {
	res, ok, err := (probeInferrer{}).Infer(context.Background(), nil, probeFinding("8080", "80"), fix.Plan{Strategy: "probe"})
	if err != nil || !ok {
		t.Fatalf("want accept, got ok=%v err=%v", ok, err)
	}
	if res.Options.ProbePort != "8080" {
		t.Fatalf("want 8080, got %q", res.Options.ProbePort)
	}
	if res.Options.Container != "web-app" {
		t.Fatalf("want web-app, got %q", res.Options.Container)
	}
}

func TestProbeInferrerDeclinesOnMultiplePorts(t *testing.T) {
	// With two candidate ports there is no single right answer, and a wrong
	// guess produces a container that never becomes Ready.
	if _, ok, _ := (probeInferrer{}).Infer(context.Background(), nil, probeFinding("8080,9090", "80"), fix.Plan{Strategy: "probe"}); ok {
		t.Fatal("multiple declared ports must decline")
	}
}

func TestProbeInferrerDeclinesWithNoDeclaredPort(t *testing.T) {
	if _, ok, _ := (probeInferrer{}).Infer(context.Background(), nil, probeFinding("", "80"), fix.Plan{Strategy: "probe"}); ok {
		t.Fatal("no declared port must decline")
	}
}

func TestProbeInferrerDeclinesWhenPortAlreadyMatches(t *testing.T) {
	if _, ok, _ := (probeInferrer{}).Infer(context.Background(), nil, probeFinding("8080", "8080"), fix.Plan{Strategy: "probe"}); ok {
		t.Fatal("a matching probe port is not the problem; must decline")
	}
}

func TestProbeInferrerDeclinesOnNamedProbePort(t *testing.T) {
	// A named probe port (port: http) that resolves correctly is not a
	// misconfiguration; replacing it with a number would "fix" nothing.
	if _, ok, _ := (probeInferrer{}).Infer(context.Background(), nil, probeFinding("8080", "http"), fix.Plan{Strategy: "probe"}); ok {
		t.Fatal("a named probe port must decline")
	}
}

func TestProbeInferrerDeclinesOnMissingContainer(t *testing.T) {
	f := probeFinding("8080", "80")
	f.Evidence = f.Evidence[1:] // drop the "Container image web-app" evidence
	if _, ok, _ := (probeInferrer{}).Infer(context.Background(), nil, f, fix.Plan{Strategy: "probe"}); ok {
		t.Fatal("no failing-container evidence must decline")
	}
}

func TestProbeInferrerDeclinesOnNonNumericDeclaredPort(t *testing.T) {
	// The declared value derives from cluster data and is substituted textually
	// into the patch; a value carrying a newline would inject arbitrary YAML.
	if _, ok, _ := (probeInferrer{}).Infer(context.Background(), nil, probeFinding("8080\nfoo: bar", "80"), fix.Plan{Strategy: "probe"}); ok {
		t.Fatal("a non-numeric declared port must decline")
	}
}

func TestProbeInferrerHandlesProbeStrategy(t *testing.T) {
	if !(probeInferrer{}).Handles(fix.Plan{Strategy: "probe"}) {
		t.Fatal("must handle the probe strategy")
	}
	if (probeInferrer{}).Handles(fix.Plan{Strategy: "resources"}) {
		t.Fatal("must not handle other strategies")
	}
}

// TestProbeInferrerConcretePlanIsApplyEligible builds a real ProbeFailure
// finding carrying the port evidence probePortEvidence emits, runs it through
// fix.BuildPlan then infer.Concrete, and asserts the concretized plan comes
// back ApplyEligible with both probe placeholders — TODO_CONTAINER_NAME and
// TODO_PROBE_PORT — filled. A branch that leaves either unfilled silently fails
// Concretize's validation gate and concreteWith discards the whole Result.
func TestProbeInferrerConcretePlanIsApplyEligible(t *testing.T) {
	f := analyzer.Finding{
		ResourceKind: "Deployment", ResourceName: "probe-demo", Namespace: "default",
		Status: "ProbeFailure",
		Evidence: []analyzer.Evidence{
			{Label: "Container image web-app", Value: "python:3.9-slim"},
			{Label: "Container ports", Value: "8080"},
			{Label: "Readiness probe port", Value: "80"},
		},
	}
	plan := fix.BuildPlan(f)
	got, ok := Concrete(context.Background(), nil, f, plan)
	if !ok {
		t.Fatalf("want a concretized plan, got ok=false (blocked: %v)", got.BlockedReasons)
	}
	if !got.ApplyEligible {
		t.Fatalf("want ApplyEligible, got false (blocked: %v)", got.BlockedReasons)
	}
	if strings.Contains(got.PatchTemplate, "TODO_") {
		t.Fatalf("patch still has an unsubstituted placeholder:\n%s", got.PatchTemplate)
	}
}
