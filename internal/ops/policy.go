package ops

import (
	"context"
	"errors"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
)

func VerifyPlacementRollout(ctx context.Context, r kube.Reader, f analyzer.Finding, p fix.Plan) (RolloutOutcome, error) {
	if p.Strategy != "restore-placement" {
		return RolloutOutcome{}, errors.New("placement plan required")
	}
	return verifyPolicyRollout(ctx, r, f, p)
}
func verifyPolicyRollout(ctx context.Context, r kube.Reader, f analyzer.Finding, p fix.Plan) (RolloutOutcome, error) {
	unknown := RolloutOutcome{Class: RolloutUnknown, Summary: "updated owned Pods do not yet prove the policy repair"}
	if p.PolicyProof == nil || p.TargetUID == "" {
		return unknown, errors.New("live policy proof is missing")
	}
	obj, err := r.GetResource(ctx, f.Namespace, strings.ToLower(f.ResourceKind)+"/"+f.ResourceName)
	if err != nil {
		return unknown, err
	}
	meta, _ := obj["metadata"].(map[string]any)
	if meta["uid"] != p.TargetUID || !appliedTemplateMatches(obj, p.PatchYAML()) {
		return unknown, errors.New("live target identity or approved template differs")
	}
	if f.ResourceKind == "CronJob" {
		return RolloutOutcome{Class: CronJobPending, Summary: "template accepted; next scheduled run remains unverified"}, nil
	}
	pods, err := r.GetPods(ctx, f.Namespace, false)
	if err != nil {
		return unknown, err
	}
	count := 0
	for _, pod := range pods.Items {
		if pod.Metadata.DeletionTimestamp != "" {
			continue
		}
		owner, err := kube.ResolveWorkloadOwner(ctx, r, pod)
		if err != nil {
			return unknown, err
		}
		if !owner.Matches(f.ResourceKind, f.ResourceName, p.TargetUID) {
			continue
		}
		count++
		if err := repairpolicy.CheckLivePod(ctx, r, p.PolicyProof, &pod, p.PatchYAML()); err != nil {
			return unknown, err
		}
	}
	if count == 0 {
		return unknown, nil
	}
	return RolloutOutcome{Class: RolloutHealthy, Summary: f.ResourceKind + "/" + f.ResourceName + " updated Pods satisfy the approved policy and are Ready"}, nil
}

func VerifySecurityRollout(ctx context.Context, r kube.Reader, f analyzer.Finding, p fix.Plan) (RolloutOutcome, error) {
	if p.Strategy != "security-strengthen" {
		return RolloutOutcome{}, errors.New("security plan required")
	}
	return verifyPolicyRollout(ctx, r, f, p)
}
