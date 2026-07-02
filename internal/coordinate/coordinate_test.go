package coordinate

import (
	"context"
	"errors"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

// fakeDeps keys behavior on the step's PatchFile (tests set PatchFile == Ref).
type fakeDeps struct {
	dryRunErr  map[string]error
	applyErr   map[string]error
	captureErr map[string]error
	restoreErr map[string]error
	healthy    map[string]bool
	applied    []string // order of successful Apply
	restored   []string // order of Restore calls
}

func (f *fakeDeps) DryRunApply(_ context.Context, file string) error { return f.dryRunErr[file] }
func (f *fakeDeps) Apply(_ context.Context, file string) error {
	if err := f.applyErr[file]; err != nil {
		return err
	}
	f.applied = append(f.applied, file)
	return nil
}
func (f *fakeDeps) Capture(_ context.Context, _, name, _ string) ([]byte, error) {
	if err := f.captureErr[name]; err != nil {
		return nil, err
	}
	return []byte("prior:" + name), nil
}
func (f *fakeDeps) Restore(_ context.Context, manifest []byte) error {
	name := string(manifest)
	f.restored = append(f.restored, name)
	return f.restoreErr[name]
}
func (f *fakeDeps) Verify(_ context.Context, step Step) (bool, string) {
	if h, ok := f.healthy[step.PatchFile]; ok {
		return h, "verify:" + step.PatchFile
	}
	return true, "healthy"
}

func step(ref string, eligible bool) Step {
	return Step{
		Ref: ref, Kind: "Deployment", Name: ref, Namespace: "prod",
		PatchFile: ref,
		Finding:   analyzer.Finding{ResourceKind: "Deployment", ResourceName: ref, Namespace: "prod"},
		Plan:      fix.Plan{ApplyEligible: eligible},
	}
}

func never() bool                      { return false }
func always() bool                     { return true }
func notManaged(analyzer.Finding) bool { return false }

func TestRunPreflightAbortsOnIneligible(t *testing.T) {
	d := &fakeDeps{}
	rep := Run(context.Background(), []Step{step("a", true), step("b", false)}, d, notManaged, always, always)
	if !rep.Aborted || rep.Mutated {
		t.Fatalf("expected aborted with no mutation, got %#v", rep)
	}
	if len(d.applied) != 0 {
		t.Fatalf("no Apply should happen on preflight abort, got %v", d.applied)
	}
}

func TestRunPreflightAbortsOnSourceManaged(t *testing.T) {
	d := &fakeDeps{}
	managed := func(f analyzer.Finding) bool { return f.ResourceName == "b" }
	rep := Run(context.Background(), []Step{step("a", true), step("b", true)}, d, managed, always, always)
	if !rep.Aborted || len(d.applied) != 0 {
		t.Fatalf("source-managed step must abort the set, got %#v applied=%v", rep, d.applied)
	}
}

func TestRunPreflightAbortsOnDryRunReject(t *testing.T) {
	d := &fakeDeps{dryRunErr: map[string]error{"b": errors.New("rejected")}}
	rep := Run(context.Background(), []Step{step("a", true), step("b", true)}, d, notManaged, always, always)
	if !rep.Aborted || len(d.applied) != 0 {
		t.Fatalf("dry-run reject must abort the set, got %#v", rep)
	}
}

func TestRunAbortsWhenApplyNotConfirmed(t *testing.T) {
	d := &fakeDeps{}
	rep := Run(context.Background(), []Step{step("a", true)}, d, notManaged, never, always)
	if !rep.Aborted || len(d.applied) != 0 {
		t.Fatalf("declined confirm must abort with no mutation, got %#v", rep)
	}
}

func TestRunHappyPath(t *testing.T) {
	d := &fakeDeps{}
	rep := Run(context.Background(), []Step{step("a", true), step("b", true), step("c", true)}, d, notManaged, always, always)
	if rep.Aborted || !rep.Mutated {
		t.Fatalf("expected non-aborted mutated run, got %#v", rep)
	}
	if len(d.applied) != 3 || len(d.restored) != 0 {
		t.Fatalf("all applied, none restored; applied=%v restored=%v", d.applied, d.restored)
	}
	for _, s := range rep.Steps {
		if !s.Applied || !s.Healthy {
			t.Fatalf("step not applied+healthy: %#v", s)
		}
	}
}

func TestRunMidFailureRollsBackPrefixInReverse(t *testing.T) {
	// step "b" is unhealthy -> a,b applied; c never applied; rollback b then a.
	d := &fakeDeps{healthy: map[string]bool{"b": false}}
	rep := Run(context.Background(), []Step{step("a", true), step("b", true), step("c", true)}, d, notManaged, always, always)
	if len(d.applied) != 2 {
		t.Fatalf("expected a,b applied, got %v", d.applied)
	}
	if len(d.restored) != 2 || d.restored[0] != "prior:b" || d.restored[1] != "prior:a" {
		t.Fatalf("expected reverse rollback [prior:b, prior:a], got %v", d.restored)
	}
	if !rep.Steps[0].RolledBack || !rep.Steps[1].RolledBack {
		t.Fatalf("applied steps must be marked rolled back: %#v", rep.Steps)
	}
	if rep.Steps[2].Applied {
		t.Fatal("step after failure must not be applied")
	}
}

func TestRunMidFailureNonInteractiveLeavesPrefixApplied(t *testing.T) {
	d := &fakeDeps{healthy: map[string]bool{"b": false}}
	rep := Run(context.Background(), []Step{step("a", true), step("b", true)}, d, notManaged, always, never)
	if len(d.restored) != 0 {
		t.Fatalf("non-interactive must not restore, got %v", d.restored)
	}
	if !rep.Mutated {
		t.Fatal("prefix stayed applied; Mutated should be true")
	}
}

func TestRunRestoreErrorContinuesReverseWalk(t *testing.T) {
	d := &fakeDeps{healthy: map[string]bool{"b": false}, restoreErr: map[string]error{"prior:a": errors.New("immutable")}}
	rep := Run(context.Background(), []Step{step("a", true), step("b", true)}, d, notManaged, always, always)
	if len(d.restored) != 2 {
		t.Fatalf("reverse walk must attempt both restores, got %v", d.restored)
	}
	if rep.Steps[0].RolledBack {
		t.Fatal("step a restore failed; must not be marked rolled back")
	}
	if !rep.Steps[1].RolledBack {
		t.Fatal("step b restore succeeded; should be marked rolled back")
	}
}
