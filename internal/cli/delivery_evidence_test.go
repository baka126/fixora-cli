package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
)

// TestCollectDeliveryEvidenceRunsWithoutAI proves the AI-independent evidence
// the infer package consumes — here the `kubectl top` output the OOMKilled
// resources inferrer reads — is gathered on the fix path even when --ai is off.
// It used to live only inside enrichFindingForAI, which the dispatch calls under
// `if opts.useAI`, so under --no-ai the image and OOMKilled-metrics inferrers
// always declined.
func TestCollectDeliveryEvidenceRunsWithoutAI(t *testing.T) {
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s' 'POD NAME CPU MEM\\noomkilled-demo-abc memory-hog 2m 118Mi\\n'\n"
	if err := os.WriteFile(filepath.Join(binDir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	finding := analyzer.Finding{
		Namespace: "default", ResourceKind: "Deployment", ResourceName: "oomkilled-demo",
		PodName: "oomkilled-demo-abc", Status: "OOMKilled",
	}
	got := collectDeliveryEvidence(context.Background(), kube.Kubectl{}, options{namespace: "default"}, finding)

	if !hasEvidenceLabel(got.Evidence, "Metrics pod containers") {
		t.Fatalf("expected 'Metrics pod containers' evidence with useAI false, got %+v", got.Evidence)
	}
}
