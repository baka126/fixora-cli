package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/coordinate"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/output"
	"github.com/fixora/kubectl-fixora/internal/redact"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"github.com/fixora/kubectl-fixora/internal/termui"
)

func buildPreparedCoordinateSteps(ctx context.Context, r kube.Reader, k kube.Kubectl, opts options, refs []string, stderr io.Writer) ([]coordinate.Step, error) {
	steps := []coordinate.Step{}
	seen := map[string]bool{}
	baselineTargets := map[string]string{}
	used := map[string]bool{}
	for _, path := range []string{opts.startupBaseline, opts.repairBaseline} {
		if path == "" {
			continue
		}
		a, err := repairpolicy.ReadBaseline(path)
		if err != nil {
			return nil, err
		}
		target := a.Target()
		if opts.namespace != "" && target.Namespace != opts.namespace {
			return nil, errors.New("baseline namespace differs from coordinated namespace")
		}
		baselineTargets[path] = target.Kind + "/" + target.Name
	}
	for _, ref := range refs {
		perStep := opts
		canonical := canonicalCoordinateRef(ref)
		if opts.startupBaseline != "" {
			if baselineTargets[opts.startupBaseline] == canonical {
				used[opts.startupBaseline] = true
			} else {
				perStep.startupBaseline = ""
			}
		}
		if opts.repairBaseline != "" {
			if baselineTargets[opts.repairBaseline] == canonical {
				used[opts.repairBaseline] = true
			} else {
				perStep.repairBaseline = ""
				perStep.repairPolicy = ""
			}
		}

		f, p, _, err := prepareFixPlan(ctx, r, k, perStep, ref, stderr)
		if err != nil {
			return nil, fmt.Errorf("prepare %s: %w", ref, err)
		}
		canonical = f.ResourceKind + "/" + f.ResourceName
		key := f.Namespace + "/" + canonical
		if seen[key] {
			return nil, errors.New("duplicate/conflicting coordinated target")
		}
		seen[key] = true
		steps = append(steps, coordinate.Step{Ref: canonical, Kind: f.ResourceKind, Name: f.ResourceName, Namespace: f.Namespace, Finding: f, Plan: p})
	}
	for path := range baselineTargets {
		if !used[path] {
			return nil, errors.New("baseline does not match any selected coordinated target")
		}
	}
	return steps, nil
}

type coordinateShadowVerifier func(context.Context, *kube.TypedClient, shadow.Request) (shadow.Result, error)

func certifyCoordinateSteps(ctx context.Context, r kube.Reader, client *kube.TypedClient, k kube.Kubectl, opts options, steps []coordinate.Step) ([]coordinate.CertifiedStep, error) {
	return certifyCoordinateWith(ctx, r, client, opts, steps, shadow.Run)
}
func certifyCoordinateWith(ctx context.Context, r kube.Reader, client *kube.TypedClient, opts options, steps []coordinate.Step, verify coordinateShadowVerifier) ([]coordinate.CertifiedStep, error) {
	if len(steps) < 2 {
		return nil, errors.New("coordinated certification requires at least two eligible independent workloads")
	}
	certified := make([]coordinate.CertifiedStep, 0, len(steps))
	seen := map[string]bool{}
	for _, step := range steps {
		if step.Kind != "Deployment" && step.Kind != "StatefulSet" && step.Kind != "DaemonSet" {
			return nil, fmt.Errorf("%s requires source review; coordinated delivery supports independent controllers", step.Ref)
		}
		if !step.Plan.ApplyEligible || sourceManaged(step.Finding) {
			return nil, fmt.Errorf("%s is not eligible for direct coordinated delivery", step.Ref)
		}
		key := step.Namespace + "/" + step.Ref
		if seen[key] {
			return nil, errors.New("duplicate/conflicting coordinated target")
		}
		seen[key] = true
		target := repairpolicy.Target{Kind: step.Kind, Name: step.Name, Namespace: step.Namespace, UID: step.Plan.TargetUID, PodName: step.Finding.PodName, PodUID: step.Finding.PodUID}
		if target.UID == "" {
			target.UID = step.Finding.ResourceUID
		}
		before, err := repairpolicy.Collect(ctx, r, target)
		if err != nil {
			return nil, err
		}
		proof := step.Plan.PolicyProof
		if proof == nil {
			if repairpolicy.Dedicated(step.Plan.Strategy) {
				return nil, errors.New("new repair policy requires its original operator-bound proof")
			}
			proof, err = repairpolicy.BindCommon(before, step.Plan.PatchYAML(), step.Plan.Strategy, shadow.ValidateCandidatePatch)
			if err != nil {
				return nil, err
			}
		}
		if err = proof.Check(before, step.Plan.PatchYAML()); err != nil {
			return nil, err
		}
		inverse, err := coordinate.BuildInversePatch(before.Object, step.Plan.PatchYAML())
		if err != nil {
			return nil, err
		}
		step.Plan.PolicyProof = proof
		step.Plan.TargetUID = before.Target.UID
		step.Plan.TargetPodUID = before.Target.PodUID
		certified = append(certified, coordinate.CertifiedStep{Step: step, Proof: proof, InversePatch: inverse, Before: before})
	}
	if err := coordinate.CheckIndependentSteps(certified); err != nil {
		return nil, err
	}
	for i := range certified {
		c := &certified[i]
		result, err := verify(ctx, client, shadow.Request{Namespace: c.Step.Namespace, Resource: c.Step.Ref, Patch: c.Step.Plan.PatchYAML(), Finding: c.Step.Finding, Plan: c.Step.Plan, Timeout: opts.shadowTimeout, Egress: opts.shadowEgress, Retries: 0, Keep: false, Delivery: shadow.DeliveryPatch, Redact: opts.redact || opts.paranoid})
		if err != nil {
			return nil, fmt.Errorf("shadow %s: %w", c.Step.Ref, err)
		}
		if !result.Verified {
			return nil, fmt.Errorf("shadow %s did not verify; no production changes made", c.Step.Ref)
		}
		c.Receipt = result.PolicyReceipt
		if err = c.Check(); err != nil {
			return nil, fmt.Errorf("certificate %s: %w", c.Step.Ref, err)
		}
	}
	return certified, nil
}
func runPreparedCoordinateSteps(ctx context.Context, stdout, stderr io.Writer, opts options, r kube.Reader, k kube.Kubectl, steps []coordinate.Step) int {
	if opts.preview {
		if opts.output != "" && opts.output != "text" {
			return output.Write(stdout, opts.output, steps)
		}
		writeCoordinateProposal(stdout, opts, steps, false)
		fmt.Fprintln(stdout, "Preview only; candidates have not been shadow verified or applied.")
		return 0
	}
	typed, err := kube.NewRequiredTypedClient(opts.context, "coordinated shadow verification")
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	typed.LogTail = opts.logTail
	typed.LogLimitBytes = opts.maxLogBytes
	fmt.Fprintf(stderr, "Verifying %d workload repairs before production apply...\n", len(steps))
	certified, err := certifyCoordinateSteps(ctx, r, typed, k, opts, steps)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	apply, rollback := coordinateConfirmers(opts.yes, len(certified), inputFor(opts), stderr)
	confirm := func() bool {
		writeCertifiedCoordinateProposal(stderr, opts, certified)
		return apply()
	}
	report := coordinate.RunVerified(ctx, certified, coordinateVerifiedDeps{k: k, timeout: opts.rolloutTimeout}, confirm, rollback)
	if opts.output != "" && opts.output != "text" {
		if code := output.Write(stdout, opts.output, report); code != 0 {
			return code
		}
	} else {
		fmt.Fprint(stdout, coordinate.ReportText(report))
	}
	if report.Aborted {
		fmt.Fprintln(stderr, "coordinated apply aborted; no production changes made")
		return 2
	}
	if report.Failed {
		fmt.Fprintln(stderr, "coordinated apply stopped; inspect live verification and rollback status for each step")
		return 1
	}
	fmt.Fprintln(stdout, "All coordinated repairs are live-verified.")
	return 0
}

// Guarded files are derived from the certified bytes, never a mutable user file.
func guardedPolicyFile(patch, uid, version string) (string, func(), error) {
	file, err := os.CreateTemp("", "fixora-certified-*.yaml")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.Remove(file.Name()) }
	if _, err = file.WriteString(patch); err != nil {
		file.Close()
		cleanup()
		return "", nil, err
	}
	if err = file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	guarded, removeGuard, err := kube.GuardedApplyFile(file.Name(), uid, version)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return guarded, func() { removeGuard(); cleanup() }, nil
}
func canonicalCoordinateRef(ref string) string {
	kind, name, ok := strings.Cut(ref, "/")
	if !ok {
		return ref
	}
	switch strings.ToLower(kind) {
	case "deploy", "deployments", "deployment":
		kind = "Deployment"
	case "sts", "statefulsets", "statefulset":
		kind = "StatefulSet"
	case "ds", "daemonsets", "daemonset":
		kind = "DaemonSet"
	}
	return kind + "/" + name
}

// Show the certified bytes at the confirmation boundary, after all preflights.

func writeCertifiedCoordinateProposal(out io.Writer, opts options, steps []coordinate.CertifiedStep) {
	selected := make([]coordinate.Step, len(steps))
	for i, c := range steps {
		selected[i] = c.Step
	}
	writeCoordinateProposal(out, opts, selected, true)
}
func writeCoordinateProposal(out io.Writer, opts options, steps []coordinate.Step, verified bool) {
	label := "candidate"
	if verified {
		label = "shadow verified"
	}
	fmt.Fprintln(out, "Repair set (in apply order)")
	for _, step := range steps {
		fmt.Fprintf(out, "%s (%s): %s; %s\n", step.Ref, step.Namespace, step.Plan.Strategy, label)
		patch := step.Plan.PatchYAML()
		if opts.redact || opts.paranoid {
			patch = redact.KubernetesText(patch)
		}
		fmt.Fprint(out, termui.DisplayDiff(shadow.PatchDiff(step.Ref, patch), opts.noColor))
	}
}
