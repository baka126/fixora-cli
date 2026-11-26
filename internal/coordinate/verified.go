package coordinate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/ops"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
)

const RollbackRestored = "rollback-restored"

type CertifiedStep struct {
	Step         Step
	Proof        *repairpolicy.Proof   `json:"-"`
	Receipt      *repairpolicy.Receipt `json:"-"`
	InversePatch string                `json:"-"`
	Before       repairpolicy.Snapshot `json:"-"`
}

// CheckGraphCertifiedSteps validates the shared graph receipt and every
// workload candidate before graph-coordinated apply. Unlike the legacy
// independent-target path, this check permits selected ownership edges because
// the candidates were evaluated together in one shadow graph.
func CheckGraphCertifiedSteps(steps []CertifiedStep, certificate GraphCertificate, current GraphSnapshot) error {
	if err := certificate.Check(current); err != nil {
		return err
	}
	workloadCandidates := map[string]string{}
	for _, node := range certificate.Snapshot.Nodes {
		if isGraphWorkload(node.Kind) {
			workloadCandidates[node.Namespace+"/"+node.Kind+"/"+node.Name] = node.Candidate
		}
	}
	if len(steps) != len(workloadCandidates) || len(steps) < 2 {
		return errors.New("graph certificate workload set differs from coordinated steps")
	}
	seen := map[string]bool{}
	for _, step := range steps {
		if err := step.Check(); err != nil {
			return err
		}
		key := step.Step.Namespace + "/" + step.Step.Kind + "/" + step.Step.Name
		candidate, ok := workloadCandidates[key]
		if !ok || seen[key] || candidate != step.Step.Plan.PatchYAML() {
			return errors.New("coordinated step candidate differs from graph shadow certificate")
		}
		seen[key] = true
	}
	if len(seen) != len(workloadCandidates) {
		return errors.New("graph certificate is missing a coordinated workload")
	}
	return nil
}

type VerifiedDeps interface {
	Preflight(context.Context, CertifiedStep) (ops.TargetSnapshot, error)
	Apply(context.Context, CertifiedStep, ops.TargetSnapshot) error
	Verify(context.Context, CertifiedStep) (ops.RolloutOutcome, error)
	RestoreFields(context.Context, CertifiedStep) (ops.RolloutOutcome, error)
}

func (c CertifiedStep) Check() error {
	s := c.Step
	p := fix.WithTargetPolicy(s.Plan)
	if s.Kind != "Deployment" && s.Kind != "StatefulSet" && s.Kind != "DaemonSet" {
		return errors.New("coordinated delivery supports independent Deployment, StatefulSet and DaemonSet repairs only")
	}
	if !p.CanApply || !p.ApplyEligible || s.Ref != s.Kind+"/"+s.Name || p.Resource != s.Ref || p.Namespace != s.Namespace {
		return errors.New("coordinated step is not an eligible exact workload target")
	}
	g := s.Finding.GitOps
	if strings.EqualFold(g.ManagedBy, "helm") || g.HelmRelease != "" || g.HelmChart != "" || g.TargetAdvice != "" || g.FluxHint != "" || g.ArgoHint != "" {
		return errors.New("managed workload requires source delivery")
	}
	if c.Proof == nil || p.PolicyProof != c.Proof || c.Proof.Family() != p.Strategy {
		return errors.New("fresh policy proof is required")
	}
	target := c.Proof.Target()
	if target.Kind != s.Kind || target.Name != s.Name || target.Namespace != s.Namespace || target.UID == "" || target.UID != p.TargetUID {
		return errors.New("certificate target differs from step")
	}
	if err := c.Proof.Check(c.Before, p.PatchYAML()); err != nil {
		return err
	}
	if err := c.Receipt.Check(c.Proof, p.PatchYAML()); err != nil {
		return err
	}
	inverse, err := BuildInversePatch(c.Before.Object, p.PatchYAML())
	if err != nil || inverse != c.InversePatch {
		return errors.New("field inverse is missing or changed")
	}
	return nil
}

// RunVerified is an ordered apply/verify saga, not an atomic Kubernetes transaction.
func RunVerified(ctx context.Context, steps []CertifiedStep, d VerifiedDeps, confirmApply, confirmRollback func() bool) Report {
	report := Report{Steps: make([]StepReport, len(steps))}
	seen := map[string]bool{}
	for i, c := range steps {
		report.Steps[i].Ref = c.Step.Ref
		key := c.Step.Namespace + "/" + c.Step.Ref
		err := c.Check()
		if seen[key] {
			err = errors.New("duplicate/conflicting coordinated target")
		}
		seen[key] = true
		if err != nil {
			report.Steps[i].Detail = "blocked: " + err.Error()
			report.Aborted = true
		} else {
			report.Steps[i].Verification = "shadow-verified"
		}
	}
	if err := CheckIndependentSteps(steps); err != nil {
		report.Aborted = true
		if len(report.Steps) > 0 {
			report.Steps[0].Detail = err.Error()
		}
	}
	if len(steps) < 2 || d == nil {
		report.Aborted = true
	}
	if report.Aborted {
		return report
	}
	for i, c := range steps {
		if ctx.Err() != nil {
			report.Aborted = true
			report.Steps[i].Detail = "cancelled before apply"
			return report
		}
		snap, err := d.Preflight(ctx, c)
		if err == nil && (snap.UID != c.Step.Plan.TargetUID || snap.ResourceVersion == "") {
			err = errors.New("preflight identity/version is incomplete")
		}
		if err != nil {
			report.Steps[i].Detail = "preflight refused: " + err.Error()
			report.Aborted = true
			return report
		}
		report.Steps[i].Detail = "shadow verified; guarded preflight passed"
	}
	if ctx.Err() != nil || confirmApply == nil || !confirmApply() {
		report.Aborted = true
		return report
	}
	applied := []int{}
	for i, c := range steps {
		if ctx.Err() != nil {
			report.Steps[i].Detail = "cancelled before apply"
			if len(applied) == 0 {
				report.Aborted = true
				return report
			}
			report.Failed = true
			break
		}
		snap, err := d.Preflight(ctx, c)
		if err == nil && (snap.UID != c.Step.Plan.TargetUID || snap.ResourceVersion == "") {
			err = errors.New("execution identity/version is incomplete")
		}
		if err != nil {
			report.Steps[i].Detail = "execution preflight refused: " + err.Error()
			report.Failed = true
			break
		}
		report.Steps[i].ApplyAttempted = true
		if err = d.Apply(ctx, c, snap); err != nil {
			report.Steps[i].Verification = "apply-outcome-unknown"
			report.Steps[i].Detail = "apply returned an error; outcome unconfirmed: " + err.Error()
			report.Failed = true
			break
		}
		report.Steps[i].Applied = true
		report.Mutated = true
		report.Steps[i].RollbackStatus = "available"
		applied = append(applied, i)
		outcome, err := d.Verify(ctx, c)
		report.Steps[i].Detail = outcome.Summary
		report.Steps[i].Verification = outcome.Class
		if err != nil {
			report.Steps[i].Detail = "live verification failed: " + err.Error()
		}
		report.Steps[i].Healthy = err == nil && outcome.Class == ops.RolloutHealthy
		if !report.Steps[i].Healthy {
			report.Failed = true
			break
		}
	}
	if !report.Failed || len(applied) == 0 {
		return report
	}
	if confirmRollback == nil || !confirmRollback() {
		return report
	}
	for j := len(applied) - 1; j >= 0; j-- {
		i := applied[j]
		outcome, err := d.RestoreFields(ctx, steps[i])
		if err != nil || outcome.Class != RollbackRestored {
			report.Steps[i].RollbackStatus = "refused-or-failed"
			detail := outcome.Summary
			if err != nil {
				detail = err.Error()
			}
			report.Steps[i].Detail += " | rollback refused or failed: " + detail
			continue
		}
		report.Steps[i].RolledBack = true
		report.Steps[i].Healthy = false
		report.Steps[i].RollbackStatus = "fields-restored"
		report.Steps[i].Detail += " | original changed fields restored; original incident may remain"
	}
	return report
}

// RunGraphVerified applies one complete, freshly revalidated graph certificate
// as a guarded saga. It preflights the entire set before confirmation and
// rechecks all source identities before each mutation.
func RunGraphVerified(ctx context.Context, steps []CertifiedStep, certificate GraphCertificate, readCurrent func(context.Context) (GraphSnapshot, error), d VerifiedDeps, confirmApply, confirmRollback func() bool) (report Report) {
	shadowVerified, liveVerified, externalTested := false, false, false
	report = Report{Steps: make([]StepReport, len(steps)), ShadowVerified: &shadowVerified, LiveVerified: &liveVerified, ExternalDependenciesTested: &externalTested}
	defer func() { *report.LiveVerified = liveVerified }()
	for i, step := range steps {
		report.Steps[i].Ref = step.Step.Ref
	}
	if len(steps) < 2 || d == nil || readCurrent == nil {
		report.Aborted = true
		return report
	}
	checkGraph := func() error {
		current, err := readCurrent(ctx)
		if err != nil {
			return err
		}
		return CheckGraphCertifiedSteps(steps, certificate, current)
	}
	if err := checkGraph(); err != nil {
		report.Aborted = true
		report.Steps[0].Detail = "graph certificate refused: " + err.Error()
		return report
	}
	shadowVerified = true
	report.ShadowCleanup = certificate.CleanupStatus()
	for i, step := range steps {
		snapshot, err := d.Preflight(ctx, step)
		if err == nil && (snapshot.UID != step.Step.Plan.TargetUID || snapshot.ResourceVersion == "") {
			err = errors.New("preflight identity/version is incomplete")
		}
		if err != nil {
			report.Aborted = true
			report.Steps[i].Detail = "preflight refused: " + err.Error()
			return report
		}
		report.Steps[i].Verification = "shadow-verified"
		report.Steps[i].Detail = "graph shadow verified; guarded preflight passed"
	}
	if err := checkGraph(); err != nil {
		report.Aborted = true
		report.Steps[0].Detail = "graph changed before confirmation: " + err.Error()
		return report
	}
	if ctx.Err() != nil || confirmApply == nil || !confirmApply() {
		report.Aborted = true
		return report
	}
	applied := []int{}
	for i, step := range steps {
		if ctx.Err() != nil {
			report.Steps[i].Detail = "cancelled before apply"
			if len(applied) == 0 {
				report.Aborted = true
				return report
			}
			report.Failed = true
			break
		}
		if err := checkGraph(); err != nil {
			report.Steps[i].Detail = "graph changed before apply: " + err.Error()
			report.Failed = true
			break
		}
		snapshot, err := d.Preflight(ctx, step)
		if err == nil && (snapshot.UID != step.Step.Plan.TargetUID || snapshot.ResourceVersion == "") {
			err = errors.New("execution identity/version is incomplete")
		}
		if err != nil {
			report.Steps[i].Detail = "execution preflight refused: " + err.Error()
			report.Failed = true
			break
		}
		report.Steps[i].ApplyAttempted = true
		if err = d.Apply(ctx, step, snapshot); err != nil {
			report.Steps[i].Verification = "apply-outcome-unknown"
			report.Steps[i].Detail = "apply returned an error; outcome unconfirmed: " + err.Error()
			report.Failed = true
			break
		}
		report.Steps[i].Applied = true
		report.Mutated = true
		report.Steps[i].RollbackStatus = "available"
		applied = append(applied, i)
		outcome, verifyErr := d.Verify(ctx, step)
		report.Steps[i].Detail = outcome.Summary
		report.Steps[i].Verification = outcome.Class
		if verifyErr != nil {
			report.Steps[i].Detail = "live verification failed: " + verifyErr.Error()
		}
		report.Steps[i].Healthy = verifyErr == nil && outcome.Class == ops.RolloutHealthy
		if !report.Steps[i].Healthy {
			report.Failed = true
			break
		}
	}
	if !report.Failed || len(applied) == 0 {
		if !report.Failed && len(applied) == len(steps) {
			liveVerified = true
		}
		if report.Failed && len(applied) == 0 {
			report.Aborted = true
		}
		return report
	}
	if confirmRollback == nil || !confirmRollback() {
		return report
	}
	for j := len(applied) - 1; j >= 0; j-- {
		i := applied[j]
		outcome, err := d.RestoreFields(ctx, steps[i])
		if err != nil || outcome.Class != RollbackRestored {
			report.Steps[i].RollbackStatus = "refused-or-failed"
			detail := outcome.Summary
			if err != nil {
				detail = err.Error()
			}
			report.Steps[i].Detail += " | rollback refused or failed: " + detail
			continue
		}
		report.Steps[i].RolledBack = true
		report.Steps[i].Healthy = false
		report.Steps[i].RollbackStatus = "fields-restored"
		report.Steps[i].Detail += " | original changed fields restored; original incident may remain"
	}
	return report
}
func ReportText(report Report) string {
	var out strings.Builder
	for _, step := range report.Steps {
		status := "not-applied"
		if step.ApplyAttempted {
			status = "apply-unconfirmed"
		}
		if step.Applied {
			status = "applied-unverified"
		}
		if step.Healthy {
			status = "live-verified"
		}
		if step.RolledBack {
			status = "fields-restored"
		}
		fmt.Fprintf(&out, "- %s [%s] %s\n", step.Ref, status, step.Detail)
	}
	if report.ShadowVerified != nil {
		fmt.Fprintf(&out, "Graph shadow verified: %t\n", *report.ShadowVerified)
	}
	if report.LiveVerified != nil {
		fmt.Fprintf(&out, "Graph live verified: %t\n", *report.LiveVerified)
	}
	if report.ExternalDependenciesTested != nil {
		fmt.Fprintf(&out, "External dependencies tested: %t\n", *report.ExternalDependenciesTested)
	}
	if report.ShadowCleanup != "" {
		fmt.Fprintf(&out, "Shadow cleanup: %s\n", report.ShadowCleanup)
	}
	return out.String()
}

// Declared ownership edges between selected targets require dependent verification,
// which this independently verified path does not provide. Reject cycles too.
func CheckIndependentSteps(steps []CertifiedStep) error {
	selected := map[string]bool{}
	for _, c := range steps {
		selected[c.Step.Namespace+"/"+c.Step.Ref] = true
	}
	for _, c := range steps {
		owners, _ := objectMap(c.Before.Object["metadata"])["ownerReferences"].([]any)
		for _, raw := range owners {
			owner := objectMap(raw)
			kind, _ := owner["kind"].(string)
			name, _ := owner["name"].(string)
			if selected[c.Step.Namespace+"/"+kind+"/"+name] {
				return errors.New("selected controllers have a declared ownership dependency or cycle; source review is required")
			}
		}
	}
	return nil
}
