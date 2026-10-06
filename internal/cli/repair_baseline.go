package cli

import (
	"context"
	"errors"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/image"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
)

func withRepairBaseline(ctx context.Context, r kube.Reader, opts options, f analyzer.Finding, p fix.Plan) (fix.Plan, error) {
	if opts.repairBaseline == "" {
		return p, errors.New("repair baseline is required")
	}
	if opts.startupBaseline != "" {
		return p, errors.New("use only one baseline policy per repair")
	}
	if opts.repairPolicy != "" && opts.repairPolicy != "placement" && opts.repairPolicy != "security" {
		return p, errors.New("repair-policy must be placement or security")
	}
	a, err := repairpolicy.ReadBaseline(opts.repairBaseline)
	if err != nil {
		return p, err
	}
	s, err := repairpolicy.Collect(ctx, r, repairpolicy.Target{Kind: f.ResourceKind, Name: f.ResourceName, Namespace: f.Namespace, UID: f.ResourceUID, PodName: f.PodName, PodUID: f.PodUID})
	if err != nil {
		return p, err
	}
	selection := opts.repairPolicy
	if selection == "" {
		placement, pe := repairpolicy.Candidate(s, a, "placement")
		security, se := repairpolicy.Candidate(s, a, "security")
		if pe == nil && se == nil {
			return p, errors.New("baseline changes placement and security; select --repair-policy")
		}
		if pe == nil && placement != "" {
			selection = "placement"
		} else if se == nil && security != "" {
			selection = "security"
		} else {
			return p, errors.New("baseline has no supported changed policy fields")
		}
	}
	patch, err := repairpolicy.Candidate(s, a, selection)
	if err != nil {
		return p, err
	}
	p.PolicyAuthority = a
	p.PolicySelection = selection
	p.PatchTemplate = patch
	p.TargetUID = s.Target.UID
	p.TargetPodUID = s.Target.PodUID
	p.Patches = []fix.Patch{{Type: "strategic-merge", Target: p.Resource, Description: "Restore explicitly approved " + selection + " fields.", Preview: patch}}
	p.Strategy = "restore-placement"
	if selection == "placement" {
		s.Nodes, err = r.GetNodes(ctx)
		if err == nil {
			p.PolicyProof, err = repairpolicy.Placement(ctx, s, a, patch, image.Inspect)
		}
		if err == nil {
			err = repairpolicy.CheckPlacementCapacity(ctx, r, p.PolicyProof)
		}
	} else {
		p.Strategy = "security-strengthen"
		s, err = repairpolicy.CollectSecurity(ctx, r, s)
		if err == nil {
			p.PolicyProof, err = repairpolicy.Security(s, a, patch)
		}
	}
	if err != nil || f.Recovered || sourceManaged(f) {
		reason := "Policy candidate requires review"
		if err != nil {
			reason += ": " + err.Error()
		}
		if f.Recovered {
			reason += ": workload is recovered"
		}
		if sourceManaged(f) {
			reason += ": edit the managed source"
		}
		p.PolicyProof = nil
		return fix.WithControllerReview(p, reason), nil
	}
	p.CanApply = true
	p.ApplyEligible = true
	p.Safe = true
	p.Risk = "low"
	p.Confidence = 95
	p.BlockedReasons = nil
	p.Guardrails = appendUniqueString(p.Guardrails, "shadow-required")
	summary := p.PolicyProof.Summary()
	p.PolicySummary = &summary
	return fix.WithTargetPolicy(p), nil
}
