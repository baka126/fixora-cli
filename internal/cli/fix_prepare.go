package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/infer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/ops"
	"github.com/fixora/kubectl-fixora/internal/repo"
	"github.com/fixora/kubectl-fixora/internal/workloadmemory"
	"k8s.io/apimachinery/pkg/api/resource"
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
	memoryContext, memoryErr := lookupWorkloadMemory(ctx, k, finding, opts.workloadMemoryPath)
	if memoryErr == nil {
		opts.workloadMemoryFingerprint = memoryContext.fingerprint
		opts.workloadMemoryPath = memoryContext.path
		opts.workloadMemoryNote = memoryContext.note
		if memoryContext.note != "" {
			appendWorkloadMemoryEvidence(&finding, memoryContext.note)
		}
	} else if opts.verbose {
		fmt.Fprintln(stderr, "workload memory ignored because the local profile could not be read")
	}
	if opts.useAI {
		finding = enrichFindingForAI(ctx, reader, k, opts, resource, finding)
		if opts.servicePolicy != "" {
			finding, err = withServiceProposalIntent(finding, opts.servicePolicy)
			if err != nil {
				return finding, fix.Plan{}, opts, err
			}
		}
		augmentWithAI(ctx, &finding, opts, stderr)
	} else {
		// The infer package fills patch placeholders from `kubectl top` and
		// registry evidence. That evidence used to be gathered only inside
		// enrichFindingForAI, so under --no-ai the image and OOMKilled-metrics
		// inferrers always declined. Collect it here regardless of --ai.
		finding = collectDeliveryEvidence(ctx, k, opts, finding)
	}
	if opts.servicePolicy != "" && finding.ResourceKind == "Service" {
		return finding, fix.BuildPlan(finding), opts, nil
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
	if (plan.ApplyEligible || fix.SourceReviewAllowed(plan)) && !strings.HasPrefix(plan.Resource, "Deployment/") {
		preflightPlan := plan
		if fix.SourceReviewAllowed(plan) {
			preflightPlan.BlockedReasons = nil
			preflightPlan.ApplyEligible = true
			preflightPlan.CanApply = true
			preflightPlan.Safe = true
		}
		if uid, preflightErr := ops.PreflightController(ctx, reader, finding, preflightPlan); preflightErr != nil {
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

const workloadMemoryEvidenceLabel = "Previously verified local Fixora workload"

type workloadMemoryContext struct {
	fingerprint string
	path        string
	note        string
}

func lookupWorkloadMemory(ctx context.Context, k kube.Kubectl, finding analyzer.Finding, path string) (workloadMemoryContext, error) {
	clusterID, err := k.ClusterID(ctx)
	if err != nil {
		return workloadMemoryContext{}, err
	}
	imageRepository := imageRepositoryFromFinding(finding)
	fingerprint := workloadmemory.Fingerprint(workloadmemory.ProfileKey{
		ClusterID: clusterID, Namespace: finding.Namespace, ResourceKind: finding.ResourceKind,
		ResourceName: finding.ResourceName, ImageRepository: imageRepository,
	})
	if fingerprint == "" {
		return workloadMemoryContext{}, nil
	}
	if strings.TrimSpace(path) == "" {
		path, err = workloadmemory.DefaultPath(clusterID)
		if err != nil {
			return workloadMemoryContext{}, err
		}
	}
	profile, found, err := (workloadmemory.Store{Path: path}).Load(fingerprint)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return workloadMemoryContext{fingerprint: fingerprint, path: path}, nil
		}
		return workloadMemoryContext{}, err
	}
	result := workloadMemoryContext{fingerprint: fingerprint, path: path}
	if !found {
		return result, nil
	}
	session, ok := profile.LatestVerified()
	if !ok {
		return result, nil
	}
	result.note = summarizePriorWorkloadMemory(session)
	return result, nil
}

func imageRepositoryFromFinding(finding analyzer.Finding) string {
	for _, evidence := range finding.Evidence {
		if strings.HasPrefix(evidence.Label, "Container image ") && strings.TrimSpace(evidence.Value) != "" {
			reference := strings.ToLower(strings.TrimSpace(evidence.Value))
			if at := strings.IndexByte(reference, '@'); at >= 0 {
				reference = reference[:at]
			}
			lastSlash := strings.LastIndexByte(reference, '/')
			if colon := strings.LastIndexByte(reference, ':'); colon > lastSlash {
				reference = reference[:colon]
			}
			return strings.TrimSuffix(reference, "/")
		}
	}
	return ""
}

func summarizePriorWorkloadMemory(session workloadmemory.Session) string {
	parts := []string{"previously verified"}
	if session.FailureClass != "" {
		parts = append(parts, session.FailureClass)
	}
	if session.Strategy != "" {
		parts = append(parts, "via "+session.Strategy)
	}
	if session.Parity > 0 {
		parts = append(parts, fmt.Sprintf("parity %d%%", session.Parity))
	}
	if session.RepairMemoryLimitBytes > 0 {
		quantity := *resource.NewQuantity(session.RepairMemoryLimitBytes, resource.BinarySI)
		parts = append(parts, "repaired memory limit "+quantity.String())
	}
	if session.MemoryPeakBytes > 0 {
		quantity := *resource.NewQuantity(session.MemoryPeakBytes, resource.BinarySI)
		parts = append(parts, "peak "+quantity.String())
	}
	if session.MemoryHeadroomPercent > 0 {
		parts = append(parts, fmt.Sprintf("%d%% headroom", session.MemoryHeadroomPercent))
	}
	if session.TimeToReadySeconds > 0 {
		parts = append(parts, fmt.Sprintf("ready in %ds", session.TimeToReadySeconds))
	}
	if !session.At.IsZero() {
		parts = append(parts, "on "+session.At.UTC().Format("2006-01-02"))
	}
	return strings.Join(parts, "; ")
}

func appendWorkloadMemoryEvidence(finding *analyzer.Finding, note string) {
	if finding == nil || strings.TrimSpace(note) == "" {
		return
	}
	finding.Evidence = append(finding.Evidence, analyzer.Evidence{Label: workloadMemoryEvidenceLabel, Value: note})
}
