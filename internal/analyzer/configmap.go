package analyzer

import (
	"fmt"
)

func (a Analyzer) analyzeConfigMaps(ctx *ScanContext) ([]Finding, error) {
	configMaps, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "configmaps")
	if err != nil {
		return nil, err
	}
	pods, podErr := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "pods")
	used := configMapUsageByNamespace(pods)

	out := []Finding{}
	for _, cm := range configMaps {
		namespace, name := objectNamespaceName(cm)
		labels, annotations := objectLabelsAnnotations(cm)
		if configMapUsageCheckSkipped(labels, annotations) {
			continue
		}
		if !used[keyFor(namespace, name)] {
			out = append(out, configMapFinding(cm, "Unused", "low", "ConfigMap is not referenced by any observable pod in the namespace.", fmt.Sprintf("No pod volume, envFrom, or env key reference was found for %s.", keyFor(namespace, name))))
		}
		if configMapSize(cm) == 0 {
			out = append(out, configMapFinding(cm, "Empty", "low", "ConfigMap has no data or binaryData entries.", "Empty ConfigMaps are often stale, misgenerated, or missing source data."))
		}
		if size := configMapSize(cm); size > 1024*1024 {
			out = append(out, configMapFinding(cm, "Large", "medium", "ConfigMap is larger than 1MiB.", fmt.Sprintf("Approximate data size is %d bytes; large ConfigMaps can slow apiserver and kubelet reads.", size)))
		}
	}
	if podErr != nil && len(configMaps) > 0 {
		out = append(out, Finding{
			ID:           "cluster/ConfigMap/UsageCheckSkipped",
			ResourceKind: "ConfigMap",
			ResourceName: "usage",
			Status:       "PodListUnavailable",
			Severity:     "low",
			Category:     "configuration",
			Summary:      "ConfigMap usage checks could not read pods.",
			Evidence:     []Evidence{{Label: "Pod list error", Value: podErr.Error()}},
			Recommendations: []Recommendation{{
				Title:         "Grant read access before pruning ConfigMaps",
				Description:   "Fix pod list permissions or scope the scan to a namespace before acting on unused ConfigMap findings.",
				PatchType:     "configmap",
				SafeByDefault: false,
			}},
		})
	}
	return out, nil
}

func configMapFinding(cm map[string]any, status, severity, summary, evidence string) Finding {
	namespace, name := objectNamespaceName(cm)
	return Finding{
		ID:           keyFor(namespace, "ConfigMap/"+name+"/"+status),
		Namespace:    namespace,
		ResourceKind: "ConfigMap",
		ResourceName: name,
		Status:       status,
		Severity:     severity,
		Category:     "configuration",
		Summary:      summary,
		Evidence:     []Evidence{{Label: "ConfigMap", Value: evidence}},
		GitOps:       gitOpsForObject(cm),
		Recommendations: []Recommendation{{
			Title:         "Review ConfigMap ownership and usage",
			Description:   "Check GitOps ownership, mounted configuration, sidecar loaders, and rollout references before deleting or splitting this ConfigMap.",
			PatchType:     "configmap",
			SafeByDefault: false,
		}},
	}
}

func configMapUsageByNamespace(pods []map[string]any) map[string]bool {
	used := map[string]bool{}
	for _, pod := range pods {
		namespace, _ := objectNamespaceName(pod)
		spec := nestedMap(pod, "spec")
		for _, volume := range nestedSlice(spec, "volumes") {
			volumeMap, _ := volume.(map[string]any)
			if name := strValue(nestedMap(volumeMap, "configMap")["name"]); name != "" {
				used[keyFor(namespace, name)] = true
			}
		}
		for _, container := range podAllContainers(pod) {
			containerMap, _ := container.(map[string]any)
			for _, envFrom := range nestedSlice(containerMap, "envFrom") {
				envFromMap, _ := envFrom.(map[string]any)
				if name := strValue(nestedMap(envFromMap, "configMapRef")["name"]); name != "" {
					used[keyFor(namespace, name)] = true
				}
			}
			for _, env := range nestedSlice(containerMap, "env") {
				envMap, _ := env.(map[string]any)
				if name := strValue(nestedMap(nestedMap(envMap, "valueFrom"), "configMapKeyRef")["name"]); name != "" {
					used[keyFor(namespace, name)] = true
				}
			}
		}
	}
	return used
}

func configMapSize(cm map[string]any) int {
	total := 0
	for _, key := range []string{"data", "binaryData"} {
		for _, value := range nestedMap(cm, key) {
			total += len(fmt.Sprint(value))
		}
	}
	return total
}

func configMapUsageCheckSkipped(labels, annotations map[string]string) bool {
	if annotations["k8sgpt.ai/skip-usage-check"] == "true" {
		return true
	}
	for _, label := range []string{"grafana_dashboard", "grafana_datasource", "prometheus_rule", "fluentd_config"} {
		if _, ok := labels[label]; ok {
			return true
		}
	}
	return labels["k8sgpt.ai/dynamically-loaded"] == "true"
}

func podAllContainers(pod map[string]any) []any {
	spec := nestedMap(pod, "spec")
	out := append([]any{}, nestedSlice(spec, "containers")...)
	out = append(out, nestedSlice(spec, "initContainers")...)
	out = append(out, nestedSlice(spec, "ephemeralContainers")...)
	return out
}
