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
	ApplyEligible    bool     `json:"applyEligible"`
	Confidence       int      `json:"confidence"`
	Risk             string   `json:"risk"`
	AutoFixRequested bool     `json:"autoFixRequested"`
	Patches          []Patch  `json:"patches,omitempty"`
	Commands         []string `json:"commands,omitempty"`
	Verification     []string `json:"verification,omitempty"`
	Rollback         []string `json:"rollback,omitempty"`
	Steps            []string `json:"steps"`
	Warnings         []string `json:"warnings,omitempty"`
	BlockedReasons   []string `json:"blockedReasons,omitempty"`
	Guardrails       []string `json:"guardrails,omitempty"`
	PatchTemplate    string   `json:"patchTemplate"`
	RollbackCommand  string   `json:"rollbackCommand,omitempty"`
}

type Patch struct {
	Type        string `json:"type"`
	Target      string `json:"target"`
	Description string `json:"description"`
	Preview     string `json:"preview,omitempty"`
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
	Strategy      string
	ForceRisky    bool
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
		Verification: []string{
			fmt.Sprintf("kubectl get %s -n %s -o wide", strings.ToLower(resource), finding.Namespace),
			fmt.Sprintf("kubectl describe %s -n %s", strings.ToLower(resource), finding.Namespace),
			fmt.Sprintf("kubectl get events -n %s --sort-by=.lastTimestamp", finding.Namespace),
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
		plan.Strategy = "image"
		plan.PatchTemplate = imagePatchTemplate(finding)
		plan.Patches = append(plan.Patches, Patch{Type: "strategic-merge", Target: resource, Description: "Pin a verified replacement image for the failing container.", Preview: plan.PatchTemplate})
		plan.Confidence = 70
		plan.Warnings = append(plan.Warnings, "Verify the replacement image exists, is pinned by tag or digest, and supports the node CPU architecture before applying.")
		plan.Verification = append(plan.Verification, "crane digest <image> or docker manifest inspect <image>")
	case strings.Contains(finding.Status, "OOMKilled"):
		plan.Strategy = "resources"
		plan.PatchTemplate = resourcesPatchTemplate(finding)
		plan.Patches = append(plan.Patches, Patch{Type: "strategic-merge", Target: resource, Description: "Set memory and CPU requests/limits from observed usage with headroom.", Preview: plan.PatchTemplate})
		plan.Confidence = 62
		plan.Warnings = append(plan.Warnings, "Right-size from observed usage. Do not only raise limits if the process is intentionally allocating too much memory.")
		plan.Verification = append(plan.Verification, "query Prometheus p95/p99 memory before choosing request and limit")
	case strings.Contains(finding.Status, "CrashLoopBackOff"):
		plan.Strategy = "runtime"
		plan.PatchTemplate = runtimePatchTemplate(finding)
		plan.Patches = append(plan.Patches, Patch{Type: "review-only", Target: resource, Description: "Patch command, args, probe, env, or securityContext only after confirming the actual root cause.", Preview: plan.PatchTemplate})
		plan.Confidence = 55
		plan.BlockedReasons = append(plan.BlockedReasons, "CrashLoopBackOff fixes require workload-specific root-cause proof before apply.")
	case strings.Contains(finding.Status, "CreateContainerConfigError"):
		plan.Strategy = "env"
		plan.PatchTemplate = envPatchTemplate(finding)
		plan.Patches = append(plan.Patches, Patch{Type: "strategic-merge", Target: resource, Description: "Inject the missing ConfigMap or Secret reference.", Preview: plan.PatchTemplate})
		plan.Confidence = 80
		plan.Warnings = append(plan.Warnings, "Ensure the referenced ConfigMap/Secret exists in the same namespace before applying.")
	case strings.Contains(finding.Status, "ExecFormatError"):
		plan.Strategy = "fix-architecture"
		plan.PatchTemplate = archPatchTemplate(finding)
		plan.Patches = append(plan.Patches, Patch{Type: "strategic-merge", Target: resource, Description: "Pin a verified multi-architecture image for the failing container.", Preview: plan.PatchTemplate})
		plan.Confidence = 85
		plan.Warnings = append(plan.Warnings, "The container image is built for a different CPU architecture than the target node.")
		plan.Steps = append(plan.Steps, "Rebuild the image with docker buildx build --platform linux/amd64,linux/arm64 to support multiple architectures.")
		plan.Steps = append(plan.Steps, "Use nodeSelector only as a reviewed fallback after confirming matching node capacity and scheduling policy.")
	case strings.Contains(finding.Status, "PermissionDenied"):
		plan.Strategy = "security"
		plan.PatchTemplate = securityContextPatchTemplate(finding)
		plan.Patches = append(plan.Patches, Patch{Type: "strategic-merge", Target: resource, Description: "Configure securityContext fsGroup or runAsUser for the container.", Preview: plan.PatchTemplate})
		plan.Confidence = 75
		plan.Warnings = append(plan.Warnings, "Setting specific UIDs or fsGroup can affect volume write access.")
	case strings.Contains(finding.Status, "NoEndpoints") || strings.Contains(finding.Status, "ConnectionRefused"):
		plan.PatchTemplate = serviceSelectorPatchTemplate(finding)
		plan.Patches = append(plan.Patches, Patch{Type: "review-only", Target: resource, Description: "Repair the Service selector to match running pods.", Preview: plan.PatchTemplate})
		plan.Strategy = "repair-selector"
		plan.Confidence = 85
	case strings.Contains(finding.Status, "HPA"):
		plan.Strategy = "hpa"
		plan.PatchTemplate = hpaRequestsPatchTemplate(finding)
		plan.Patches = append(plan.Patches, Patch{Type: "strategic-merge", Target: resource, Description: "Add resource requests to the scale target so the HPA can calculate metrics.", Preview: plan.PatchTemplate})
		plan.Confidence = 90
		plan.Warnings = append(plan.Warnings, "Without CPU/memory requests, the HPA cannot compute utilization percentage.")
	case strings.Contains(finding.Status, "Evicted"):
		plan.Strategy = "pdb"
		plan.PatchTemplate = pdbPatchTemplate(finding)
		plan.Patches = append(plan.Patches, Patch{Type: "review-only", Target: resource, Description: "Configure PodDisruptionBudget to protect critical workloads from node draining.", Preview: plan.PatchTemplate})
		plan.Confidence = 60
		plan.Warnings = append(plan.Warnings, "Do not set maxUnavailable: 0 as it blocks node upgrades.")
	case strings.Contains(finding.Status, "Webhook"):
		plan.Strategy = "webhook"
		plan.PatchTemplate = webhookPatchTemplate(finding)
		plan.Patches = append(plan.Patches, Patch{Type: "review-only", Target: resource, Description: "Bypass failing admission webhook temporarily.", Preview: plan.PatchTemplate})
		plan.Confidence = 40
		plan.BlockedReasons = append(plan.BlockedReasons, "Modifying admission webhooks requires high-privilege review.")
		plan.Warnings = append(plan.Warnings, "Webhook changes affect admission safety. Prefer restoring backend Service before changing failure policy.")
	default:
		plan.PatchTemplate = genericPatchTemplate(finding)
		plan.BlockedReasons = append(plan.BlockedReasons, "No deterministic patch strategy matched this status.")
	}
	plan.Commands = []string{fmt.Sprintf("kubectl apply -f fixora-patch.yaml -n %s", finding.Namespace)}
	plan.RollbackCommand = rollbackCommand(finding)
	if plan.RollbackCommand != "" {
		plan.Rollback = append(plan.Rollback, plan.RollbackCommand)
	}
	if plan.Safe && len(plan.BlockedReasons) == 0 {
		plan.Risk = "low"
	}
	plan.refreshApplyEligibility(false)
	return plan
}

func Concretize(plan Plan, opts ConcreteOptions) Plan {
	if opts.Strategy != "" {
		plan.Strategy = opts.Strategy
		plan.Guardrails = append(plan.Guardrails, "strategy="+opts.Strategy)
	}
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
		validationWarnings, validationBlocks := validateConcretePatch(plan)
		plan.Warnings = append(plan.Warnings, validationWarnings...)
		plan.BlockedReasons = appendUniqueMany(plan.BlockedReasons, validationBlocks...)
		if len(validationBlocks) == 0 {
			plan.CanApply = true
			plan.Safe = true
			plan.Risk = "low"
			plan.Confidence = max(plan.Confidence, 90)
			plan.BlockedReasons = nil
			plan.Guardrails = append(plan.Guardrails, "concrete-values-provided")
		} else {
			plan.CanApply = false
			plan.Safe = false
			plan.Risk = "review-only"
		}
	}
	plan.refreshApplyEligibility(opts.ForceRisky)
	return plan
}

func WithValidatedAIPatch(plan Plan, patch string, confidence int) Plan {
	patch = strings.TrimSpace(patch)
	if patch == "" {
		return plan
	}
	plan.PatchTemplate = patch
	plan.CanApply = true
	plan.Safe = true
	plan.Risk = "low"
	plan.Confidence = max(plan.Confidence, max(confidence, 90))
	plan.BlockedReasons = nil
	plan.Guardrails = appendUnique(plan.Guardrails, "ai-patch-safety-validated")
	plan.Warnings = appendUnique(plan.Warnings, "AI generated this patch from redacted evidence; review the diff before shadow verification or apply.")
	if len(plan.Patches) == 0 {
		plan.Patches = append(plan.Patches, Patch{Type: "strategic-merge", Target: plan.Resource, Description: "AI-generated Kubernetes remediation patch.", Preview: patch})
	} else {
		plan.Patches[0].Preview = patch
		if plan.Patches[0].Description == "" {
			plan.Patches[0].Description = "AI-generated Kubernetes remediation patch."
		}
	}
	plan.refreshApplyEligibility(false)
	return plan
}

func WithReviewOnlyAIPatch(plan Plan, patch, reason string) Plan {
	patch = strings.TrimSpace(patch)
	if patch == "" {
		return plan
	}
	plan.PatchTemplate = patch
	plan.CanApply = false
	plan.ApplyEligible = false
	plan.Safe = false
	plan.Risk = "review-only"
	plan.BlockedReasons = appendUnique(plan.BlockedReasons, firstNonEmpty(reason, "AI patch requires human source review before production delivery."))
	plan.Guardrails = appendUnique(plan.Guardrails, "ai-patch-review-only")
	plan.Warnings = appendUnique(plan.Warnings, "AI generated a review-only patch for a non-shadow-verifiable resource; use GitOps/source review before production.")
	if len(plan.Patches) == 0 {
		plan.Patches = append(plan.Patches, Patch{Type: "review-only", Target: plan.Resource, Description: "AI-generated review-only Kubernetes remediation patch.", Preview: patch})
	} else {
		plan.Patches[0].Type = "review-only"
		plan.Patches[0].Preview = patch
		if plan.Patches[0].Description == "" {
			plan.Patches[0].Description = "AI-generated review-only Kubernetes remediation patch."
		}
	}
	return plan
}

func validateConcretePatch(plan Plan) ([]string, []string) {
	strategy := strings.ToLower(strings.TrimSpace(plan.Strategy))
	patch := strings.ToLower(plan.PatchTemplate)
	switch strategy {
	case "image", "fix-architecture":
		if !strings.Contains(patch, "image:") {
			return nil, []string{"image strategy requires a concrete image field"}
		}
		return nil, nil
	case "env":
		if !strings.Contains(patch, "configmapkeyref") && !strings.Contains(patch, "secretkeyref") && !strings.Contains(patch, "env:") {
			return nil, []string{"env strategy requires concrete env or valueFrom evidence"}
		}
		return nil, nil
	case "resources":
		if !strings.Contains(patch, "resources:") || (!strings.Contains(patch, "requests:") && !strings.Contains(patch, "limits:")) {
			return nil, []string{"resources strategy requires concrete resource request or limit fields"}
		}
		return nil, nil
	case "repair-selector", "service", "webhook", "runtime", "scheduling", "pdb", "ingress", "hpa":
		return []string{"strategy " + strategy + " remains review-only and is not auto-applied"}, []string{"strategy " + strategy + " is not apply-eligible by default"}
	default:
		return nil, []string{"unknown strategy " + strategy + " is review-only"}
	}
}

func (p *Plan) refreshApplyEligibility(forceRisky bool) {
	p.ApplyEligible = p.CanApply && p.Confidence >= 90 && len(p.BlockedReasons) == 0 && (p.Safe || forceRisky)
	if p.CanApply && !p.ApplyEligible {
		if p.Confidence < 90 {
			p.BlockedReasons = appendUnique(p.BlockedReasons, "confidence below production apply threshold 90")
		}
		if !p.Safe && !forceRisky {
			p.BlockedReasons = appendUnique(p.BlockedReasons, "risky fix requires --force-risky")
		}
	}
}

func (p Plan) DiffView() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Fixora remediation plan\n")
	fmt.Fprintf(&b, "resource: %s\nnamespace: %s\nstatus: %s\nstrategy: %s\nsafe: %t\ncanApply: %t\napplyEligible: %t\nconfidence: %d\nrisk: %s\n\n", p.Resource, p.Namespace, p.Status, p.Strategy, p.Safe, p.CanApply, p.ApplyEligible, p.Confidence, p.Risk)
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
	if len(p.Verification) > 0 {
		fmt.Fprintf(&b, "\nverify:\n")
		for _, command := range p.Verification {
			fmt.Fprintf(&b, "- %s\n", command)
		}
	}
	fmt.Fprintf(&b, "\n--- suggested patch template ---\n%s", p.PatchTemplate)
	return b.String()
}

func (p Plan) PatchYAML() string {
	return p.PatchTemplate
}

func imagePatchTemplate(f analyzer.Finding) string {
	return workloadPatchTemplate(f, `containers:
- name: TODO_CONTAINER_NAME
  image: TODO_PINNED_MULTI_ARCH_IMAGE
`)
}

func resourcesPatchTemplate(f analyzer.Finding) string {
	return workloadPatchTemplate(f, `containers:
- name: TODO_CONTAINER_NAME
  resources:
    requests:
      memory: TODO_OBSERVED_REQUEST
      cpu: TODO_OBSERVED_CPU_REQUEST
    limits:
      memory: TODO_SAFE_LIMIT
`)
}

func runtimePatchTemplate(f analyzer.Finding) string {
	return workloadPatchTemplate(f, `containers:
- name: TODO_CONTAINER_NAME
  # Add only the proven command, args, probe, env, or securityContext change.
`)
}

func envPatchTemplate(f analyzer.Finding) string {
	return workloadPatchTemplate(f, `containers:
- name: TODO_CONTAINER_NAME
  env:
  - name: TODO_ENV_NAME
    valueFrom:
      configMapKeyRef:
        name: TODO_CONFIGMAP
        key: TODO_KEY
`)
}

func serviceSelectorPatchTemplate(f analyzer.Finding) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
spec:
  selector:
    app.kubernetes.io/name: TODO_PROVEN_BACKEND_LABEL
`, f.ResourceName, f.Namespace)
}

func hpaRequestsPatchTemplate(f analyzer.Finding) string {
	return fmt.Sprintf(`# Patch the HPA scale target workload, not the HPA object itself.
# HPA: %s/%s in namespace %s
# Add requests to every scaled container:
resources:
  requests:
    cpu: TODO_OBSERVED_CPU_REQUEST
    memory: TODO_OBSERVED_REQUEST
`, f.ResourceKind, f.ResourceName, f.Namespace)
}

func pdbPatchTemplate(f analyzer.Finding) string {
	return fmt.Sprintf(`apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: %s
  namespace: %s
spec:
  # Review selector first. Choose exactly one policy after confirming availability.
  maxUnavailable: TODO_SAFE_MAX_UNAVAILABLE
`, f.ResourceName, f.Namespace)
}

func webhookPatchTemplate(f analyzer.Finding) string {
	return fmt.Sprintf(`# Temporary emergency patch for %s/%s.
# Prefer restoring the webhook Service first.
failurePolicy: Ignore
timeoutSeconds: 5
`, f.ResourceKind, f.ResourceName)
}

func genericPatchTemplate(f analyzer.Finding) string {
	return fmt.Sprintf(`# Fixora could not infer a concrete patch.
# Resource: %s/%s
# Namespace: %s
# Status: %s
# Add a source-controlled manifest, Helm values, or Kustomize patch after reviewing evidence.
`, f.ResourceKind, f.ResourceName, f.Namespace, f.Status)
}

func archPatchTemplate(f analyzer.Finding) string {
	return imagePatchTemplate(f)
}

func securityContextPatchTemplate(f analyzer.Finding) string {
	// fsGroup is a pod-level field (sibling of containers), which is the
	// canonical fix for volume-write PermissionDenied. runAsNonRoot is left
	// unset rather than hardcoded false, which would push workloads toward root.
	return workloadPatchTemplate(f, `securityContext:
  fsGroup: TODO_VOLUME_GROUP_ID
containers:
- name: TODO_CONTAINER_NAME
  securityContext:
    runAsUser: TODO_UID
`)
}

func workloadPatchTemplate(f analyzer.Finding, podSpecPatch string) string {
	kind := normalizeKind(f.ResourceKind)
	switch kind {
	case "Pod":
		return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
spec:
%s`, f.ResourceName, f.Namespace, indent(podSpecPatch, 2))
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
		return fmt.Sprintf(`apiVersion: apps/v1
kind: %s
metadata:
  name: %s
  namespace: %s
spec:
  template:
    spec:
%s`, kind, f.ResourceName, f.Namespace, indent(podSpecPatch, 6))
	case "Job":
		return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
spec:
  template:
    spec:
%s`, f.ResourceName, f.Namespace, indent(podSpecPatch, 6))
	case "CronJob":
		return fmt.Sprintf(`apiVersion: batch/v1
kind: CronJob
metadata:
  name: %s
  namespace: %s
spec:
  jobTemplate:
    spec:
      template:
        spec:
%s`, f.ResourceName, f.Namespace, indent(podSpecPatch, 10))
	default:
		return fmt.Sprintf(`# Fixora does not know how to safely patch pod template fields for %s/%s.
# Add this pod spec change in the source manifest, Helm values, or Kustomize overlay:
%s`, f.ResourceKind, f.ResourceName, indent(podSpecPatch, 0))
	}
}

func indent(value string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(strings.TrimRight(value, "\n"), "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n") + "\n"
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

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueMany(values []string, next ...string) []string {
	for _, value := range next {
		values = appendUnique(values, value)
	}
	return values
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
