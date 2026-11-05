package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
)

// Authority comes only from a manifest explicitly supplied by the operator.
func withStartupBaseline(ctx context.Context, reader kube.Reader, path string, f analyzer.Finding, p fix.Plan) (fix.Plan, error) {
	if path == "" || reader == nil {
		return p, errors.New("startup restoration requires an explicit baseline file and cluster reader")
	}
	if f.Status != "CrashLoopBackOff" || p.Strategy != "runtime" || kube.WorkloadVersion(f.ResourceKind) == "" {
		return p, errors.New("startup baseline applies only to a crashing workload runtime plan")
	}
	if p.Resource != f.ResourceKind+"/"+f.ResourceName || p.Namespace != f.Namespace {
		return p, errors.New("startup baseline plan identity differs from finding")
	}
	for _, reason := range p.BlockedReasons {
		if strings.Contains(reason, "GitOps") {
			return p, errors.New("startup restoration requires source-level delivery for GitOps-managed workloads")
		}
	}
	a, err := repairpolicy.ReadBaseline(path)
	if err != nil {
		return p, err
	}
	s, err := repairpolicy.Collect(ctx, reader, repairpolicy.Target{Kind: f.ResourceKind, Name: f.ResourceName, Namespace: f.Namespace, UID: f.ResourceUID, PodName: f.PodName, PodUID: f.PodUID})
	if err != nil {
		return p, err
	}
	patch, proof, err := repairpolicy.Startup(s, a, "")
	if err != nil {
		return p, err
	}
	p.PatchTemplate = patch
	p.Strategy = "restore-command"
	p.Confidence = 95
	p.Safe = true
	p.Risk = "low"
	p.CanApply = !p.Recovered && !f.Recovered
	p.ApplyEligible = p.CanApply
	p.BlockedReasons = nil
	if !p.CanApply {
		p.BlockedReasons = []string{"Workload is already recovered; startup restoration requires review."}
	}
	p.TargetUID = s.Target.UID
	p.TargetPodUID = s.Target.PodUID
	p.PolicyProof = proof
	p.PolicyAuthority = a
	summary := proof.Summary()
	p.PolicySummary = &summary
	p.Patches = []fix.Patch{{Type: "strategic-merge", Target: p.Resource, Description: "Restore startup command and args from the operator-supplied matching baseline.", Preview: patch}}
	p.Guardrails = append(append([]string{}, p.Guardrails...), "operator-startup-baseline", "shadow-required")
	p.Warnings = append(append([]string{}, p.Warnings...), "Verify the operator-supplied startup baseline in shadow before production delivery.")
	return fix.WithTargetPolicy(p), nil
}
