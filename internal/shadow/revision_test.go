package shadow

import (
	"context"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type mockAI struct {
	patch string
}

func (m mockAI) Explain(ctx context.Context, finding analyzer.Finding) (*analyzer.AIResult, error) {
	return &analyzer.AIResult{RecommendedFix: m.patch}, nil
}

func TestRevisePatchWithMockAI(t *testing.T) {
	patch := `kind: Pod`
	mock := mockAI{patch: "kind: Pod\n  some: modified_patch"}
	revised, ok := revisePatch(context.Background(), mock, patch, Attempt{ExitReason: "OOMKilled"})
	if !ok {
		t.Fatal("expected patch revision")
	}
	if !strings.Contains(revised, "modified_patch") {
		t.Fatalf("expected mocked patch, got:\n%s", revised)
	}
}

func TestRevisePatchNilProvider(t *testing.T) {
	revised, ok := revisePatch(context.Background(), nil, "kind: Pod\n", Attempt{ExitReason: "CrashLoopBackOff"})
	if ok {
		t.Fatalf("unexpected revision: %s", revised)
	}
}
