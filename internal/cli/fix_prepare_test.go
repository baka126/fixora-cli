package cli

import (
	"context"
	"io"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func TestPrepareFixPreservesStartupPinning(t *testing.T) {
	path, r, f, _ := startupFixture(t, validStartupBaseline)
	t.Setenv("PATH", t.TempDir())
	_, p, opts, err := prepareAnalyzedFixPlan(context.Background(), r, kube.Kubectl{}, options{startupBaseline: path}, "Deployment/worker", f, io.Discard)
	if err != nil || !p.ApplyEligible || p.PolicyProof == nil || !opts.shadowVerify || opts.shadowRetries != 0 {
		t.Fatalf("shared preparation lost startup policy: %+v %v", p, err)
	}
}
