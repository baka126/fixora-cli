// Package coordinate applies an ordered, user-vetted set of single-resource
// fixes as a transaction, rolling back the applied prefix on failure.
package coordinate

import (
	"context"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

type Step struct {
	Ref       string
	Kind      string
	Name      string
	Namespace string
	Finding   analyzer.Finding
	Plan      fix.Plan
	PatchFile string
}

type StepReport struct {
	Ref        string
	Applied    bool
	Healthy    bool
	RolledBack bool
	Detail     string
}

type Report struct {
	Steps   []StepReport
	Aborted bool // preflight/confirm rejected the set; nothing mutated
	Mutated bool // at least one Apply succeeded
}

// Deps is the injected side-effect surface. Real impl is kube-backed; tests
// use a fake.
type Deps interface {
	DryRunApply(ctx context.Context, file string) error
	Apply(ctx context.Context, file string) error
	Capture(ctx context.Context, kind, name, namespace string) ([]byte, error)
	Restore(ctx context.Context, manifest []byte) error
	Verify(ctx context.Context, step Step) (healthy bool, detail string)
}

// Run executes the coordinated apply saga. sourceManaged reports whether a
// finding is Helm/GitOps-managed (direct apply blocked). confirmApply is asked
// once before any mutation; confirmRollback before executing a partial
// rollback. Both are func() bool so non-interactive callers can decline.
func Run(ctx context.Context, steps []Step, d Deps, sourceManaged func(analyzer.Finding) bool, confirmApply func() bool, confirmRollback func() bool) Report {
	report := Report{Steps: make([]StepReport, len(steps))}

	// Preflight — fail-closed, zero mutation.
	for i, s := range steps {
		report.Steps[i].Ref = s.Ref
		switch {
		case !s.Plan.ApplyEligible:
			report.Steps[i].Detail = "blocked: not apply-eligible (" + strings.Join(s.Plan.BlockedReasons, "; ") + ")"
			report.Aborted = true
		case sourceManaged(s.Finding):
			report.Steps[i].Detail = "blocked: Helm/GitOps-managed — deliver via --repo, not direct apply"
			report.Aborted = true
		default:
			if err := d.DryRunApply(ctx, s.PatchFile); err != nil {
				report.Steps[i].Detail = "blocked: server dry-run rejected: " + err.Error()
				report.Aborted = true
			} else {
				report.Steps[i].Detail = "preflight ok"
			}
		}
	}
	if report.Aborted {
		return report
	}
	if !confirmApply() {
		report.Aborted = true
		return report
	}

	// Apply loop with capture-before-mutate.
	type captured struct {
		idx      int
		manifest []byte
	}
	var appliedPrefix []captured
	failed := false
	for i := range steps {
		s := steps[i]
		manifest, err := d.Capture(ctx, s.Kind, s.Name, s.Namespace)
		if err != nil {
			report.Steps[i].Detail = "capture failed, not applying: " + err.Error()
			failed = true
			break
		}
		if err := d.Apply(ctx, s.PatchFile); err != nil {
			report.Steps[i].Detail = "apply failed: " + err.Error()
			failed = true
			break
		}
		report.Steps[i].Applied = true
		report.Mutated = true
		appliedPrefix = append(appliedPrefix, captured{idx: i, manifest: manifest})

		healthy, detail := d.Verify(ctx, s)
		report.Steps[i].Healthy = healthy
		report.Steps[i].Detail = detail
		if !healthy {
			failed = true
			break
		}
	}

	if !failed {
		return report
	}

	// Partial rollback of the applied prefix, in reverse, consent-gated.
	if !confirmRollback() {
		for j := len(appliedPrefix) - 1; j >= 0; j-- {
			report.Steps[appliedPrefix[j].idx].Detail += " | rollback available (not run)"
		}
		return report
	}
	for j := len(appliedPrefix) - 1; j >= 0; j-- {
		idx := appliedPrefix[j].idx
		if err := d.Restore(ctx, appliedPrefix[j].manifest); err != nil {
			report.Steps[idx].Detail += " | rollback FAILED: " + err.Error() + " (restore manually)"
			continue
		}
		report.Steps[idx].RolledBack = true
		report.Steps[idx].Detail += " | rolled back"
	}
	return report
}
