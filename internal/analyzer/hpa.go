package analyzer

import (
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func (a Analyzer) analyzeHPATargets(ctx *ScanContext) ([]Finding, error) {
	hpas, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "hpa")
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for _, hpa := range hpas {
		namespace, name := objectNamespaceName(hpa)
		spec := nestedMap(hpa, "spec")
		out = append(out, hpaStatusFindings(hpa, namespace, name)...)
		target := nestedMap(spec, "scaleTargetRef")
		targetKind, targetName := strValue(target["kind"]), strValue(target["name"])
		if targetKind == "" || targetName == "" {
			out = append(out, Finding{
				ID:           keyFor(namespace, "HPA/"+name+"/MissingTargetRef"),
				Namespace:    namespace,
				ResourceKind: "HorizontalPodAutoscaler",
				ResourceName: name,
				Status:       "MissingScaleTargetRef",
				Severity:     "high",
				Category:     "autoscaling",
				Summary:      "HPA does not have a complete scaleTargetRef.",
				Evidence:     []Evidence{{Label: "scaleTargetRef", Value: compactMap(target)}},
				GitOps:       gitOpsForObject(hpa),
				Recommendations: []Recommendation{{
					Title:         "Set a valid scale target",
					Description:   "Point the HPA at an existing scalable workload such as Deployment, StatefulSet, or ReplicaSet.",
					PatchType:     "hpa",
					SafeByDefault: false,
				}},
			})
			continue
		}
		if !supportedHPAScaleTarget(targetKind) {
			out = append(out, Finding{
				ID:           keyFor(namespace, "HPA/"+name+"/UnsupportedScaleTarget/"+targetKind),
				Namespace:    namespace,
				ResourceKind: "HorizontalPodAutoscaler",
				ResourceName: name,
				Status:       "UnsupportedScaleTarget",
				Severity:     "medium",
				Category:     "autoscaling",
				Summary:      "HPA target kind is outside Fixora's verified workload support.",
				Evidence:     []Evidence{{Label: "Target", Value: targetKind + "/" + targetName}},
				GitOps:       gitOpsForObject(hpa),
				Recommendations: []Recommendation{{
					Title:         "Review the HPA target controller",
					Description:   "Confirm that the target exposes a working scale subresource and that its controller supports the configured metrics.",
					PatchType:     "hpa",
					SafeByDefault: false,
				}},
			})
		}
		targetResource := strings.ToLower(targetKind) + "/" + targetName
		targetObj, targetErr := ctx.GetResource(namespace, targetResource)
		if targetErr != nil {
			missing := hpaTargetNotFound(targetErr, targetKind, targetName)
			status, severity, summary := "ScaleTargetUnreadable", "medium", "HPA scale target could not be verified because its read failed."
			title, description := "Resolve scale target read error", "Check RBAC and API availability before concluding the HPA target is missing."
			if missing {
				status, severity, summary = "MissingScaleTarget", "high", "HPA references a scale target that does not exist."
				title, description = "Fix or restore the autoscale target", "Confirm the target kind, name, namespace, and API availability before the HPA is allowed to drive scaling decisions."
			}
			out = append(out, Finding{
				ID:           keyFor(namespace, "HPA/"+name+"/"+status+"/"+targetKind+"/"+targetName),
				Namespace:    namespace,
				ResourceKind: "HorizontalPodAutoscaler",
				ResourceName: name,
				Status:       status,
				Severity:     severity,
				Category:     "autoscaling",
				Summary:      summary,
				Evidence: []Evidence{
					{Label: "Target", Value: targetKind + "/" + targetName},
					{Label: "Error", Value: targetErr.Error()},
				},
				GitOps: gitOpsForObject(hpa),
				Recommendations: []Recommendation{{
					Title:         title,
					Description:   description,
					PatchType:     "hpa",
					SafeByDefault: false,
				}},
			})
			continue
		}
		for _, metric := range hpaResourceUtilizationMetricNames(spec) {
			missing := containersMissingResourceRequest(targetObj, metric)
			if len(missing) == 0 {
				continue
			}
			out = append(out, Finding{
				ID:           keyFor(namespace, "HPA/"+name+"/MissingRequests/"+metric),
				Namespace:    namespace,
				ResourceKind: "HorizontalPodAutoscaler",
				ResourceName: name,
				Status:       "MissingResourceRequests",
				Severity:     "medium",
				Category:     "autoscaling",
				Summary:      "HPA uses a resource metric but target containers are missing matching resource requests.",
				Evidence: []Evidence{
					{Label: "Metric", Value: metric},
					{Label: "Containers", Value: strings.Join(missing, ", ")},
				},
				GitOps: gitOpsForObject(hpa),
				Recommendations: []Recommendation{{
					Title:         "Add resource requests before relying on HPA",
					Description:   "Set realistic container requests in the workload source so utilization-based scaling has stable inputs.",
					PatchType:     "resources",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}

func hpaStatusFindings(hpa map[string]any, namespace, name string) []Finding {
	metadata := nestedMap(hpa, "metadata")
	status := nestedMap(hpa, "status")
	out := []Finding{}
	generation := intValue(metadata["generation"])
	observedGeneration := intValue(status["observedGeneration"])
	if generation > 0 && observedGeneration < generation {
		out = append(out, hpaStatusFinding(hpa, namespace, name, "HPAStatusStale", "medium", "HPA status has not observed the current specification.", []Evidence{
			{Label: "Generation", Value: fmt.Sprint(generation)},
			{Label: "Observed generation", Value: fmt.Sprint(observedGeneration)},
		}))
	}
	for _, raw := range nestedSlice(status, "conditions") {
		condition, _ := raw.(map[string]any)
		if strValue(condition["status"]) != "False" {
			continue
		}
		conditionType := strValue(condition["type"])
		reason := strValue(condition["reason"])
		code, severity, summary := "", "medium", "HPA reports an unhealthy condition."
		switch {
		case conditionType == "ScalingActive" && (strings.HasPrefix(reason, "FailedGet") || strings.HasPrefix(reason, "FailedCompute")):
			code, summary = "MetricsUnavailable", "HPA could not read or calculate a configured metric."
		case conditionType == "ScalingActive" && (reason == "InvalidMetricSource" || reason == "InvalidMetricTarget"):
			code, severity, summary = "MetricTargetInvalid", "high", "HPA cannot use its configured metric source or target."
		case conditionType == "AbleToScale":
			code, summary = "ScaleTargetUnreadable", "HPA cannot read or update its scale target."
		default:
			continue
		}
		out = append(out, hpaStatusFinding(hpa, namespace, name, code, severity, summary, []Evidence{
			{Label: "Condition", Value: conditionType + "=False"},
			{Label: "Reason", Value: reason},
			{Label: "Message", Value: trim(strValue(condition["message"]), 320)},
			{Label: "Current metrics", Value: trim(fmt.Sprint(status["currentMetrics"]), 320)},
		}))
	}
	for _, metric := range nestedSlice(nestedMap(hpa, "spec"), "metrics") {
		metricMap, _ := metric.(map[string]any)
		if strValue(metricMap["type"]) != "Resource" {
			continue
		}
		resource := nestedMap(metricMap, "resource")
		target := nestedMap(resource, "target")
		targetType := strValue(target["type"])
		if strValue(resource["name"]) == "" || (targetType != "Utilization" && targetType != "AverageValue") {
			out = append(out, hpaStatusFinding(hpa, namespace, name, "MetricTargetInvalid", "high", "HPA resource metric is missing a supported target type or resource name.", []Evidence{
				{Label: "Metric", Value: compactMap(metricMap)},
			}))
		}
	}
	if len(nestedSlice(nestedMap(hpa, "spec"), "metrics")) == 0 && hpa["apiVersion"] == "autoscaling/v2" {
		out = append(out, hpaStatusFinding(hpa, namespace, name, "InvalidHPASpec", "high", "autoscaling/v2 HPA has no metric sources.", nil))
	}
	minReplicas := 1
	if value, ok := nestedMap(hpa, "spec")["minReplicas"]; ok {
		minReplicas = intValue(value)
	}
	maxReplicas := intValue(nestedMap(hpa, "spec")["maxReplicas"])
	if minReplicas < 0 || maxReplicas <= 0 || minReplicas > maxReplicas {
		out = append(out, hpaStatusFinding(hpa, namespace, name, "InvalidHPASpec", "high", "HPA replica bounds are invalid.", []Evidence{
			{Label: "minReplicas", Value: fmt.Sprint(minReplicas)},
			{Label: "maxReplicas", Value: fmt.Sprint(maxReplicas)},
		}))
	}
	return out
}

func hpaStatusFinding(hpa map[string]any, namespace, name, status, severity, summary string, evidence []Evidence) Finding {
	return Finding{
		ID:           keyFor(namespace, "HPA/"+name+"/"+status),
		ResourceUID:  strValue(nestedMap(hpa, "metadata")["uid"]),
		Namespace:    namespace,
		ResourceKind: "HorizontalPodAutoscaler",
		ResourceName: name,
		Status:       status,
		Severity:     severity,
		Category:     "autoscaling",
		Summary:      summary,
		Evidence:     evidence,
		GitOps:       gitOpsForObject(hpa),
		Recommendations: []Recommendation{{
			Title:         "Review the HPA condition and metric configuration",
			Description:   "Confirm metric availability, target health, and the approved scaling intent before changing the autoscaler or workload.",
			PatchType:     "hpa",
			SafeByDefault: false,
		}},
	}
}

func hpaResourceUtilizationMetricNames(spec map[string]any) []string {
	seen := map[string]bool{}
	for _, metric := range nestedSlice(spec, "metrics") {
		metricMap, _ := metric.(map[string]any)
		if strValue(metricMap["type"]) != "Resource" {
			continue
		}
		resource := nestedMap(metricMap, "resource")
		target := nestedMap(resource, "target")
		if strValue(target["type"]) == "Utilization" {
			if name := strValue(resource["name"]); name != "" {
				seen[name] = true
			}
		}
	}
	if len(seen) == 0 && spec["targetCPUUtilizationPercentage"] != nil {
		seen["cpu"] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func supportedHPAScaleTarget(kind string) bool {
	switch strings.ToLower(kind) {
	case "deployment", "statefulset", "replicaset":
		return true
	default:
		return false
	}
}

func hpaTargetNotFound(err error, kind, name string) bool {
	if apierrors.IsNotFound(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "(notfound)") &&
		strings.Contains(message, strings.ToLower(kind)) &&
		strings.Contains(message, `"`+strings.ToLower(name)+`" not found`)
}
