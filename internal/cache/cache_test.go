package cache

import (
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"testing"
)

func TestKeyChangesWhenWorkloadEvidenceChanges(t *testing.T) {
	a := analyzer.Finding{ID: "prod/api", Summary: "crash", Status: "CrashLoopBackOff", Evidence: []analyzer.Evidence{{Label: "Container command", Value: "old command"}}}
	b := a
	b.Evidence = []analyzer.Evidence{{Label: "Container command", Value: "new command"}}
	if Key(a) == Key(b) {
		t.Fatal("must not reuse a remediation after its workload evidence changes")
	}
}

func TestKeyIncludesModelAndPrompt(t *testing.T) {
	f := analyzer.Finding{ID: "prod/api"}
	if Key(f, "gemini", "old-model", "prompt") == Key(f, "gemini", "new-model", "prompt") || Key(f, "gemini", "model", "old prompt") == Key(f, "gemini", "model", "new prompt") {
		t.Fatal("must not reuse AI across model or prompt changes")
	}
}
