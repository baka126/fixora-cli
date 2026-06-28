package analyzer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ponytail: TOML skipped — not vendored.

func (a Analyzer) analyzeConfigMaps(ctx *ScanContext) ([]Finding, error) {
	configMaps, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "configmaps")
	if err != nil {
		return nil, err
	}
	pods, podErr := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "pods")
	used := configMapUsageByNamespace(pods)
	owners := configMapOwnersByNamespace(pods)

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

		// Check 1: embedded-format validation.
		for key, val := range nestedMapStrings(cm, "data") {
			var parseErr error
			switch {
			case strings.HasSuffix(key, ".json"):
				var v any
				parseErr = json.Unmarshal([]byte(val), &v)
			case strings.HasSuffix(key, ".yaml"), strings.HasSuffix(key, ".yml"):
				var v any
				parseErr = yaml.Unmarshal([]byte(val), &v)
			}
			if parseErr != nil {
				f := configMapFinding(cm, "ConfigMapInvalidFormat", "medium",
					"ConfigMap contains a key with invalid embedded format.",
					key+": "+parseErr.Error())
				f.Recommendations = []Recommendation{{
					Title:         "Fix or remove the malformed embedded config",
					Description:   "The key contains unparseable content; applications reading it at runtime will likely error.",
					PatchType:     "configmap",
					SafeByDefault: false,
				}}
				out = append(out, f)
			}
		}

		// Check 2: shared blast radius.
		key := keyFor(namespace, name)
		ownerSet := owners[key]
		if len(ownerSet) >= 2 {
			ownerList := sortedKeys(ownerSet)
			envConsumed := configMapEnvConsumedByNamespace(pods)[key]
			f := configMapFinding(cm, "ConfigMapShared", "low",
				"ConfigMap is referenced by multiple distinct workloads.",
				fmt.Sprintf("referenced by %d workloads: %s", len(ownerList), strings.Join(ownerList, ", ")))
			f.Recommendations = []Recommendation{{
				Title:         "Review shared ConfigMap change blast radius",
				Description:   "Changes to this ConfigMap affect multiple workloads simultaneously. Consider splitting per-workload or using Kustomize overlays.",
				PatchType:     "configmap",
				SafeByDefault: false,
			}}
			if envConsumed {
				for i := range f.Recommendations {
					f.Recommendations[i].Description += " env-var ConfigMap changes require a pod restart/rollout to take effect."
				}
			}
			out = append(out, f)
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

// nestedMapStrings returns the string-valued entries of a nested map key,
// skipping any non-string values (e.g. binaryData).
func nestedMapStrings(obj map[string]any, key string) map[string]string {
	raw := nestedMap(obj, key)
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// configMapOwnersByNamespace builds a map of "<ns>/<cm-name>" → set of owner
// identities (kind/name) that reference it via envFrom, env, or volume.
// Falls back to pod name when the pod has no ownerReferences.
func configMapOwnersByNamespace(pods []map[string]any) map[string]map[string]struct{} {
	owners := map[string]map[string]struct{}{}
	ensure := func(key, owner string) {
		if owners[key] == nil {
			owners[key] = map[string]struct{}{}
		}
		owners[key][owner] = struct{}{}
	}
	for _, pod := range pods {
		namespace, podName := objectNamespaceName(pod)
		ownerID := podOwnerID(pod, podName)
		spec := nestedMap(pod, "spec")

		// Volume mounts.
		for _, volume := range nestedSlice(spec, "volumes") {
			vm, _ := volume.(map[string]any)
			if name := strValue(nestedMap(vm, "configMap")["name"]); name != "" {
				ensure(keyFor(namespace, name), ownerID)
			}
		}
		// env / envFrom across all containers.
		for _, container := range podAllContainers(pod) {
			cm, _ := container.(map[string]any)
			for _, ef := range nestedSlice(cm, "envFrom") {
				efm, _ := ef.(map[string]any)
				if name := strValue(nestedMap(efm, "configMapRef")["name"]); name != "" {
					ensure(keyFor(namespace, name), ownerID)
				}
			}
			for _, env := range nestedSlice(cm, "env") {
				em, _ := env.(map[string]any)
				if name := strValue(nestedMap(nestedMap(em, "valueFrom"), "configMapKeyRef")["name"]); name != "" {
					ensure(keyFor(namespace, name), ownerID)
				}
			}
		}
	}
	return owners
}

// configMapEnvConsumedByNamespace returns the set of "<ns>/<cm-name>" keys
// for ConfigMaps consumed via env or envFrom (not volume mount).
func configMapEnvConsumedByNamespace(pods []map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, pod := range pods {
		namespace, _ := objectNamespaceName(pod)
		for _, container := range podAllContainers(pod) {
			cm, _ := container.(map[string]any)
			for _, ef := range nestedSlice(cm, "envFrom") {
				efm, _ := ef.(map[string]any)
				if name := strValue(nestedMap(efm, "configMapRef")["name"]); name != "" {
					out[keyFor(namespace, name)] = true
				}
			}
			for _, env := range nestedSlice(cm, "env") {
				em, _ := env.(map[string]any)
				if name := strValue(nestedMap(nestedMap(em, "valueFrom"), "configMapKeyRef")["name"]); name != "" {
					out[keyFor(namespace, name)] = true
				}
			}
		}
	}
	return out
}

// podOwnerID returns "Kind/name" from the first ownerReference, or the pod
// name when the pod has no owners.
func podOwnerID(pod map[string]any, podName string) string {
	meta := nestedMap(pod, "metadata")
	refs, _ := meta["ownerReferences"].([]any)
	if len(refs) == 0 {
		return podName
	}
	ref, _ := refs[0].(map[string]any)
	kind := strValue(ref["kind"])
	name := strValue(ref["name"])
	if kind == "" || name == "" {
		return podName
	}
	return kind + "/" + name
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
