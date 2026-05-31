package fix

import (
	"fmt"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type Plan struct {
	Resource         string   `json:"resource"`
	Namespace        string   `json:"namespace"`
	Status           string   `json:"status"`
	Strategy         string   `json:"strategy"`
	Safe             bool     `json:"safe"`
	CanApply         bool     `json:"canApply"`
	Confidence       int      `json:"confidence"`
	Risk             string   `json:"risk"`
	AutoFixRequested bool     `json:"autoFixRequested"`
	Steps            []string `json:"steps"`
	Warnings         []string `json:"warnings,omitempty"`
	BlockedReasons   []string `json:"blockedReasons,omitempty"`
	Guardrails       []string `json:"guardrails,omitempty"`
	PatchTemplate    string   `json:"patchTemplate"`
	ApplyCommand     string   `json:"applyCommand,omitempty"`
	RollbackCommand  string   `json:"rollbackCommand,omitempty"`
}

type ConcreteOptions struct {
	Container     string
	Image         string
	MemoryRequest string
	MemoryLimit   string
	CPURequest    string
	EnvName       string
	ConfigMap     string
	ConfigKey     string
}

func BuildPlan(finding analyzer.Finding) Plan {
	resource := finding.ResourceKind + "/" + finding.ResourceName
	plan := Plan{
		Resource:   resource,
		Namespace:  finding.Namespace,
		Status:     finding.Status,
		Strategy:   "collect-evidence",
		Safe:       false,
		CanApply:   false,
		Confidence: 45,
		Risk:       "needs-review",
		Steps: []string{
			"Confirm the top-level owner and GitOps source before editing rendered Kubernetes objects.",
			"Review events and logs for the failing container.",
			"Generate a PR against the source manifest, Helm values file, or Kustomize overlay.",
		},
	}
	if len(finding.Recommendations) > 0 {
		rec := finding.Recommendations[0]
		plan.Strategy = firstNonEmpty(rec.PatchType, slug(rec.Title))
		plan.Safe = rec.SafeByDefault
	}
	if finding.GitOps.TargetAdvice != "" {
		plan.Steps = append(plan.Steps, finding.GitOps.TargetAdvice)
		plan.BlockedReasons = append(plan.BlockedReasons, "GitOps-managed workload requires source-level patching.")
	}
	plan.Guardrails = append(plan.Guardrails, "identity-check-required", "source-patch-preferred", "secret-values-blocked")
	switch {
	case strings.Contains(finding.Status, "ImagePull"):
		plan.PatchTemplate = imagePatchTemplate(finding)
		plan.Confidence = 70
		plan.Warnings = append(plan.Warnings, "Verify the replacement image exists, is pinned by tag or digest, and supports the node CPU architecture before applying.")
	case strings.Contains(finding.Status, "OOMKilled"):
		plan.PatchTemplate = resourcesPatchTemplate(finding)
		plan.Confidence = 62
		plan.Warnings = append(plan.Warnings, "Right-size from observed usage. Do not only raise limits if the process is intentionally allocating too much memory.")
	case strings.Contains(finding.Status, "CrashLoopBackOff"):
		plan.PatchTemplate = runtimePatchTemplate(finding)
		plan.Confidence = 55
		plan.Warnings = append(plan.Warnings, "Crash fixes are workload-specific. Validate command, args, env, probes, permissions, and dependencies before applying.")
	case strings.Contains(finding.Status, "Config"):
		plan.PatchTemplate = envPatchTemplate(finding)
		plan.Confidence = 68
		plan.Warnings = append(plan.Warnings, "Do not write secret values into plain manifests. Reference existing Secrets or create them with kubectl/secret tooling.")
	default:
		plan.PatchTemplate = genericPatchTemplate(finding)
		plan.BlockedReasons = append(plan.BlockedReasons, "No deterministic patch strategy matched this status.")
	}
	plan.ApplyCommand = fmt.Sprintf("kubectl apply -f fixora-patch.yaml -n %s", finding.Namespace)
	plan.RollbackCommand = rollbackCommand(finding)
	if plan.Safe && len(plan.BlockedReasons) == 0 {
		plan.Risk = "low"
	}
	return plan
}

func Concretize(plan Plan, opts ConcreteOptions) Plan {
	patch := plan.PatchTemplate
	replacements := map[string]string{
		"TODO_CONTAINER_NAME":          opts.Container,
		"TODO_PINNED_MULTI_ARCH_IMAGE": opts.Image,
		"TODO_OBSERVED_REQUEST":        opts.MemoryRequest,
		"TODO_OBSERVED_CPU_REQUEST":    opts.CPURequest,
		"TODO_SAFE_LIMIT":              opts.MemoryLimit,
		"TODO_ENV_NAME":                opts.EnvName,
		"TODO_CONFIGMAP":               opts.ConfigMap,
		"TODO_KEY":                     opts.ConfigKey,
	}
	for key, value := range replacements {
		if value != "" {
			patch = strings.ReplaceAll(patch, key, value)
		}
	}
	plan.PatchTemplate = patch
	if !strings.Contains(patch, "TODO_") && strings.TrimSpace(patch) != "" && !strings.HasPrefix(strings.TrimSpace(patch), "#") {
		plan.CanApply = true
		plan.Safe = true
		plan.Risk = "low"
		plan.Confidence = max(plan.Confidence, 82)
		plan.BlockedReasons = nil
		plan.Guardrails = append(plan.Guardrails, "concrete-values-provided")
	}
	return plan
}

func (p Plan) DiffView() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Fixora remediation plan\n")
	fmt.Fprintf(&b, "resource: %s\nnamespace: %s\nstatus: %s\nstrategy: %s\nsafe: %t\ncanApply: %t\nconfidence: %d\nrisk: %s\n\n", p.Resource, p.Namespace, p.Status, p.Strategy, p.Safe, p.CanApply, p.Confidence, p.Risk)
	for _, step := range p.Steps {
		fmt.Fprintf(&b, "+ %s\n", step)
	}
	for _, warning := range p.Warnings {
		fmt.Fprintf(&b, "! %s\n", warning)
	}
	for _, reason := range p.BlockedReasons {
		fmt.Fprintf(&b, "x %s\n", reason)
	}
	if p.RollbackCommand != "" {
		fmt.Fprintf(&b, "\nrollback: %s\n", p.RollbackCommand)
	}
	fmt.Fprintf(&b, "\n--- suggested patch template ---\n%s", p.PatchTemplate)
	return b.String()
}

func (p Plan) PatchYAML() string {
	return p.PatchTemplate
}

func imagePatchTemplate(f analyzer.Finding) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: %s
metadata:
  name: %s
  namespace: %s
spec:
  template:
    spec:
      containers:
      - name: TODO_CONTAINER_NAME
        image: TODO_PINNED_MULTI_ARCH_IMAGE
`, normalizeKind(f.ResourceKind), f.ResourceName, f.Namespace)
}

func resourcesPatchTemplate(f analyzer.Finding) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: %s
metadata:
  name: %s
  namespace: %s
spec:
  template:
    spec:
      containers:
      - name: TODO_CONTAINER_NAME
        resources:
          requests:
            memory: TODO_OBSERVED_REQUEST
            cpu: TODO_OBSERVED_CPU_REQUEST
          limits:
            memory: TODO_SAFE_LIMIT
`, normalizeKind(f.ResourceKind), f.ResourceName, f.Namespace)
}

func runtimePatchTemplate(f analyzer.Finding) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: %s
metadata:
  name: %s
  namespace: %s
spec:
  template:
    spec:
      containers:
      - name: TODO_CONTAINER_NAME
        # Add only the proven command, args, probe, env, or securityContext change.
`, normalizeKind(f.ResourceKind), f.ResourceName, f.Namespace)
}

func envPatchTemplate(f analyzer.Finding) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: %s
metadata:
  name: %s
  namespace: %s
spec:
  template:
    spec:
      containers:
      - name: TODO_CONTAINER_NAME
        env:
        - name: TODO_ENV_NAME
          valueFrom:
            configMapKeyRef:
              name: TODO_CONFIGMAP
              key: TODO_KEY
`, normalizeKind(f.ResourceKind), f.ResourceName, f.Namespace)
}

func genericPatchTemplate(f analyzer.Finding) string {
	return fmt.Sprintf(`# Fixora could not infer a concrete patch.
# Resource: %s/%s
# Namespace: %s
# Status: %s
# Add a source-controlled manifest, Helm values, or Kustomize patch after reviewing evidence.
`, f.ResourceKind, f.ResourceName, f.Namespace, f.Status)
}

func normalizeKind(kind string) string {
	switch strings.ToLower(kind) {
	case "deployment", "deployments":
		return "Deployment"
	case "statefulset", "statefulsets":
		return "StatefulSet"
	case "daemonset", "daemonsets":
		return "DaemonSet"
	default:
		return kind
	}
}

func slug(value string) string {
	return strings.ToLower(strings.ReplaceAll(value, " ", "-"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func rollbackCommand(f analyzer.Finding) string {
	switch strings.ToLower(f.ResourceKind) {
	case "deployment":
		return fmt.Sprintf("kubectl rollout undo deployment/%s -n %s", f.ResourceName, f.Namespace)
	case "statefulset":
		return fmt.Sprintf("kubectl rollout undo statefulset/%s -n %s", f.ResourceName, f.Namespace)
	case "daemonset":
		return fmt.Sprintf("kubectl rollout undo daemonset/%s -n %s", f.ResourceName, f.Namespace)
	}
	if f.GitOps.HelmRelease != "" {
		return fmt.Sprintf("helm rollback %s -n %s", f.GitOps.HelmRelease, f.Namespace)
	}
	return "restore the previous Git commit or re-apply the last known good manifest"
}
