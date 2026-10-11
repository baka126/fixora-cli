package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/infer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/ops"
	"github.com/fixora/kubectl-fixora/internal/repo"
)

func prepareFixPlan(ctx context.Context, reader kube.Reader, k kube.Kubectl, opts options, resource string, stderr io.Writer) (analyzer.Finding, fix.Plan, options, error) {
	a := analyzer.New(reader, analyzer.Options{Namespace: opts.namespace, AllNS: opts.allNS, IncludeLogs: opts.includeLogs, Redact: opts.redact || opts.paranoid, Filters: analyzerFiltersForCommand("fix", []string{resource}, opts), LabelSelector: opts.labelSelector, CheckSecretKeys: opts.checkSecretKeys, CheckCertExpiry: opts.checkCertExpiry})
	finding, err := a.AnalyzeResource(ctx, resource)
	if err != nil {
		return finding, fix.Plan{}, opts, err
	}
	finding = preferSmartFinding(ctx, a, finding)
	return prepareAnalyzedFixPlan(ctx, reader, k, opts, resource, finding, stderr)
}
func prepareAnalyzedFixPlan(ctx context.Context, reader kube.Reader, k kube.Kubectl, opts options, resource string, finding analyzer.Finding, stderr io.Writer) (analyzer.Finding, fix.Plan, options, error) {
	var err error
	if opts.useAI {
		finding = enrichFindingForAI(ctx, reader, k, opts, resource, finding)
		augmentWithAI(ctx, &finding, opts, stderr)
	} else {
		// The infer package fills patch placeholders from `kubectl top` and
		// registry evidence. That evidence used to be gathered only inside
		// enrichFindingForAI, so under --no-ai the image and OOMKilled-metrics
		// inferrers always declined. Collect it here regardless of --ai.
		finding = collectDeliveryEvidence(ctx, k, opts, finding)
	}
	plan := fix.BuildPlan(finding)
	plan = fix.Concretize(plan, concreteOptions(opts))
	inferred := false
	if !plan.ApplyEligible {
		plan, inferred = infer.Concrete(ctx, reader, finding, plan)
	}
	if opts.startupBaseline != "" {
		plan, err = withStartupBaseline(ctx, reader, opts.startupBaseline, finding, plan)
		if err != nil {
			return finding, plan, opts, fmt.Errorf("startup baseline: %w", err)
		}
		opts.shadowVerify = true
		opts.shadowRetries = 0 // AI must not revise an explicit operator command.
	}
	if opts.repairBaseline != "" {
		plan, err = withRepairBaseline(ctx, reader, opts, finding, plan)
		if err != nil {
			return finding, plan, opts, fmt.Errorf("repair baseline: %w", err)
		}
		if plan.ApplyEligible {
			opts.shadowVerify = true
			opts.shadowRetries = 0
		}
	}
	if opts.useAI {
		plan = selectAIRepair(ctx, reader, plan, finding, stderr, opts.verbose)
		if slices.Contains(plan.Guardrails, "ai-patch-safety-validated") {
			opts.shadowVerify = true
		}
	}
	if plan.ApplyEligible && !strings.HasPrefix(plan.Resource, "Deployment/") {
		if uid, preflightErr := ops.PreflightController(ctx, reader, finding, plan); preflightErr != nil {
			plan = fix.WithControllerReview(plan, "Live target requires source review: "+preflightErr.Error())
		} else {
			plan.TargetUID = uid
		}
	}
	if slices.Contains(plan.Guardrails, "controller-preflight-required") && plan.ApplyEligible {
		opts.shadowVerify = true
	}
	if inferred {
		opts.shadowVerify = true
	}
	if opts.repoPath != "" {
		mode, repoErr := repo.Plan(ctx, opts.repoPath, finding, plan)
		if repoErr == nil {
			plan.Steps = append(plan.Steps, "Repo mode detected "+mode.Type+" source at "+mode.Path+". "+mode.ValidationNote)
		} else {
			plan.BlockedReasons = append(plan.BlockedReasons, repoErr.Error())
		}
	}
	if opts.autoFix {
		plan.AutoFixRequested = true
		if opts.repoPath != "" {
			opts.sourcePatch = true
		}
	}

	return finding, plan, opts, nil
}
