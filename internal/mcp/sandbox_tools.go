package mcp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	sandboxLabelName     = "fixora.io/sandbox"
	sandboxSessionLabel  = "fixora.io/session"
	sourceNamespaceLabel = "fixora.io/source-namespace"
)

type openSandboxSummary struct {
	Verified       bool     `json:"verified"`
	SandboxID      string   `json:"sandboxId,omitempty"`
	Resource       string   `json:"resource"`
	Namespace      string   `json:"namespace,omitempty"`
	Pod            string   `json:"pod,omitempty"`
	Container      string   `json:"container,omitempty"`
	Parity         int      `json:"parity,omitempty"`
	ExpiresAt      string   `json:"expiresAt,omitempty"`
	ExecArgs       []string `json:"execArgs,omitempty"`
	NetworkPolicy  string   `json:"networkPolicy,omitempty"`
	FailureSummary string   `json:"failureSummary,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

type closeSandboxInput struct {
	SandboxID string `json:"sandboxId" jsonschema:"Opaque handle returned by open-sandbox; never use a namespace name as a handle"`
	Confirm   bool   `json:"confirm" jsonschema:"Must be true after operator approval to delete this Fixora sandbox namespace"`
}

type closeSandboxSummary struct {
	Closed bool `json:"closed"`
}

func (s Server) callOpenSandbox(ctx context.Context, input shadowInput) (openSandboxSummary, error) {
	summary := openSandboxSummary{Resource: input.Resource}
	if !s.EnableSandbox || !s.EnableShadow || s.sandboxes == nil {
		return summary, errors.New("MCP verified sandbox is disabled at server startup")
	}
	if !input.Confirm {
		return summary, errors.New("opening a sandbox requires confirm=true after operator approval")
	}
	id, err := s.sandboxes.reserve()
	if err != nil {
		return summary, err
	}
	committed := false
	defer func() {
		if !committed {
			s.sandboxes.cancelReservation(id)
		}
	}()

	result, runErr := s.executeShadow(ctx, input, true)
	summary.Verified = result.Verified && result.Status == shadow.VerificationVerified
	summary.Resource = result.Resource
	summary.FailureSummary = result.FailureSummary
	summary.Warnings = result.Warnings
	if result.ParityAssessment != nil {
		summary.Parity = result.OperationalParity
	} else {
		summary.Parity = result.Parity
	}
	if runErr != nil || !summary.Verified {
		if result.Verified && result.IsolationNamespace != "" && result.IsolationUID != "" {
			s.warnCleanupFailure(&summary, ctx, result.IsolationNamespace, result.IsolationUID)
		}
		if runErr != nil {
			return summary, runErr
		}
		return summary, errors.New("sandbox verification did not pass; no sandbox access was opened")
	}
	client, err := s.typedClientForSandbox()
	if err != nil {
		s.warnCleanupFailure(&summary, ctx, result.IsolationNamespace, result.IsolationUID)
		return summary, errors.New("could not recheck the verified sandbox before returning access details")
	}
	candidate, pod, err := validateRetainedSandbox(ctx, client, result)
	if err != nil {
		s.warnCleanupFailure(&summary, ctx, result.IsolationNamespace, result.IsolationUID)
		return summary, errors.New("verified sandbox identity, Pod readiness, or deny policy changed; the sandbox was not opened")
	}
	containerName := strings.TrimSpace(input.Container)
	if containerName == "" && len(pod.Spec.Containers) > 0 {
		containerName = pod.Spec.Containers[0].Name
	}
	if containerName == "" || !podHasContainer(pod, containerName) {
		s.warnCleanupFailure(&summary, ctx, result.IsolationNamespace, result.IsolationUID)
		return summary, errors.New("verified sandbox does not contain the requested application container")
	}
	expiresAt, err := s.sandboxes.commit(id, candidate)
	if err != nil {
		s.warnCleanupFailure(&summary, ctx, result.IsolationNamespace, result.IsolationUID)
		return summary, errors.New("could not register the verified sandbox for bounded cleanup")
	}
	committed = true
	args := []string{"kubectl"}
	if s.Kubectl.Context != "" {
		args = append(args, "--context", s.Kubectl.Context)
	}
	args = append(args, "-n", result.IsolationNamespace, "exec", "-it", "pod/"+result.CloneName, "-c", containerName, "--", "/bin/sh")
	summary.SandboxID = id
	summary.Namespace = result.IsolationNamespace
	summary.Pod = result.CloneName
	summary.Container = containerName
	summary.ExpiresAt = expiresAt.UTC().Format("2006-01-02T15:04:05Z")
	summary.ExecArgs = args
	summary.NetworkPolicy = "deny-ingress-and-egress"
	return summary, nil
}

func (s Server) callCloseSandbox(ctx context.Context, input closeSandboxInput) (closeSandboxSummary, error) {
	if !s.EnableSandbox || s.sandboxes == nil {
		return closeSandboxSummary{}, errors.New("MCP verified sandbox is disabled at server startup")
	}
	if !input.Confirm {
		return closeSandboxSummary{}, errors.New("closing a sandbox requires confirm=true after operator approval")
	}
	id := strings.TrimSpace(input.SandboxID)
	if len(id) != 48 {
		return closeSandboxSummary{}, errors.New("sandboxId is invalid or unknown")
	}
	if err := s.sandboxes.close(ctx, id); err != nil {
		return closeSandboxSummary{}, err
	}
	return closeSandboxSummary{Closed: true}, nil
}

func (s Server) cleanupRetainedSandbox(ctx context.Context, namespace, uid string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	client, err := s.typedClientForSandbox()
	if err != nil {
		return err
	}
	current, err := client.GetNamespace(cleanupCtx, namespace)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if current == nil {
		return errors.New("sandbox namespace read returned no object")
	}
	if string(current.UID) != uid {
		return errors.New("sandbox namespace UID changed; refusing cleanup")
	}
	candidates, err := shadow.ListCleanupCandidates(cleanupCtx, client)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if candidate.Namespace == namespace && candidate.UID == uid {
			return shadow.DeleteCleanupCandidate(cleanupCtx, client, candidate)
		}
	}
	return errors.New("sandbox namespace no longer matches Fixora cleanup ownership")
}

func (s Server) warnCleanupFailure(summary *openSandboxSummary, ctx context.Context, namespace, uid string) {
	if err := s.cleanupRetainedSandbox(ctx, namespace, uid); err != nil {
		summary.Warnings = append(summary.Warnings, "sandbox cleanup could not be confirmed; run kubectl fixora cleanup")
	}
}

func validateRetainedSandbox(ctx context.Context, client *kube.TypedClient, result shadow.Result) (shadow.CleanupCandidate, *corev1.Pod, error) {
	if client == nil || result.IsolationNamespace == "" || result.IsolationUID == "" || result.CloneName == "" || result.CloneUID == "" || result.NetworkPolicyName == "" {
		return shadow.CleanupCandidate{}, nil, errors.New("sandbox identity is incomplete")
	}
	candidates, err := shadow.ListCleanupCandidates(ctx, client)
	if err != nil {
		return shadow.CleanupCandidate{}, nil, err
	}
	var candidate shadow.CleanupCandidate
	for _, item := range candidates {
		if item.Namespace == result.IsolationNamespace && item.UID == result.IsolationUID {
			candidate = item
			break
		}
	}
	if candidate.Namespace == "" || candidate.SourceNamespace != result.Namespace {
		return shadow.CleanupCandidate{}, nil, errors.New("Fixora cleanup identity did not match this run")
	}
	currentNamespace, err := client.GetNamespace(ctx, result.IsolationNamespace)
	if err != nil || currentNamespace == nil || string(currentNamespace.UID) != candidate.UID || currentNamespace.Labels[sandboxLabelName] != "true" || currentNamespace.Labels[sandboxSessionLabel] != candidate.Session || currentNamespace.Annotations[sourceNamespaceLabel] != candidate.SourceNamespace {
		return candidate, nil, errors.New("sandbox namespace identity changed during verification")
	}
	pod, err := client.GetTypedPod(ctx, result.IsolationNamespace, result.CloneName)
	if err != nil || pod == nil || string(pod.UID) != result.CloneUID || pod.Labels[sandboxLabelName] != "true" || pod.Labels[sandboxSessionLabel] != candidate.Session || !podReadyWithoutRestarts(pod) {
		return candidate, nil, errors.New("verified candidate Pod changed or is no longer Ready")
	}
	policy, err := client.Clientset.NetworkingV1().NetworkPolicies(result.IsolationNamespace).Get(ctx, result.NetworkPolicyName, metav1.GetOptions{})
	if err != nil || !isDefaultDenyPolicy(policy, candidate.Session) {
		return candidate, nil, errors.New("shadow NetworkPolicy is missing or changed")
	}
	return candidate, pod, nil
}

func podHasContainer(pod *corev1.Pod, name string) bool {
	if pod == nil {
		return false
	}
	for _, container := range pod.Spec.Containers {
		if container.Name == name {
			return true
		}
	}
	return false
}

func podReadyWithoutRestarts(pod *corev1.Pod) bool {
	if pod == nil || pod.Status.Phase != corev1.PodRunning {
		return false
	}
	ready := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	if !ready || len(pod.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, status := range append(append([]corev1.ContainerStatus{}, pod.Status.ContainerStatuses...), pod.Status.InitContainerStatuses...) {
		if status.RestartCount != 0 {
			return false
		}
	}
	return true
}

func isDefaultDenyPolicy(policy *networkingv1.NetworkPolicy, session string) bool {
	if policy == nil || policy.Labels[sandboxLabelName] != "true" || policy.Labels[sandboxSessionLabel] != session ||
		len(policy.Spec.PodSelector.MatchLabels) != 1 || policy.Spec.PodSelector.MatchLabels[sandboxSessionLabel] != session || len(policy.Spec.PodSelector.MatchExpressions) != 0 ||
		len(policy.Spec.Ingress) != 0 || len(policy.Spec.Egress) != 0 || len(policy.Spec.PolicyTypes) != 2 {
		return false
	}
	hasIngress, hasEgress := false, false
	for _, policyType := range policy.Spec.PolicyTypes {
		hasIngress = hasIngress || policyType == networkingv1.PolicyTypeIngress
		hasEgress = hasEgress || policyType == networkingv1.PolicyTypeEgress
	}
	return hasIngress && hasEgress
}
