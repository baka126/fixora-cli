package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/coordinate"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/ops"
)

// coordinateDeps is the kube-backed implementation of coordinate.Deps.
type coordinateDeps struct {
	k       kube.Kubectl
	timeout time.Duration
}

func (d coordinateDeps) DryRunApply(ctx context.Context, file string) error {
	return d.k.DryRunApply(ctx, file)
}

func (d coordinateDeps) Apply(ctx context.Context, file string) error {
	return d.k.Apply(ctx, file)
}

func (d coordinateDeps) Capture(ctx context.Context, kind, name, namespace string) ([]byte, error) {
	return d.k.Run(ctx, "get", kind+"/"+name, "-n", namespace, "-o", "yaml")
}

func (d coordinateDeps) Restore(ctx context.Context, manifest []byte) error {
	f, err := os.CreateTemp("", "fixora-rollback-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(manifest); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return d.k.Apply(ctx, f.Name())
}

func (d coordinateDeps) Verify(ctx context.Context, step coordinate.Step) (bool, string) {
	var outcome ops.RolloutOutcome
	switch strings.ToLower(strings.TrimSpace(step.Finding.ResourceKind)) {
	case "job", "cronjob":
		outcome = ops.VerifyCompletion(ctx, d.k, step.Finding, step.Plan, d.timeout)
	default:
		outcome = ops.VerifyRollout(ctx, d.k, step.Finding, step.Plan, d.timeout)
	}
	return rolloutHealthy(outcome.Class), outcome.Summary
}

// rolloutHealthy reports whether a rollout/completion class is NOT a failure.
// Mirrors gateRollout in root.go: only the fallthrough branch (Stuck, Timeout,
// Invalid, CompletionFailed, CronJobFailing) is a true failure; everything else
// (healthy, skipped, unknown variants) is treated as non-failing.
func rolloutHealthy(class string) bool {
	switch class {
	case ops.RolloutHealthy, ops.CompletionSucceeded, ops.CronJobHealthy,
		ops.RolloutSkipped, ops.RolloutUnknown,
		ops.CompletionPending, ops.CompletionUnknown,
		ops.CronJobSuspended, ops.CronJobUnknown:
		return true
	default:
		return false
	}
}

// buildCoordinateSteps derives an ordered step list from resource refs, reusing
// the same planning path as `fix` so ApplyEligible is enforced identically.
// Each step's patch is written to a temp file for dry-run/apply.
func buildCoordinateSteps(ctx context.Context, a analyzer.Analyzer, opts options, refs []string) (_ []coordinate.Step, err error) {
	steps := make([]coordinate.Step, 0, len(refs))
	// Clean up patch files written so far if we bail on a later ref.
	defer func() {
		if err != nil {
			for _, s := range steps {
				if s.PatchFile != "" {
					os.Remove(s.PatchFile)
				}
			}
		}
	}()
	for _, ref := range refs {
		finding, err := a.AnalyzeResource(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("analyze %s: %w", ref, err)
		}
		finding = preferSmartFinding(ctx, a, finding)
		plan := fix.BuildPlan(finding)
		plan = fix.Concretize(plan, concreteOptions(opts))

		patchFile := ""
		if strings.TrimSpace(plan.PatchYAML()) != "" {
			f, err := os.CreateTemp("", "fixora-coord-*.yaml")
			if err != nil {
				return nil, err
			}
			if _, err := f.WriteString(plan.PatchYAML()); err != nil {
				f.Close()
				return nil, err
			}
			f.Close()
			patchFile = f.Name()
		}
		steps = append(steps, coordinate.Step{
			Ref:       finding.ResourceKind + "/" + finding.ResourceName,
			Kind:      finding.ResourceKind,
			Name:      finding.ResourceName,
			Namespace: finding.Namespace,
			Finding:   finding,
			Plan:      plan,
			PatchFile: patchFile,
		})
	}
	return steps, nil
}

// runCoordinateSteps executes the saga over prebuilt steps and prints a report.
// Exit: 0 all healthy; 2 preflight/confirm abort (no mutation); 1 a step failed.
func runCoordinateSteps(ctx context.Context, stdout, stderr io.Writer, steps []coordinate.Step, d coordinate.Deps, confirmApply, confirmRollback func() bool) int {
	report := coordinate.Run(ctx, steps, d, sourceManaged, confirmApply, confirmRollback)

	fmt.Fprintln(stdout, "Coordinated fix report")
	fmt.Fprintln(stdout, strings.Repeat("=", 22))
	for _, s := range report.Steps {
		status := "pending"
		switch {
		case s.RolledBack:
			status = "rolled-back"
		case s.Applied && s.Healthy:
			status = "applied"
		case s.Applied && !s.Healthy:
			status = "applied-unhealthy"
		}
		fmt.Fprintf(stdout, "- %s [%s] %s\n", s.Ref, status, s.Detail)
	}

	if report.Aborted {
		fmt.Fprintln(stderr, "coordinated apply aborted; no changes were made")
		return 2
	}
	for _, s := range report.Steps {
		if s.Applied && !s.Healthy {
			fmt.Fprintln(stderr, "coordinated apply failed; applied steps were rolled back where possible")
			return 1
		}
	}
	fmt.Fprintln(stdout, "coordinated apply healthy")
	return 0
}
