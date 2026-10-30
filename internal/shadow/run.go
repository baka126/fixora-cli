package shadow

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
	if err := requirePolicyForFields(req); err != nil {
		return Result{}, err
	}
	if repairpolicy.Dedicated(req.Plan.Strategy) {
		if req.Plan.PolicyProof == nil || req.Plan.PolicyProof.Family() != req.Plan.Strategy {
			return Result{}, fmt.Errorf("matching policy proof is required before shadow execution")
		}
		if req.Plan.Strategy == "restore-command" {
			if err := ValidateCandidatePatch(req.Patch, req.Plan.Strategy, req.Resource, req.Namespace); err != nil {
				return Result{}, err
			}
		}
		if err := repairpolicy.Revalidate(ctx, c, req.Plan.PolicyProof, req.Patch); err != nil {
			return Result{}, err
		}
		req.Retries = 0
		req.AI = nil
	}
	if req.Timeout <= 0 {
		req.Timeout = 10 * time.Minute
	}
	if req.Retries < 0 {
		req.Retries = 0
	}
	if req.Egress == "" {
		req.Egress = "deny"
	}
	if !strings.EqualFold(req.Egress, "deny") {
		return Result{}, fmt.Errorf("shadow verification only supports deny egress; dependency access is not enabled")
	}
	result := Result{
		Resource:      req.Resource,
		Namespace:     req.Namespace,
		Delivery:      string(req.Delivery),
		VerifiedPatch: req.Patch,
		Status:        VerificationBlocked,
	}
	if strings.TrimSpace(req.ProbeImage) == "" {
		req.ProbeImage = os.Getenv("FIXORA_SHADOW_PROBE_IMAGE")
	}
	if err := validateProbeImage(req.ProbeImage); err != nil {
		result.Isolation = &IsolationReport{Status: string(VerificationBlocked), Summary: err.Error()}
		result.Warnings = appendUnique(result.Warnings, err.Error())
		return result, nil
	}
	var lastPlan clonePlan
	for attempt := 1; attempt <= req.Retries+1; attempt++ {
		session := uuid.NewString()
		shadowNSName := shadowNamespaceName(session)
		shadowNS, err := c.CreateNamespace(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:        shadowNSName,
			Labels:      map[string]string{labelSandbox: "true", labelSession: session},
			Annotations: map[string]string{"fixora.io/source-namespace": req.Namespace},
		}})
		if err != nil {
			result.IsolationNamespace = shadowNSName
			recovered, getErr := c.GetNamespace(ctx, shadowNSName)
			if getErr == nil && recovered.Name == shadowNSName && recovered.Labels[labelSandbox] == "true" && recovered.Labels[labelSession] == session {
				if recovered.UID == "" {
					cleanupWarning(&result, fmt.Sprintf("cleanup failed to confirm ownership for namespace/%s: API returned no UID after ambiguous create", shadowNSName))
					return result, fmt.Errorf("create isolated shadow namespace response was ambiguous; cannot safely clean up without a UID")
				}
				cleanup(ctx, c, clonePlan{Namespace: recovered}, &result)
				if len(result.cleanupFailures()) > 0 {
					return result, fmt.Errorf("create isolated shadow namespace failed and cleanup failed: %s", result.cleanupFailures()[0])
				}
			} else if getErr != nil && !apierrors.IsNotFound(getErr) {
				cleanupWarning(&result, fmt.Sprintf("cleanup failed to inspect namespace/%s after ambiguous create: %v", shadowNSName, getErr))
			}
			return result, fmt.Errorf("create isolated shadow namespace: %w", err)
		}
		result.IsolationNamespace = shadowNS.Name
		plan, err := buildClonePlan(ctx, c, req, session, shadowNS.Name)
		if err != nil {
			cleanup(ctx, c, clonePlan{Namespace: shadowNS}, &result)
			return result, err
		}
		plan.Namespace = shadowNS
		lastPlan = plan
		result.Evidence = collectFailureEvidence(ctx, c, req.Namespace, plan.Original, nil)
		accountHasPullSecrets, accountErr := inspectSourceServiceAccount(ctx, c, req.Namespace, plan.Original)
		if accountErr != nil {
			result.Status = VerificationBlocked
			result.Isolation = &IsolationReport{Status: string(VerificationBlocked), Summary: "source service-account image-pull dependencies could not be inspected"}
			result.Warnings = appendUnique(result.Warnings, accountErr.Error())
			cleanup(ctx, c, plan, &result)
			if len(result.cleanupFailures()) > 0 {
				return result, fmt.Errorf("shadow cleanup failed: %s", result.cleanupFailures()[0])
			}
			return result, nil
		}
		limitations := sandboxDependencyLimitations(plan.Original)
		if accountHasPullSecrets {
			limitations = appendUnique(limitations, "source ServiceAccount image-pull secret references are not copied into the isolated namespace")
		}
		if len(limitations) > 0 {
			result.Status = VerificationInconclusive
			result.Isolation = &IsolationReport{Status: string(VerificationInconclusive), Summary: strings.Join(limitations, "; ")}
			result.Warnings = appendUnique(result.Warnings, limitations...)
			cleanup(ctx, c, plan, &result)
			if len(result.cleanupFailures()) > 0 {
				result.Status = VerificationBlocked
				return result, fmt.Errorf("shadow cleanup failed: %s", result.cleanupFailures()[0])
			}
			return result, nil
		}
		if req.Plan.PolicyProof != nil {
			if err := repairpolicy.Revalidate(ctx, c, req.Plan.PolicyProof, req.Patch); err != nil {
				cleanup(ctx, c, plan, &result)
				return result, err
			}
		}
		if req.Plan.Strategy == "security-strengthen" {
			if _, err := c.Clientset.CoreV1().Pods(plan.Clone.Namespace).Create(ctx, plan.Clone, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
				cleanup(ctx, c, plan, &result)
				return result, fmt.Errorf("security shadow admission dry-run: %w", err)
			}
		}
		result.Warnings = appendUnique(result.Warnings, plan.Warnings...)
		result.CloneName = plan.Clone.Name
		result.NetworkPolicyName = plan.Policy.Name
		isolation, isolationErr := proveNetworkPolicy(ctx, c, plan.Namespace, plan.Policy, session, req.ProbeImage)
		result.Isolation = &isolation
		if isolationErr != nil {
			result.Status = VerificationInconclusive
			if failure, ok := isolationErr.(gateFailure); ok {
				result.Status = failure.status
			}
			result.Isolation.Status = string(result.Status)
			result.Isolation.Summary = isolationErr.Error()
			result.Warnings = appendUnique(result.Warnings, isolationErr.Error())
			// Keep is only meaningful after isolation has been proven. Preserve
			// no namespace containing probes from a failed or inconclusive gate.
			cleanup(ctx, c, plan, &result)
			if len(result.cleanupFailures()) > 0 {
				result.Status = VerificationBlocked
				return result, fmt.Errorf("shadow cleanup failed: %s", result.cleanupFailures()[0])
			}
			return result, nil
		}
		if err := ensureSandboxNetworkPolicy(ctx, c, plan.Policy); err != nil {
			result.Status = VerificationBlocked
			result.Isolation.Status = string(VerificationBlocked)
			result.Isolation.Summary = err.Error()
			result.Warnings = appendUnique(result.Warnings, err.Error())
			// A policy identity change invalidates the proof, so never retain
			// this namespace even when the operator requested --keep-shadow.
			cleanup(ctx, c, plan, &result)
			if len(result.cleanupFailures()) > 0 {
				return result, fmt.Errorf("shadow cleanup failed: %s", result.cleanupFailures()[0])
			}
			return result, nil
		}
		if _, err := c.CreatePod(ctx, plan.Clone); err != nil {
			cleanup(ctx, c, plan, &result)
			return result, fmt.Errorf("create shadow pod: %w", err)
		}
		verification := verifyCloneWithSource(ctx, c, plan.Clone.Namespace, plan.Clone.Name, req.Timeout, attempt, resourceAllowsCompletion(req.Resource), plan.Original, result.Evidence)
		result.Evidence = verification.Evidence
		result.Attempts = append(result.Attempts, verification)
		result.Parity = parityScore(plan.Original, plan.UnpatchedClone)
		result.Verified = verification.Ready
		result.Status = statusForAttempt(verification)
		if result.Verified && req.Plan.PolicyProof != nil {
			pod, err := c.GetPod(ctx, plan.Clone.Namespace, plan.Clone.Name)
			if err == nil {
				if req.Plan.Strategy == "restore-command" {
					result.PolicyReceipt, err = repairpolicy.VerifyStartup(ctx, c, req.Plan.PolicyProof, &pod, req.Patch)
				} else if req.Plan.Strategy == "restore-placement" {
					result.PolicyReceipt, err = repairpolicy.VerifyPlacement(ctx, c, req.Plan.PolicyProof, &pod, req.Patch)
				} else if req.Plan.Strategy == "security-strengthen" {
					result.PolicyReceipt, err = repairpolicy.VerifySecurity(ctx, c, req.Plan.PolicyProof, &pod, req.Patch)
				} else {
					result.PolicyReceipt, err = repairpolicy.VerifyCommon(ctx, c, req.Plan.PolicyProof, &pod, req.Patch)
				}
			}
			if err != nil {
				result.Verified = false
				result.Status = VerificationFailed
				result.Warnings = appendUnique(result.Warnings, "policy verification failed: "+err.Error())
				break
			}
			summary := result.PolicyReceipt.Summary()
			result.PolicySummary = &summary
		}
		if result.Verified {
			result.Caveats = appendUnique(result.Caveats, partialPassCaveats(plan.Original, plan.NamespaceMetadata)...)
			break
		}
		if result.Status == VerificationInconclusive {
			// An incomplete observation does not prove that the candidate is bad
			// and cannot justify an automatic revision/retry.
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
			cleanup(ctx, c, plan, &result)
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
		result.Verified = false
		result.Status = VerificationBlocked
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
	if plan.Namespace != nil {
		ns, err := c.GetNamespace(cleanupCtx, plan.Namespace.Name)
		if apierrors.IsNotFound(err) {
			result.Cleanup = appendUnique(result.Cleanup, "deleted namespace/"+plan.Namespace.Name)
			return
		}
		if err != nil {
			cleanupWarning(result, fmt.Sprintf("cleanup failed for namespace/%s: %v", plan.Namespace.Name, err))
			return
		}
		if !isShadowObject(ns.Labels) || ns.UID == "" || ns.UID != plan.Namespace.UID {
			cleanupWarning(result, fmt.Sprintf("cleanup failed to confirm ownership for namespace/%s: label or UID did not match", plan.Namespace.Name))
			return
		}
		if err := c.DeleteNamespaceUID(cleanupCtx, ns.Name, types.UID(ns.UID)); err != nil && !apierrors.IsNotFound(err) {
			cleanupWarning(result, fmt.Sprintf("cleanup failed for namespace/%s: %v", ns.Name, err))
			return
		}
		for {
			_, err := c.GetNamespace(cleanupCtx, ns.Name)
			if apierrors.IsNotFound(err) {
				result.Cleanup = appendUnique(result.Cleanup, "deleted namespace/"+ns.Name)
				return
			}
			if err != nil {
				cleanupWarning(result, fmt.Sprintf("cleanup failed for namespace/%s while waiting: %v", ns.Name, err))
				return
			}
			select {
			case <-cleanupCtx.Done():
				cleanupWarning(result, fmt.Sprintf("cleanup failed for namespace/%s: deletion did not complete", ns.Name))
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
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

func cleanupWarning(result *Result, message string) {
	result.Warnings = appendUnique(result.Warnings, message)
	fmt.Fprintf(os.Stderr, "ERROR: %s\n", message)
}

func shadowNamespaceName(session string) string {
	compact := strings.ReplaceAll(session, "-", "")
	if len(compact) > 16 {
		compact = compact[:16]
	}
	return "fixora-shadow-" + compact
}

func (r Result) cleanupFailures() []string {
	var failures []string
	for _, warning := range r.Warnings {
		if strings.HasPrefix(warning, "cleanup failed") || strings.HasPrefix(warning, "cleanup skipped") {
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
