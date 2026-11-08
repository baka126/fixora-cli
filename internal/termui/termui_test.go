package termui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

func TestWhyWrapsLongEvidenceAndRecommendations(t *testing.T) {
	long := strings.Repeat("evidence ", 40)
	longToken := "sha256:" + strings.Repeat("a", 180)
	finding := analyzer.Finding{
		Summary:         "The image platform does not match the node architecture.",
		Evidence:        []analyzer.Evidence{{Label: "Ranked public image candidate", Value: longToken}},
		Recommendations: []analyzer.Recommendation{{Title: "Use a compatible image", Description: long}},
	}
	var output bytes.Buffer
	Why(&output, finding, fix.Plan{}, true, Options{Wide: true})

	for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if len(line) > wideTextWidth {
			t.Fatalf("rendered line exceeds width %d: %d %q", wideTextWidth, len(line), line)
		}
	}
	if !strings.Contains(output.String(), "  - Ranked public image candidate:") {
		t.Fatalf("expected evidence label in output: %s", output.String())
	}
}

func TestPlanWrapsLongSteps(t *testing.T) {
	plan := fix.Plan{Resource: "Pod/api", Steps: []string{strings.Repeat("review the rendered patch ", 12)}}
	var output bytes.Buffer
	Plan(&output, plan, Options{Wide: true})

	for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if len(line) > wideTextWidth {
			t.Fatalf("rendered line exceeds width %d: %d %q", wideTextWidth, len(line), line)
		}
	}
}

func TestUnresolvedFixNextStepUsesCurrentFailingContainer(t *testing.T) {
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "prod", Status: "CrashLoopBackOff", Evidence: []analyzer.Evidence{{Label: "Exit code init", Value: "1"}, {Label: "Container command init", Value: "OLD_INIT_COMMAND"}, {Label: "Container command app", Value: "CURRENT_APP_COMMAND"}, {Label: "Exit code app", Value: "2"}, {Label: "Failing container", Value: "app"}}}
	var out bytes.Buffer
	UnresolvedFix(&out, f, fix.Plan{Strategy: "runtime"}, false, Options{Wide: true})
	if strings.Contains(out.String(), "OLD_INIT_COMMAND") || strings.Contains(out.String(), "CURRENT_APP_COMMAND") || !strings.Contains(out.String(), "app's intended startup behavior") {
		t.Fatalf("default output should stay concise and identify the failing container: %s", out.String())
	}
}

func TestUnresolvedFixShowsCompactAIResultByDefault(t *testing.T) {
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "prod", Status: "CrashLoopBackOff", Summary: "Container app exited with code 1.", AI: &analyzer.AIResult{Summary: "The configured startup command exits 1 after printing an error.", RootCause: "Full evidence-based causal explanation with logs, events, and alternative hypotheses.", RecommendedFix: "Check the owning manifest or rollout history and restore the intended command before retrying the deployment.", Commands: []string{"kubectl logs deployment/api -n prod --previous"}, Warnings: []string{"Do not guess an application startup command."}}}
	var out bytes.Buffer
	UnresolvedFix(&out, f, fix.Plan{Strategy: "runtime"}, false, Options{Wide: true})
	for _, want := range []string{"Cause: The configured startup command exits 1", "Next: Check the owning manifest", "No repair proposed"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("compact result missing %q: %s", want, out.String())
		}
	}
	for _, unwanted := range []string{"Full evidence-based causal explanation", "kubectl logs deployment/api", "Do not guess an application"} {
		if strings.Contains(out.String(), unwanted) {
			t.Fatalf("default result leaked details %q: %s", unwanted, out.String())
		}
	}
}

func TestUnresolvedFixShowsAIDetailsWithProof(t *testing.T) {
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "prod", Status: "CrashLoopBackOff", AI: &analyzer.AIResult{Summary: "Short cause", RootCause: "Full evidence-based causal explanation", RecommendedFix: "Restore the known working source command", Commands: []string{"kubectl logs deployment/api -n prod --previous"}, Warnings: []string{"Do not guess an application startup command."}}}
	var out bytes.Buffer
	UnresolvedFix(&out, f, fix.Plan{Strategy: "runtime"}, true, Options{Wide: true})
	for _, want := range []string{"Full evidence-based causal explanation", "kubectl logs deployment/api", "Do not guess an application"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("proof output missing %q: %s", want, out.String())
		}
	}
}

func TestFixPreviewUsesCompleteAIStartSentences(t *testing.T) {
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "prod", Status: "OOMKilled", AI: &analyzer.AIResult{
		Summary:        "The memory-hog container was OOM-killed and is restarting. This second sentence adds more explanation that should be hidden by the compact view.",
		RecommendedFix: "Raise the memory limit from 32Mi to at least 64Mi to cover the observed allocation. This additional sentence discusses verification details and should be available in proof output.",
	}}
	plan := fix.Plan{Resource: "Deployment/api", PatchTemplate: "spec: {}"}
	var out bytes.Buffer
	FixPreview(&out, f, plan, Options{Wide: true, NoColor: true})
	got := out.String()
	for _, want := range []string{"OOM-killed and is restarting.", "at least 64Mi to cover the observed allocation."} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact preview missing complete sentence %q: %s", want, got)
		}
	}
	for _, unwanted := range []string{"This second sentence", "This additional sentence", "..."} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("compact preview should not truncate mid-sentence or include later sentences: %s", got)
		}
	}
}

func TestWhyShowsAIDetailsOnlyWithProof(t *testing.T) {
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Summary: "Container failed.", AI: &analyzer.AIResult{RootCause: "Full AI explanation"}}
	var out bytes.Buffer
	Why(&out, f, fix.Plan{}, false, Options{Wide: true})
	if strings.Contains(out.String(), "Full AI explanation") {
		t.Fatalf("default why output should keep AI details collapsed: %s", out.String())
	}
	out.Reset()
	Why(&out, f, fix.Plan{}, true, Options{Wide: true})
	if !strings.Contains(out.String(), "Full AI explanation") {
		t.Fatalf("--proof should reveal AI details: %s", out.String())
	}
}

func TestAIExplanationIncludesDiagnosticCommandsAndCaveats(t *testing.T) {
	var out bytes.Buffer
	f := analyzer.Finding{AI: &analyzer.AIResult{RootCause: "Startup probe times out before initialization completes.", RecommendedFix: "Adjust the startup probe after measuring initialization.", Commands: []string{"kubectl logs deployment/api -n demo --previous"}, Warnings: []string{"Confirm initialization duration before changing the threshold."}}}
	AIExplanation(&out, f, Options{NoColor: true})
	for _, want := range []string{"kubectl logs deployment/api -n demo --previous", "Confirm initialization duration"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing useful AI detail %q: %s", want, out.String())
		}
	}
}
