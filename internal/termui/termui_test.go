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
