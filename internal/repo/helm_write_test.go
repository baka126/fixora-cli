package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHelmWriteCertifiedReviewAndFreshness(t *testing.T) {
	req, d := helmEffectRequest(t)
	plan, err := PrepareHelmSource(context.Background(), req, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = WriteValidatedHelmSource(context.Background(), plan, d, false); err == nil {
		t.Fatal("automatic write without runtime receipt")
	}
	result, err := WriteValidatedHelmSource(context.Background(), plan, d, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceStatus != "source-written" || result.Reconciliation != "pending" {
		t.Fatal(result)
	}
	if err = plan.Proof.CheckWritten(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(req.Context.RepoPath, req.Context.EditValuesPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(plan.Proof.candidate.edited) {
		t.Fatal("wrong bytes")
	}
	if _, err = WriteValidatedHelmSource(context.Background(), plan, d, true); err == nil {
		t.Fatal("replay accepted")
	}
}
func TestHelmWriteRefusesStaleAndModifiedPlans(t *testing.T) {
	for _, bad := range []string{"source", "live", "public", "cancel", "proof"} {
		t.Run(bad, func(t *testing.T) {
			req, d := helmEffectRequest(t)
			plan, err := PrepareHelmSource(context.Background(), req, d)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			switch bad {
			case "source":
				os.WriteFile(filepath.Join(req.Context.RepoPath, req.Context.EditValuesPath), []byte("setting: operator\n"), 0600)
			case "live":
				d.live["metadata"].(map[string]any)["uid"] = "replacement"
			case "public":
				plan.Path = "other.yaml"
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "proof":
				plan.Proof = &HelmSourceProof{}
			}
			if _, err := WriteValidatedHelmSource(ctx, plan, d, true); err == nil {
				t.Fatal("unsafe write accepted")
			}
		})
	}
}

func TestHelmWritePendingReviewPreservesMode(t *testing.T) {
	req, d := helmEffectRequest(t)
	d.serverPending = true
	dest := filepath.Join(req.Context.RepoPath, req.Context.EditValuesPath)
	if err := os.Chmod(dest, 0640); err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareHelmSource(context.Background(), req, d)
	if err != nil {
		t.Fatal(err)
	}
	result, err := WriteValidatedHelmSource(context.Background(), plan, d, true)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatal(info, err)
	}
	if result.ServerValidation != "pending" || result.RuntimeVerification != "not-requested" {
		t.Fatal(result)
	}
	if err = os.WriteFile(dest, []byte("setting: after\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err = plan.Proof.CheckWritten(context.Background(), d); err == nil {
		t.Fatal("post-write drift accepted")
	}
}
