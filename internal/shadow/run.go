package shadow

import (
	"context"
	"fmt"
	"os"
	"strings"
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
		req.Timeout = 10 * time.Minute
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
		verification := verifyClone(ctx, c, plan.Clone.Namespace, plan.Clone.Name, req.Timeout, attempt, resourceAllowsCompletion(req.Resource))
		result.Attempts = append(result.Attempts, verification)
		result.Parity = parityScore(plan.Original, plan.UnpatchedClone)
		result.Verified = verification.Ready
		if result.Verified {
			result.Caveats = appendUnique(result.Caveats, partialPassCaveats(plan.Original, plan.NamespaceMetadata)...)
			break
		}
		if attempt <= req.Retries {
			revised, ok, reviseErr := revisePatch(ctx, req.AI, req.Patch, req.Plan.Strategy, verification, req.Redact)
			result.Attempts[len(result.Attempts)-1].Revised = ok
			if ok {
				req.Patch = revised
				result.VerifiedPatch = revised
			} else {
				if reviseErr != nil {
					result.Warnings = appendUnique(result.Warnings, reviseErr.Error())
				}
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
	if !result.Verified {
		diagnosis := DiagnoseFailureForPatch(result, req.Finding, req.Plan, result.VerifiedPatch)
		result.FailureClass = diagnosis.Class
		result.FailureSummary = diagnosis.Summary
	}
	if !req.Keep && len(result.cleanupFailures()) > 0 {
		return result, fmt.Errorf("shadow cleanup failed: %s", result.cleanupFailures()[0])
	}
	return result, nil
}

func resourceAllowsCompletion(resource string) bool {
	kind, _ := splitResource(resource)
	switch strings.ToLower(kind) {
	case "job", "jobs", "cronjob", "cronjobs", "cj":
		return true
	default:
		return false
	}
}

func cleanup(ctx context.Context, c *kube.TypedClient, plan clonePlan, result *Result) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if plan.Clone != nil {
		if !isShadowObject(plan.Clone.Labels) {
			result.Warnings = appendUnique(result.Warnings, fmt.Sprintf("cleanup skipped for pod/%s in namespace %s: missing fixora shadow labels", plan.Clone.Name, plan.Clone.Namespace))
		} else if err := c.DeletePod(cleanupCtx, plan.Clone.Namespace, plan.Clone.Name); err == nil {
			result.Cleanup = appendUnique(result.Cleanup, "deleted pod/"+plan.Clone.Name)
		} else {
			msg := fmt.Sprintf("cleanup failed for pod/%s in namespace %s: %v", plan.Clone.Name, plan.Clone.Namespace, err)
			result.Warnings = appendUnique(result.Warnings, msg)
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", msg)
		}
	}
	if plan.Policy != nil {
		if !isShadowObject(plan.Policy.Labels) {
			result.Warnings = appendUnique(result.Warnings, fmt.Sprintf("cleanup skipped for networkpolicy/%s in namespace %s: missing fixora shadow labels", plan.Policy.Name, plan.Policy.Namespace))
		} else if err := c.DeleteNetworkPolicy(cleanupCtx, plan.Policy.Namespace, plan.Policy.Name); err == nil {
			result.Cleanup = appendUnique(result.Cleanup, "deleted networkpolicy/"+plan.Policy.Name)
		} else {
			msg := fmt.Sprintf("cleanup failed for networkpolicy/%s in namespace %s: %v", plan.Policy.Name, plan.Policy.Namespace, err)
			result.Warnings = appendUnique(result.Warnings, msg)
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", msg)
		}
	}
}

func (r Result) cleanupFailures() []string {
	var failures []string
	for _, warning := range r.Warnings {
		if strings.HasPrefix(warning, "cleanup failed") {
			failures = append(failures, warning)
		}
	}
	return failures
}

func isShadowObject(labels map[string]string) bool {
	return labels[labelSandbox] == "true" && strings.TrimSpace(labels[labelSession]) != ""
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
