package kube

import (
	"fmt"
	"strings"
)

// WorkloadVersion defines the template shapes supported by repair policy.
func WorkloadVersion(kind string) string {
	switch strings.ToLower(kind) {
	case "pod":
		return "v1"
	case "deployment", "statefulset", "daemonset", "replicaset":
		return "apps/v1"
	case "job", "cronjob":
		return "batch/v1"
	}
	return ""
}

// WorkloadPodSpec extracts a single unambiguous Pod spec. Identity-less patches
// may use one of the supported envelopes; live objects require a known kind.
func WorkloadPodSpec(obj map[string]any) (map[string]any, bool) {
	spec, ok := obj["spec"].(map[string]any)
	if !ok {
		return nil, false
	}
	_, direct := spec["containers"]
	if _, init := spec["initContainers"]; init {
		direct = true
	}
	template, hasTemplate := spec["template"].(map[string]any)
	job, hasJob := spec["jobTemplate"].(map[string]any)
	count := 0
	if direct {
		count++
	}
	if hasTemplate {
		count++
	}
	if hasJob {
		count++
	}
	if count != 1 {
		return nil, false
	}
	kind, _ := obj["kind"].(string)
	switch strings.ToLower(kind) {
	case "pod":
		if !direct {
			return nil, false
		}
	case "cronjob":
		if !hasJob {
			return nil, false
		}
	case "deployment", "statefulset", "daemonset", "replicaset", "job":
		if !hasTemplate {
			return nil, false
		}
	case "":
	default:
		return nil, false
	}
	if hasJob {
		inner, ok := job["spec"].(map[string]any)
		if !ok {
			return nil, false
		}
		template, ok = inner["template"].(map[string]any)
		if !ok {
			return nil, false
		}
	}
	if direct {
		return spec, true
	}
	pod, ok := template["spec"].(map[string]any)
	return pod, ok
}

// WorkloadPatch wraps a Pod spec change without altering controller semantics.
func WorkloadPatch(kind, name, namespace string, podSpec map[string]any) (map[string]any, error) {
	version := WorkloadVersion(kind)
	if version == "" {
		return nil, fmt.Errorf("unsupported workload kind %s", kind)
	}
	spec := podSpec
	if !strings.EqualFold(kind, "Pod") {
		spec = map[string]any{"template": map[string]any{"spec": podSpec}}
	}
	if strings.EqualFold(kind, "CronJob") {
		spec = map[string]any{"jobTemplate": map[string]any{"spec": spec}}
	}
	return map[string]any{"apiVersion": version, "kind": kind, "metadata": map[string]any{"name": name, "namespace": namespace}, "spec": spec}, nil
}

// ValidateWorkloadPatchEnvelope permits only the path to the Pod spec. The
// caller validates the strategy's allowed fields within that spec separately.
func ValidateWorkloadPatchEnvelope(obj map[string]any, kind string) error {
	if WorkloadVersion(kind) == "" {
		return fmt.Errorf("unsupported workload kind %s", kind)
	}
	if version, exists := obj["apiVersion"]; exists && version != WorkloadVersion(kind) {
		return fmt.Errorf("apiVersion must be %s for %s", WorkloadVersion(kind), kind)
	}
	if _, ok := WorkloadPodSpec(obj); !ok {
		return fmt.Errorf("patch must contain one correctly nested Pod spec")
	}
	spec, _ := obj["spec"].(map[string]any)
	if strings.EqualFold(kind, "Pod") {
		return nil
	}
	if strings.EqualFold(kind, "CronJob") {
		if err := onlyObject(spec, "jobTemplate"); err != nil {
			return err
		}
		job, _ := spec["jobTemplate"].(map[string]any)
		if err := onlyObject(job, "spec"); err != nil {
			return err
		}
		spec, _ = job["spec"].(map[string]any)
	}
	if err := onlyObject(spec, "template"); err != nil {
		return err
	}
	template, _ := spec["template"].(map[string]any)
	return onlyObject(template, "spec")
}
func onlyObject(obj map[string]any, key string) error {
	if len(obj) != 1 {
		return fmt.Errorf("workload envelope may contain only %s", key)
	}
	if _, ok := obj[key].(map[string]any); !ok {
		return fmt.Errorf("%s must be an object", key)
	}
	return nil
}
