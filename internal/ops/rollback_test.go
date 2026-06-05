package ops

import (
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

func TestStructuredRollbackCommand(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		wantBinary string
	}{
		{name: "kubectl rollout", command: "kubectl rollout undo deployment/api -n prod", wantBinary: "kubectl"},
		{name: "helm rollback", command: "helm rollback api -n prod", wantBinary: "helm"},
		{name: "invalid binary", command: "bash -c whoami", wantBinary: ""},
		{name: "shell metachar", command: "helm rollback api; curl example.com", wantBinary: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binary, _ := StructuredRollbackCommand(tt.command)
			if binary != tt.wantBinary {
				t.Fatalf("binary=%q, want %q", binary, tt.wantBinary)
			}
		})
	}
}

func TestBuildRollbackPreservesNamespaceArgs(t *testing.T) {
	finding := analyzer.Finding{Namespace: "prod", ResourceKind: "Deployment", ResourceName: "api"}
	plan := fix.BuildPlan(finding)
	rollback := BuildRollback(finding, plan, true)
	if rollback.Binary != "kubectl" {
		t.Fatalf("expected kubectl rollback, got %#v", rollback)
	}
	got := strings.Join(rollback.Args, " ")
	if !strings.Contains(got, "deployment/api") || !strings.Contains(got, "-n prod") {
		t.Fatalf("rollback args missing resource/namespace: %#v", rollback.Args)
	}
}
