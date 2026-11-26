package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/output"
	"github.com/fixora/kubectl-fixora/internal/repo"
	"github.com/fixora/kubectl-fixora/internal/servicefix"
)

type helmServiceOutput struct {
	Resource       string   `json:"resource" yaml:"resource"`
	Namespace      string   `json:"namespace" yaml:"namespace"`
	Diagnosis      string   `json:"diagnosis" yaml:"diagnosis"`
	Cause          string   `json:"cause,omitempty" yaml:"cause,omitempty"`
	SourceStatus   string   `json:"sourceStatus" yaml:"sourceStatus"`
	ValuesFile     string   `json:"valuesFile,omitempty" yaml:"valuesFile,omitempty"`
	ChangedKeys    []string `json:"changedKeys,omitempty" yaml:"changedKeys,omitempty"`
	Admission      string   `json:"admission,omitempty" yaml:"admission,omitempty"`
	RouteCheck     string   `json:"routeCheck" yaml:"routeCheck"`
	Reconciliation string   `json:"reconciliation" yaml:"reconciliation"`
}

func runHelmServiceSource(ctx context.Context, stdout, stderr io.Writer, o options, runtime servicefix.Runtime, finding analyzer.Finding, servicePlan servicefix.Plan, req repo.HelmSourceRequest, deps repo.HelmSourceDeps) int {
	if o.apply || strings.EqualFold(o.delivery, "cluster") {
		fmt.Fprintln(stderr, "error: Helm-managed Service routing is source-only; direct cluster delivery is blocked")
		return 2
	}
	if servicePlan.Proof == nil {
		fmt.Fprintln(stderr, "error: policy-bound Service candidate is required")
		return 1
	}
	repoPath, err := filepath.Abs(req.Context.RepoPath)
	if err == nil {
		repoPath, err = filepath.EvalSymlinks(repoPath)
	}
	if err != nil {
		fmt.Fprintln(stderr, "error: source repository unavailable")
		return 1
	}
	req.Context.RepoPath = repoPath
	patch, err := servicePlan.PatchYAML()
	if err != nil {
		fmt.Fprintln(stderr, "error: Service candidate is stale:", err)
		return 1
	}
	if req.Finding.ResourceUID == "" {
		fmt.Fprintln(stderr, "error: live Service UID is required for Helm source certification")
		return 1
	}
	req.Finding = finding
	if req.Finding.GitOps.ManagedBy == "" {
		req.Finding.GitOps.ManagedBy = "Helm"
	}
	if req.Finding.GitOps.HelmRelease == "" {
		req.Finding.GitOps.HelmRelease = req.Context.Release
	}
	req.Plan = fix.Plan{Resource: servicePlan.Resource, Namespace: servicePlan.Namespace, Strategy: "service-routing", Confidence: 100, TargetUID: finding.ResourceUID, PatchTemplate: patch, BlockedReasons: []string{fix.SourceOwnershipReviewReason}}
	helmPlan, err := repo.PrepareHelmSource(ctx, req, deps)
	if err != nil {
		fmt.Fprintln(stderr, "error: Helm Service source validation:", err)
		return 1
	}
	result := helmServiceOutput{Resource: servicePlan.Resource, Namespace: servicePlan.Namespace, Diagnosis: finding.Status, Cause: serviceCause(finding), SourceStatus: helmPlan.Status, ValuesFile: helmPlan.Path, ChangedKeys: helmPlan.ChangedKeys, Admission: helmPlan.ServerValidation, RouteCheck: "not-run", Reconciliation: "pending"}
	if helmPlan.Proof == nil {
		if o.output != "text" {
			return output.Write(stdout, o.output, result)
		}
		fmt.Fprintf(stdout, "%s (%s): %s\nCause: %s\nHelm source: %s\nNo values change certified; no files or cluster resources changed.\n", result.Resource, result.Namespace, result.Diagnosis, result.Cause, result.SourceStatus)
		return 0
	}
	wantsWrite := o.sourcePatch || canonicalPRDelivery(o) || o.commit
	readOnly := o.output != "text" || o.preview || !wantsWrite
	if readOnly {
		if o.yes {
			verification, verifyErr := verifyHelmServiceRoute(ctx, o, runtime, servicePlan)
			if verifyErr != nil {
				fmt.Fprintf(stderr, "Service route verification failed: %v\n", verifyErr)
				printServiceCleanup(stderr, verification)
				return 1
			}
			result.RouteCheck = verification.Scope
		}
		if o.output != "text" {
			return output.Write(stdout, o.output, result)
		}
		fmt.Fprintf(stdout, "%s (%s): %s\nCause: %s\nHelm source: %s\n", result.Resource, result.Namespace, result.Diagnosis, result.Cause, result.SourceStatus)
		fmt.Fprintf(stdout, "Values file: %s\nChanged keys: %s\nAdmission: %s; production reconciliation: pending.\n", helmPlan.Path, strings.Join(helmPlan.ChangedKeys, ", "), helmPlan.ServerValidation)
		if result.RouteCheck != "not-run" {
			fmt.Fprintf(stdout, "Temporary route verification: %s passed and cleaned up.\n", result.RouteCheck)
		} else {
			fmt.Fprintln(stdout, "Temporary route verification: not run; pass --yes to create and clean up the verifier resources.")
		}
		if o.preview {
			fmt.Fprintln(stdout, "Preview only; Helm values and production Service are unchanged; reconciliation is pending.")
		}
		return 0
	}
	if !o.sourcePatch && !canonicalPRDelivery(o) && !o.commit {
		fmt.Fprintln(stderr, "error: use --source-patch or --delivery=pr to write the reviewed Helm values change")
		return 2
	}
	if !o.yes {
		fmt.Fprintln(stderr, "error: use --yes after reviewing the source keys to verify the route and write Helm values")
		return 2
	}
	preflightCtx, preflightCancel := serviceOperationContext(ctx, o)
	cleanErr := requireCleanServiceRepo(preflightCtx, req.Context.RepoPath)
	preflightCancel()
	if cleanErr != nil {
		fmt.Fprintln(stderr, "error:", cleanErr)
		return 1
	}
	verification, err := verifyHelmServiceRoute(ctx, o, runtime, servicePlan)
	if err != nil {
		fmt.Fprintf(stderr, "Service route verification failed: %v\n", err)
		printServiceCleanup(stderr, verification)
		return 1
	}
	result.RouteCheck = verification.Scope
	if err := servicePlan.Proof.CheckFresh(ctx, runtime); err != nil {
		fmt.Fprintln(stderr, "error: Service changed after route verification:", err)
		return 1
	}
	if err := helmPlan.Proof.CheckFresh(ctx, deps); err != nil {
		fmt.Fprintln(stderr, "error: Helm source changed after route verification:", err)
		return 1
	}
	written, err := repo.WriteValidatedHelmSource(ctx, helmPlan, deps, true)
	if err != nil {
		if written.SourceStatus == "source-written" {
			fmt.Fprintf(stderr, "Helm values were written to %s; follow-up validation failed and no PR was attempted. Reconciliation remains pending.\n", written.Path)
		}
		fmt.Fprintln(stderr, "error: Helm values write:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Wrote certified values: %s; temporary route check %s passed and cleaned up. Production Service is unchanged; reconciliation is pending.\n", written.Path, verification.Scope)
	if !o.commit && !canonicalPRDelivery(o) {
		return 0
	}
	if err := helmPlan.Proof.CheckWritten(ctx, deps); err != nil {
		fmt.Fprintln(stderr, "error: Helm values changed before commit:", err)
		return 1
	}
	branch := o.branch
	if branch == "" {
		branch = "fixora/helm-" + strings.ToLower(finding.ResourceName)
	}
	title := firstArg([]string{o.prTitle, "fixora: validated Helm Service routing for " + servicePlan.Resource})
	if err := repo.PrepareBranchFiles(ctx, req.Context.RepoPath, branch, true, title, []string{filepath.Clean(written.Path)}); err != nil {
		fmt.Fprintln(stderr, "error: values written; Helm Git workflow failed:", err)
		return 1
	}
	if !canonicalPRDelivery(o) {
		fmt.Fprintf(stdout, "Committed source on %s; production reconciliation is pending.\n", branch)
		return 0
	}
	body := fmt.Sprintf("Review the policy-approved Helm Service routing values change for %s in %s.\n\nChanged keys: %s\nTemporary route verification: %s\nProduction Service change and reconciliation: pending.\n", servicePlan.Resource, servicePlan.Namespace, strings.Join(helmPlan.ChangedKeys, ", "), verification.Scope)
	pr, err := repo.OpenPullRequest(ctx, req.Context.RepoPath, branch, o.prBase, title, body, true)
	if err != nil {
		fmt.Fprintln(stderr, "error: source committed; PR delivery incomplete:", err)
		return 1
	}
	if pr.Opened {
		fmt.Fprintf(stdout, "Review request opened: %s\n", pr.URL)
	} else {
		fmt.Fprintf(stdout, "Branch prepared: %s; no review request opened.\n", pr.Branch)
	}
	return 0
}

func verifyHelmServiceRoute(ctx context.Context, o options, runtime servicefix.Runtime, plan servicefix.Plan) (servicefix.Verification, error) {
	if runtime == nil {
		return servicefix.Verification{}, fmt.Errorf("Service runtime verifier is unavailable")
	}
	duration := 3 * time.Minute
	if o.visited["shadow-timeout"] {
		if o.shadowTimeout <= 0 {
			return servicefix.Verification{}, fmt.Errorf("Service verification timeout must be positive")
		}
		duration = o.shadowTimeout
	}
	verifyCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	return servicefix.Verify(verifyCtx, runtime, plan)
}
