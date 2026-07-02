package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/coordinate"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/ops"
)

type fakeCoordDeps struct {
	applied   []string
	restored  int
	unhealthy map[string]bool
	applyErr  map[string]error
}

func (f *fakeCoordDeps) DryRunApply(context.Context, string) error { return nil }
func (f *fakeCoordDeps) Apply(_ context.Context, file string) error {
	if err := f.applyErr[file]; err != nil {
		return err
	}
	f.applied = append(f.applied, file)
	return nil
}
func (f *fakeCoordDeps) Capture(context.Context, string, string, string) ([]byte, error) {
	return []byte("prior"), nil
}
func (f *fakeCoordDeps) Restore(context.Context, []byte) error { f.restored++; return nil }
func (f *fakeCoordDeps) Verify(_ context.Context, s coordinate.Step) (bool, string) {
	if f.unhealthy[s.PatchFile] {
		return false, "unhealthy"
	}
	return true, "healthy"
}

func coordStep(ref string, eligible bool) coordinate.Step {
	return coordinate.Step{
		Ref: ref, Kind: "Deployment", Name: ref, Namespace: "prod", PatchFile: ref,
		Finding: analyzer.Finding{ResourceKind: "Deployment", ResourceName: ref, Namespace: "prod"},
		Plan:    fix.Plan{ApplyEligible: eligible},
	}
}

func TestRunCoordinateStepsHappyPathExit0(t *testing.T) {
	var out, errw bytes.Buffer
	d := &fakeCoordDeps{}
	code := runCoordinateSteps(context.Background(), &out, &errw, []coordinate.Step{coordStep("a", true), coordStep("b", true)}, d, func() bool { return true }, func() bool { return true })
	if code != 0 {
		t.Fatalf("healthy coordinated apply should exit 0, got %d (%s)", code, errw.String())
	}
	if len(d.applied) != 2 {
		t.Fatalf("expected 2 applies, got %v", d.applied)
	}
}

func TestRunCoordinateStepsPreflightAbortExit2(t *testing.T) {
	var out, errw bytes.Buffer
	d := &fakeCoordDeps{}
	code := runCoordinateSteps(context.Background(), &out, &errw, []coordinate.Step{coordStep("a", true), coordStep("b", false)}, d, func() bool { return true }, func() bool { return true })
	if code != 2 {
		t.Fatalf("preflight abort should exit 2, got %d", code)
	}
	if len(d.applied) != 0 {
		t.Fatal("no apply on preflight abort")
	}
}

func TestRunCoordinateStepsFailureRollbackExit1(t *testing.T) {
	var out, errw bytes.Buffer
	d := &fakeCoordDeps{unhealthy: map[string]bool{"b": true}}
	code := runCoordinateSteps(context.Background(), &out, &errw, []coordinate.Step{coordStep("a", true), coordStep("b", true)}, d, func() bool { return true }, func() bool { return true })
	if code != 1 {
		t.Fatalf("failed coordinated apply should exit 1, got %d", code)
	}
	if d.restored == 0 {
		t.Fatal("expected rollback restores on failure")
	}
}

func TestRolloutHealthyMapping(t *testing.T) {
	// These classes are treated as non-failing by gateRollout (warn or success).
	healthy := []string{
		ops.RolloutHealthy, ops.CompletionSucceeded, ops.CronJobHealthy,
		ops.RolloutSkipped, ops.RolloutUnknown,
		ops.CompletionPending, ops.CompletionUnknown,
		ops.CronJobSuspended, ops.CronJobUnknown,
	}
	for _, c := range healthy {
		if !rolloutHealthy(c) {
			t.Fatalf("class %q should be treated as non-failing", c)
		}
	}
	// These are the genuine failure classes (fall through the switch in gateRollout).
	failing := []string{ops.RolloutStuck, ops.RolloutTimeout, ops.RolloutInvalid, ops.CompletionFailed, ops.CronJobFailing}
	for _, c := range failing {
		if rolloutHealthy(c) {
			t.Fatalf("class %q must be treated as failing", c)
		}
	}
}
