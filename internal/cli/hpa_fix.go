package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/hpafix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/output"
	"github.com/fixora/kubectl-fixora/internal/termui"
)

type hpaFixReport struct {
	Resource         string                 `json:"resource"`
	Namespace        string                 `json:"namespace"`
	Status           string                 `json:"status"`
	Cause            string                 `json:"cause"`
	AI               string                 `json:"aiAssessment,omitempty"`
	ProposedRequests []hpafix.RequestChange `json:"proposedRequests,omitempty"`
	Verification     string                 `json:"verification"`
	Next             string                 `json:"next"`
	Evidence         []analyzer.Evidence    `json:"evidence,omitempty"`
}

func validateHPAOptions(o options, f analyzer.Finding) error {
	if !strings.EqualFold(f.ResourceKind, "HorizontalPodAutoscaler") {
		return fmt.Errorf("--hpa-policy requires an exact HorizontalPodAutoscaler target")
	}
	if o.servicePolicy != "" || o.pdbPolicy != "" || o.startupBaseline != "" || o.repairBaseline != "" || o.repairPolicy != "" || o.editPatch || o.sourcePatch || hasHelmSourceContext(o) {
		return fmt.Errorf("HPA repair policy cannot be combined with other repair policies or source writers")
	}
	if o.delivery != "" && o.delivery != "patch" && o.delivery != "cluster" && o.delivery != "pr" {
		return fmt.Errorf("HPA delivery must be patch, cluster or pr")
	}
	return nil
}

func runHPAFix(ctx context.Context, out, errout io.Writer, o options, f analyzer.Finding) int {
	if err := validateHPAOptions(o, f); err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	if o.hpaPolicy == "" {
		return runHPAFixRuntime(ctx, out, errout, o, nil, f)
	}
	client, err := kube.NewRequiredTypedClient(o.context, "policy-bound HPA repairs")
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	r, err := hpafix.NewClient(client)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	return runHPAFixRuntime(ctx, out, errout, o, r, f)
}

func runHPAFixRuntime(parent context.Context, out, errout io.Writer, o options, r hpafix.Runtime, f analyzer.Finding) int {
	if err := validateHPAOptions(o, f); err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	report := hpaFixReport{Resource: "HorizontalPodAutoscaler/" + f.ResourceName, Namespace: f.Namespace, Status: f.Status, Cause: briefHPA(f.Summary), Verification: "not run", Evidence: nil}
	if f.AI != nil && !f.AI.Unstructured {
		report.AI = briefHPA(firstNonEmpty(f.AI.RootCause, f.AI.Summary))
	}
	if o.proof || o.verbose {
		report.Evidence = append([]analyzer.Evidence(nil), f.Evidence...)
	}
	if o.hpaPolicy == "" {
		report.Next = "Provide --hpa-policy <local-file> with exact approved CPU or memory requests for named containers."
		return writeHPAReport(out, o, report)
	}
	if r == nil {
		fmt.Fprintln(errout, "HPA repair requires typed Kubernetes access")
		return 1
	}
	a, err := hpafix.LoadPolicy(o.hpaPolicy)
	if err != nil {
		fmt.Fprintln(errout, "HPA policy:", err)
		return 1
	}
	ctx, cancel := hpafix.OperationContext(parent)
	defer cancel()
	p, err := hpafix.Prepare(ctx, r, a, f)
	if err != nil {
		fmt.Fprintf(errout, "HPA repair requires review: %v\nNo changes made.\n", err)
		return 1
	}
	report.ProposedRequests = p.RequestChanges()
	mode := strings.ToLower(strings.TrimSpace(o.delivery))
	if mode == "" {
		mode = "patch"
	}
	if o.apply && mode == "patch" {
		mode = "cluster"
	}
	if sourceManaged(f) || hasHelmSourceContext(o) || mode == "pr" {
		report.Verification = "review only"
		report.Next = "Update the owning manifest, Helm values, or Kustomize overlay with these approved requests, then reconcile and verify the rollout."
		if mode == "cluster" {
			report.Next = "Source-managed workload; direct delivery is blocked. Update its source of truth and reconcile."
		}
		return writeHPAReport(out, o, report)
	}
	if o.preview || o.output != "text" || mode != "cluster" {
		report.Verification = "review only; no cluster or source writes"
		report.Next = "Review the approved request values; use --delivery=cluster --yes to run preflight and apply to an unmanaged target."
		return writeHPAReport(out, o, report)
	}
	if !o.yes && !termui.Confirm("Verify and apply only the HPA-policy request fields?", inputFor(o), out) {
		report.Verification = "cancelled; no changes made"
		report.Next = "Re-run with --delivery=cluster --yes after reviewing the policy."
		return writeHPAReport(out, o, report)
	}
	receipt, err := hpafix.Verify(ctx, r, p)
	if err != nil {
		fmt.Fprintf(errout, "HPA preflight failed: %v\nNo changes made.\n", err)
		return 1
	}
	report.Verification = "preflight passed"
	applied, outcome, err := hpafix.Apply(ctx, r, p, receipt)
	if err != nil {
		report.Verification = outcome.Message
		report.Next = "Inspect the live target before retrying; Fixora does not retry or undo an uncertain write."
		writeHPAReport(out, o, report)
		fmt.Fprintln(errout, "HPA delivery:", err)
		return 1
	}
	observed, observeErr := hpafix.Observe(ctx, r, applied)
	report.Verification = fmt.Sprintf("workload rollout %s; HPA metrics %s", hpaHealthLabel(observed.WorkloadConverged), hpaHealthLabel(observed.HPAHealthy))
	if observeErr != nil || observed.Partial {
		report.Next = "Review workload readiness and HPA metrics; no automatic rollback was performed."
		writeHPAReport(out, o, report)
		if !o.yes && termui.ConfirmRollback("remove only the request fields Fixora added to this workload", inputFor(o), errout) {
			rollbackCtx, rollbackCancel := context.WithTimeout(context.WithoutCancel(parent), 30*time.Second)
			rolled, rollbackErr := hpafix.Rollback(rollbackCtx, r, applied)
			rollbackCancel()
			if rollbackErr != nil {
				fmt.Fprintln(errout, "HPA rollback:", rollbackErr)
			} else {
				fmt.Fprintln(out, rolled.Message)
			}
		}
		if observeErr != nil {
			fmt.Fprintln(errout, "HPA observation:", observeErr)
		}
		return 1
	}
	report.Next = "Monitor the workload and HPA under normal traffic."
	return writeHPAReport(out, o, report)
}

func writeHPAReport(out io.Writer, o options, report hpaFixReport) int {
	if o.output != "text" {
		return output.Write(out, o.output, report)
	}
	fmt.Fprintf(out, "%s (%s): %s\nCause: %s\n", report.Resource, report.Namespace, report.Status, report.Cause)
	if report.AI != "" {
		fmt.Fprintf(out, "AI assessment: %s\n", report.AI)
	}
	if len(report.ProposedRequests) > 0 {
		fmt.Fprintln(out, "Approved request changes:")
		for _, change := range report.ProposedRequests {
			fmt.Fprintf(out, "  %s: %s=%s\n", change.Container, change.Resource, change.Value)
		}
	}
	if o.proof || o.verbose {
		for _, e := range report.Evidence {
			fmt.Fprintf(out, "  %s: %s\n", e.Label, e.Value)
		}
	}
	fmt.Fprintf(out, "Verification: %s\nNext: %s\n", report.Verification, report.Next)
	if report.Verification == "review only; no cluster or source writes" || report.Verification == "not run" {
		fmt.Fprintln(out, "No changes made.")
	}
	return 0
}

func hpaHealthLabel(ok bool) string {
	if ok {
		return "healthy"
	}
	return "not verified"
}
func briefHPA(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 240 {
		return s[:237] + "..."
	}
	return s
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
