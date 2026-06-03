package shadow

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func Run(ctx context.Context, c *kube.TypedClient, req Request) (Result, error) {
	if c == nil || c.Clientset == nil {
		return Result{}, fmt.Errorf("shadow verification requires typed Kubernetes client access")
	}
	if req.Namespace == "" {
		req.Namespace = req.Finding.Namespace
	}
	if req.Resource == "" {
		req.Resource = req.Finding.ResourceKind + "/" + req.Finding.ResourceName
	}
	if req.Timeout <= 0 {
		req.Timeout = 5 * time.Minute
	}
	if req.Retries < 0 {
		req.Retries = 0
	}
	if req.Egress == "" {
		req.Egress = "allow"
	}
	result := Result{
		Resource:      req.Resource,
		Namespace:     req.Namespace,
		Delivery:      string(req.Delivery),
		VerifiedPatch: req.Patch,
	}
	var lastPlan clonePlan
	for attempt := 1; attempt <= req.Retries+1; attempt++ {
		session := uuid.NewString()
		plan, err := buildClonePlan(ctx, c, req, session)
		if err != nil {
			return result, err
		}
		lastPlan = plan
		result.Warnings = appendUnique(result.Warnings, plan.Warnings...)
		result.CloneName = plan.Clone.Name
		result.NetworkPolicyName = plan.Policy.Name
		if _, err := c.CreateNetworkPolicy(ctx, plan.Policy); err != nil {
			return result, fmt.Errorf("create shadow NetworkPolicy: %w", err)
		}
		if _, err := c.CreatePod(ctx, plan.Clone); err != nil {
			_ = c.DeleteNetworkPolicy(context.Background(), plan.Policy.Namespace, plan.Policy.Name)
			return result, fmt.Errorf("create shadow pod: %w", err)
		}
		verification := verifyClone(ctx, c, plan.Clone.Namespace, plan.Clone.Name, req.Timeout, attempt)
		result.Attempts = append(result.Attempts, verification)
		result.Parity = parityScore(plan.Original, plan.UnpatchedClone)
		result.Verified = verification.Ready
		if result.Verified {
			break
		}
		if attempt <= req.Retries {
			revised, ok := revisePatch(ctx, req.AI, req.Patch, verification)
			result.Attempts[len(result.Attempts)-1].Revised = ok
			if ok {
				req.Patch = revised
				result.VerifiedPatch = revised
			} else {
				result.Warnings = appendUnique(result.Warnings, "shadow retry requested but no deterministic safe revision was available")
				break
			}
			if !req.Keep {
				cleanup(ctx, c, plan, &result)
			}
		}
	}
	if !req.Keep && lastPlan.Clone != nil {
		cleanup(ctx, c, lastPlan, &result)
	}
	return result, nil
}

func cleanup(ctx context.Context, c *kube.TypedClient, plan clonePlan, result *Result) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if ctx.Err() == nil {
		cleanupCtx = ctx
	}
	if plan.Clone != nil {
		if err := c.DeletePod(cleanupCtx, plan.Clone.Namespace, plan.Clone.Name); err == nil {
			result.Cleanup = appendUnique(result.Cleanup, "deleted pod/"+plan.Clone.Name)
		}
	}
	if plan.Policy != nil {
		if err := c.DeleteNetworkPolicy(cleanupCtx, plan.Policy.Namespace, plan.Policy.Name); err == nil {
			result.Cleanup = appendUnique(result.Cleanup, "deleted networkpolicy/"+plan.Policy.Name)
		}
	}
}

func appendUnique(values []string, next ...string) []string {
	for _, value := range next {
		seen := false
		for _, existing := range values {
			if existing == value {
				seen = true
				break
			}
		}
		if !seen {
			values = append(values, value)
		}
	}
	return values
}
