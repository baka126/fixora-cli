package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// withHPAAnalysisContext adds bounded, non-secret autoscaler and workload
// evidence. It deliberately omits request policy values and all env/secret data.
func withHPAAnalysisContext(ctx context.Context, reader kube.Reader, finding analyzer.Finding) analyzer.Finding {
	if !strings.EqualFold(finding.ResourceKind, "HorizontalPodAutoscaler") || reader == nil {
		return finding
	}
	hpa, err := reader.GetResource(ctx, finding.Namespace, "hpa/"+finding.ResourceName)
	if err != nil {
		finding.Evidence = append(finding.Evidence, analyzer.Evidence{Label: "HPA context", Value: "HPA object could not be read for AI analysis."})
		return finding
	}
	spec := aiMap(hpa["spec"])
	ref := aiMap(spec["scaleTargetRef"])
	status := aiMap(hpa["status"])
	kind, name := fmt.Sprint(ref["kind"]), fmt.Sprint(ref["name"])
	data := map[string]any{"hpa": map[string]any{"name": finding.ResourceName, "spec": map[string]any{"scaleTargetRef": map[string]any{"apiVersion": ref["apiVersion"], "kind": ref["kind"], "name": ref["name"]}, "minReplicas": spec["minReplicas"], "maxReplicas": spec["maxReplicas"], "metrics": safeHPASpecMetrics(spec["metrics"])}, "status": map[string]any{"observedGeneration": status["observedGeneration"], "currentReplicas": status["currentReplicas"], "desiredReplicas": status["desiredReplicas"], "conditions": safeHPAConditions(status["conditions"]), "currentMetrics": safeHPACurrentMetrics(status["currentMetrics"])}}}
	if name != "" && (strings.EqualFold(kind, "Deployment") || strings.EqualFold(kind, "StatefulSet")) {
		workload, e := reader.GetResource(ctx, finding.Namespace, strings.ToLower(kind)+"/"+name)
		if e == nil {
			ws := aiMap(workload["spec"])
			template := aiMap(aiMap(ws["template"])["spec"])
			containers := []map[string]any{}
			if raw, ok := template["containers"].([]any); ok {
				for _, item := range raw {
					c := aiMap(item)
					containers = append(containers, map[string]any{"name": c["name"], "image": c["image"], "resources": c["resources"]})
				}
			}
			status := aiMap(workload["status"])
			data["target"] = map[string]any{"kind": workload["kind"], "name": name, "generation": aiMap(workload["metadata"])["generation"], "replicas": ws["replicas"], "status": map[string]any{"observedGeneration": status["observedGeneration"], "replicas": status["replicas"], "updatedReplicas": status["updatedReplicas"], "readyReplicas": status["readyReplicas"], "availableReplicas": status["availableReplicas"]}, "containers": containers}
		}
	}
	encoded, _ := json.Marshal(data)
	value := string(encoded)
	if len(value) > 2400 {
		value = value[:2397] + "..."
	}
	finding.Evidence = append(finding.Evidence, analyzer.Evidence{Label: "HPA analysis context", Value: value})
	return finding
}

func aiMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func safeHPAConditions(v any) []map[string]any {
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		c := aiMap(item)
		out = append(out, map[string]any{"type": c["type"], "status": c["status"], "reason": c["reason"], "message": briefHPA(fmt.Sprint(c["message"]))})
	}
	return out
}

func safeHPASpecMetrics(v any) []map[string]any {
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		metric := aiMap(item)
		entry := map[string]any{"type": metric["type"]}
		resource := aiMap(metric["resource"])
		if len(resource) > 0 {
			target := aiMap(resource["target"])
			entry["resource"] = map[string]any{"name": resource["name"], "target": map[string]any{"type": target["type"], "averageUtilization": target["averageUtilization"], "averageValue": target["averageValue"]}}
		}
		external := aiMap(metric["external"])
		if len(external) > 0 {
			entry["externalMetricName"] = aiMap(external["metric"])["name"]
		}
		out = append(out, entry)
		if len(out) == 8 {
			break
		}
	}
	return out
}
func safeHPACurrentMetrics(v any) []map[string]any {
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		metric := aiMap(item)
		resource := aiMap(metric["resource"])
		if len(resource) == 0 {
			out = append(out, map[string]any{"type": metric["type"]})
			continue
		}
		current := aiMap(resource["current"])
		out = append(out, map[string]any{"type": metric["type"], "resource": map[string]any{"name": resource["name"], "averageUtilization": current["averageUtilization"], "averageValue": current["averageValue"]}})
		if len(out) == 8 {
			break
		}
	}
	return out
}

// selectAIRepair evaluates a model candidate against the live target, even when
// deterministic inference already supplied a candidate. Model claims about
// strategy or confidence never establish workload identity or verification.
func selectAIRepair(ctx context.Context, reader kube.Reader, fallback fix.Plan, f analyzer.Finding, stderr io.Writer, verbose bool) fix.Plan {
	if f.AI == nil || f.Recovered || fallback.Recovered || f.Status == "Healthy" || f.Status == "OwnedPodsHealthy" || fallback.Strategy == "restore-command" || fallback.PolicyAuthority != nil {
		return fallback
	}
	patch := aiPatchCandidate(*f.AI)
	if patch == "" {
		return aiReviewWithoutPatch(fallback, "AI did not provide a workload-specific patch; review the diagnosis before choosing a repair.")
	}
	reject := func(err error) fix.Plan {
		fallback = aiReviewWithoutPatch(fallback, "AI candidate rejected; review the diagnosis before choosing a repair: "+err.Error())
		if verbose {
			fmt.Fprintf(stderr, "ai candidate rejected: %v\n", err)
		}
		return fallback
	}
	if strings.Contains(patch, "TODO_") {
		return reject(fmt.Errorf("patch contains unresolved placeholders"))
	}
	if reader == nil {
		return reject(fmt.Errorf("live target configuration is unavailable"))
	}
	live, err := reader.GetResource(ctx, f.Namespace, strings.ToLower(f.ResourceKind)+"/"+f.ResourceName)
	if err != nil {
		return reject(fmt.Errorf("read live target: %w", err))
	}
	meta, _ := live["metadata"].(map[string]any)
	if meta["name"] != f.ResourceName || meta["namespace"] != f.Namespace || !strings.EqualFold(fmt.Sprint(live["kind"]), f.ResourceKind) || fmt.Sprint(live["apiVersion"]) == "" || live["apiVersion"] == nil {
		return reject(fmt.Errorf("live resource identity does not match the finding"))
	}
	identity, err := json.Marshal(map[string]any{"apiVersion": live["apiVersion"], "kind": live["kind"], "metadata": map[string]any{"name": f.ResourceName, "namespace": f.Namespace}})
	if err != nil {
		return reject(err)
	}
	bound, err := bindAIPatchIdentity(string(identity), patch)
	if err != nil {
		return reject(err)
	}
	patch = bound
	data, err := yaml.ToJSON([]byte(patch))
	if err != nil {
		return reject(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return reject(err)
	}
	if err := validateAILiveTarget(live, obj, f); err != nil {
		return reject(err)
	}
	strategy := aiCandidateStrategy(obj)
	candidate := fallback
	candidate.Guardrails = append([]string{}, fallback.Guardrails...)
	candidate.Warnings = append([]string{}, fallback.Warnings...)
	candidate.Patches = append([]fix.Patch{}, fallback.Patches...)
	review := func(reason string) fix.Plan {
		if err := validateReviewOnlyAIPatch(patch); err != nil {
			return reject(err)
		}
		if aiPodSpec(obj) == nil {
			sourcePlan := candidate
			sourcePlan.PatchTemplate = string(identity)
			sourcePlan.CanApply, sourcePlan.ApplyEligible = false, false
			result := applyAIPatchIfSafe(ctx, sourcePlan, f, stderr, verbose)
			if result.PatchYAML() == string(identity) {
				return reject(fmt.Errorf("source proposal failed safety validation"))
			}
			return result
		}
		return fix.WithReviewOnlyAIPatch(candidate, patch, reason)
	}

	if aiPodSpec(obj) == nil {
		return review("AI source proposal requires review before delivery.")
	}
	if strategy == "" {
		return review("AI proposal changes fields outside the automated verification policy; review intended behavior and source before delivery.")
	}
	candidate.Strategy = strategy
	if err := shadow.ValidateCandidatePatch(patch, strategy, candidate.Resource, candidate.Namespace); err != nil {
		return review("AI proposal requires source review: " + err.Error())
	}
	if strategy == "filesystem" {
		if err := validateAILiveFilesystem(live, obj); err != nil {
			return review(err.Error())
		}
	}
	if strategy == "env" {
		if err := validateAIConfigReferences(ctx, reader, obj, f.Namespace); err != nil {
			return reject(err)
		}
	}
	if strategy == "image" {
		if err := verifyArchitecturePatch(ctx, f, patch); err != nil {
			return reject(err)
		}
	}
	candidate = fix.WithValidatedAIPatch(candidate, patch, f.AI.Confidence)
	candidate.Confidence = f.AI.Confidence
	candidate.Recovered = f.Recovered || fallback.Recovered
	candidate.ApplyEligible = candidate.CanApply && candidate.Confidence >= 90 && candidate.Confidence <= 100 && !candidate.Recovered
	candidate.Guardrails = appendUniqueString(candidate.Guardrails, "shadow-required")
	candidate.Steps = []string{"Review the AI diagnosis against the collected workload evidence.", "Verify the proposed repair in an isolated shadow workload before delivery."}
	if !candidate.ApplyEligible {
		candidate.BlockedReasons = appendUniqueString(candidate.BlockedReasons, "AI confidence is below the automatic verification threshold; review this proposal.")
	}
	candidate = fix.WithTargetPolicy(candidate)
	if sourceManaged(f) {
		return fix.WithReviewOnlyAIPatch(candidate, patch, fix.SourceOwnershipReviewReason)
	}
	return candidate
}

func aiReviewWithoutPatch(plan fix.Plan, reason string) fix.Plan {
	plan.Patches = nil
	plan.PatchTemplate = ""
	plan.Commands = nil
	plan.CanApply = false
	plan.ApplyEligible = false
	plan.Safe = false
	plan.Risk = "needs-review"
	plan.Strategy = "review"
	plan.BlockedReasons = appendUniqueString(plan.BlockedReasons, reason)
	plan.Warnings = appendUniqueString(plan.Warnings, reason)
	return plan
}

func aiPodSpec(obj map[string]any) map[string]any {
	spec, _ := kube.WorkloadPodSpec(obj)
	return spec
}

func validateAILiveTarget(live, patch map[string]any, f analyzer.Finding) error {
	meta, _ := live["metadata"].(map[string]any)
	if !strings.EqualFold(fmt.Sprint(live["kind"]), f.ResourceKind) || meta["name"] != f.ResourceName || meta["namespace"] != f.Namespace || live["apiVersion"] != patch["apiVersion"] {
		return fmt.Errorf("live resource identity does not match the proposal")
	}
	if aiPodSpec(patch) == nil {
		return nil
	} // Non-workload source proposals remain review-only.
	current := aiPodSpec(live)
	for _, section := range []string{"containers", "initContainers"} {
		names := map[string]bool{}
		values, _ := current[section].([]any)
		for _, v := range values {
			c, _ := v.(map[string]any)
			names[fmt.Sprint(c["name"])] = true
		}
		proposed, _ := aiPodSpec(patch)[section].([]any)
		seen := map[string]bool{}
		for _, v := range proposed {
			c, ok := v.(map[string]any)
			name, _ := c["name"].(string)
			if !ok || name == "" || !names[name] || seen[name] {
				return fmt.Errorf("proposed %s entry is missing, duplicate, or absent from the live workload", section)
			}
			seen[name] = true
		}
	}
	return nil
}

// Classify the fields, not the model's strategy or the initial symptom. Mixed
// families remain review-only until their combined effects are verifiable.
func aiCandidateStrategy(obj map[string]any) string {
	spec := aiPodSpec(obj)
	families := map[string]bool{}
	for key := range spec {
		if key != "containers" && key != "initContainers" {
			if key == "volumes" || key == "securityContext" {
				families["filesystem"] = true
			} else {
				return ""
			}
		}
	}
	for _, section := range []string{"containers", "initContainers"} {
		containers, _ := spec[section].([]any)
		for _, v := range containers {
			c, _ := v.(map[string]any)
			for key := range c {
				switch key {
				case "name":
				case "image":
					families["image"] = true
				case "resources":
					families["resources"] = true
				case "env":
					families["env"] = true
				case "readinessProbe", "livenessProbe", "startupProbe":
					families["probe"] = true
				case "volumeMounts":
					families["filesystem"] = true
				default:
					return ""
				}
			}
		}
	}
	if len(families) != 1 {
		return ""
	}
	for family := range families {
		return family
	}
	return ""
}

func validateAIConfigReferences(ctx context.Context, reader kube.Reader, obj map[string]any, namespace string) error {
	for _, section := range []string{"containers", "initContainers"} {
		containers, _ := aiPodSpec(obj)[section].([]any)
		for _, v := range containers {
			c, _ := v.(map[string]any)
			envs, _ := c["env"].([]any)
			for _, v := range envs {
				env, _ := v.(map[string]any)
				source, _ := env["valueFrom"].(map[string]any)
				ref, _ := source["configMapKeyRef"].(map[string]any)
				name, _ := ref["name"].(string)
				key, _ := ref["key"].(string)
				cm, err := reader.GetResource(ctx, namespace, "configmap/"+name)
				if err != nil {
					return fmt.Errorf("referenced ConfigMap %s is unavailable", name)
				}
				data, _ := cm["data"].(map[string]any)
				if _, ok := data[key]; !ok {
					return fmt.Errorf("referenced ConfigMap %s lacks key %s", name, key)
				}
			}
		}
	}
	return nil
}

func validateAILiveFilesystem(live, proposed map[string]any) error {
	liveSpec, proposedSpec := aiPodSpec(live), aiPodSpec(proposed)
	knownVolumes := map[string]bool{}
	for _, value := range aiSliceMaps(liveSpec["volumes"]) {
		knownVolumes[fmt.Sprint(value["name"])] = true
	}
	for _, value := range aiSliceMaps(proposedSpec["volumes"]) {
		name := fmt.Sprint(value["name"])
		if knownVolumes[name] {
			return fmt.Errorf("filesystem proposal reuses existing volume %s", name)
		}
	}
	if proposedContext, ok := proposedSpec["securityContext"].(map[string]any); ok {
		if liveContext, ok := liveSpec["securityContext"].(map[string]any); ok && liveContext["fsGroup"] != nil && fmt.Sprint(liveContext["fsGroup"]) != fmt.Sprint(proposedContext["fsGroup"]) {
			return fmt.Errorf("filesystem proposal changes the existing fsGroup")
		}
	}
	for _, section := range []string{"containers", "initContainers"} {
		liveByName := map[string]map[string]any{}
		for _, container := range aiSliceMaps(liveSpec[section]) {
			liveByName[fmt.Sprint(container["name"])] = container
		}
		for _, proposedContainer := range aiSliceMaps(proposedSpec[section]) {
			name := fmt.Sprint(proposedContainer["name"])
			current, ok := liveByName[name]
			if !ok {
				continue // validateAILiveTarget reports unknown names first.
			}
			mountNames, mountPaths := map[string]bool{}, map[string]bool{}
			for _, mount := range aiSliceMaps(current["volumeMounts"]) {
				mountNames[fmt.Sprint(mount["name"])] = true
				mountPaths[fmt.Sprint(mount["mountPath"])] = true
			}
			for _, mount := range aiSliceMaps(proposedContainer["volumeMounts"]) {
				mountName, mountPath := fmt.Sprint(mount["name"]), fmt.Sprint(mount["mountPath"])
				if mountNames[mountName] || mountPaths[mountPath] {
					return fmt.Errorf("filesystem proposal collides with an existing mount in %s", name)
				}
			}
		}
	}
	return nil
}

func aiSliceMaps(value any) []map[string]any {
	values, _ := value.([]any)
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			result = append(result, item)
		}
	}
	return result
}

// Supply the missing live field context without exporting literal environment
// values, Secret contents, annotations, or unrelated namespace workloads.
func withAIWorkloadContext(ctx context.Context, reader kube.Reader, f analyzer.Finding) analyzer.Finding {
	if reader == nil {
		return f
	}
	live, err := reader.GetResource(ctx, f.Namespace, strings.ToLower(f.ResourceKind)+"/"+f.ResourceName)
	if err != nil {
		return f
	}
	spec := aiPodSpec(live)
	if spec == nil {
		return f
	}
	projected := map[string]any{}
	for _, key := range []string{"volumes", "securityContext"} {
		if value, ok := spec[key]; ok {
			projected[key] = value
		}
	}
	for _, section := range []string{"containers", "initContainers"} {
		containers, _ := spec[section].([]any)
		out := []any{}
		for _, v := range containers {
			c, _ := v.(map[string]any)
			view := map[string]any{}
			for _, key := range []string{"name", "image", "command", "args", "resources", "ports", "readinessProbe", "livenessProbe", "startupProbe", "securityContext", "volumeMounts", "envFrom"} {
				if value, ok := c[key]; ok {
					view[key] = value
				}
			}
			envs, _ := c["env"].([]any)
			envOut := []any{}
			for _, v := range envs {
				env, _ := v.(map[string]any)
				item := map[string]any{"name": env["name"]}
				if source, ok := env["valueFrom"]; ok {
					item["valueFrom"] = source
				} else {
					item["value"] = "<literal omitted>"
				}
				envOut = append(envOut, item)
			}
			if len(envOut) > 0 {
				view["env"] = envOut
			}
			out = append(out, view)
		}
		if len(out) > 0 {
			projected[section] = out
		}
	}
	encoded, err := json.Marshal(projected)
	if err != nil || len(encoded) > 24*1024 {
		return f
	}
	f.Evidence = append([]analyzer.Evidence{{Label: "Live workload configuration", Value: string(encoded)}}, f.Evidence...)
	return f
}
