package workloadmemory

import (
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

// SafeRepairMemoryLimit returns the sole valid memory limit in a Kubernetes
// workload pod template. It deliberately extracts one numeric measurement;
// it never retains the patch or any adjacent field values.
func SafeRepairMemoryLimit(patch string) int64 {
	type container struct {
		Resources struct {
			Limits map[string]string `json:"limits"`
		} `json:"resources"`
	}
	type template struct {
		Spec struct {
			Containers []container `json:"containers"`
		} `json:"spec"`
	}
	type jobTemplate struct {
		Spec struct {
			Template template `json:"template"`
		} `json:"spec"`
	}
	var document struct {
		Kind string `json:"kind"`
		Spec struct {
			Containers  []container `json:"containers"`
			Template    template    `json:"template"`
			JobTemplate jobTemplate `json:"jobTemplate"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal([]byte(patch), &document); err != nil {
		return 0
	}
	kind := strings.ToLower(document.Kind)
	if kind != "deployment" && kind != "daemonset" && kind != "statefulset" && kind != "job" && kind != "cronjob" && kind != "pod" {
		return 0
	}
	var containers []container
	switch kind {
	case "pod":
		containers = document.Spec.Containers
	case "cronjob":
		containers = document.Spec.JobTemplate.Spec.Template.Spec.Containers
	default:
		containers = document.Spec.Template.Spec.Containers
	}
	var bytesValue int64
	found := false
	for _, item := range containers {
		value := strings.TrimSpace(item.Resources.Limits["memory"])
		if value == "" {
			continue
		}
		quantity, err := resource.ParseQuantity(value)
		if err != nil || quantity.Sign() <= 0 || found {
			return 0
		}
		bytesValue = quantity.Value()
		if bytesValue <= 0 {
			return 0
		}
		found = true
	}
	if !found {
		return 0
	}
	return bytesValue
}
