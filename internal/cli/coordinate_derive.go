package cli

import (
	"strings"

	"github.com/fixora/kubectl-fixora/internal/coordinate"
)

// References is the set of resources a workload's pod template actually
// references, plus the pod-template labels (for Service selector matching).
type References struct {
	ConfigMaps []string
	Secrets    []string
	PVCs       []string
	PodLabels  map[string]string
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
func mapAt(m map[string]any, key string) map[string]any { return asMap(m[key]) }
func sliceAt(m map[string]any, key string) []any {
	s, _ := m[key].([]any)
	return s
}
func strAt(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
func appendUniq(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

// extractReferences walks the root workload's pod template for the ConfigMaps,
// Secrets, and PVCs it references, plus its pod-template labels. Read-only, pure.
// ponytail: covers the standard workload kinds; exotic/unknown kinds yield no refs.
func extractReferences(root map[string]any) References {
	refs := References{PodLabels: map[string]string{}}
	spec := mapAt(root, "spec")
	var tmpl map[string]any
	switch strings.ToLower(strAt(root, "kind")) {
	case "cronjob":
		tmpl = mapAt(mapAt(mapAt(spec, "jobTemplate"), "spec"), "template")
	case "pod":
		tmpl = root
	default:
		tmpl = mapAt(spec, "template")
	}
	for k, v := range mapAt(mapAt(tmpl, "metadata"), "labels") {
		if s, ok := v.(string); ok {
			refs.PodLabels[k] = s
		}
	}
	podSpec := mapAt(tmpl, "spec")

	for _, ips := range sliceAt(podSpec, "imagePullSecrets") {
		refs.Secrets = appendUniq(refs.Secrets, strAt(asMap(ips), "name"))
	}
	containers := append(append([]any{}, sliceAt(podSpec, "initContainers")...), sliceAt(podSpec, "containers")...)
	for _, c := range containers {
		cm := asMap(c)
		for _, ef := range sliceAt(cm, "envFrom") {
			efm := asMap(ef)
			refs.ConfigMaps = appendUniq(refs.ConfigMaps, strAt(mapAt(efm, "configMapRef"), "name"))
			refs.Secrets = appendUniq(refs.Secrets, strAt(mapAt(efm, "secretRef"), "name"))
		}
		for _, e := range sliceAt(cm, "env") {
			vf := mapAt(asMap(e), "valueFrom")
			refs.ConfigMaps = appendUniq(refs.ConfigMaps, strAt(mapAt(vf, "configMapKeyRef"), "name"))
			refs.Secrets = appendUniq(refs.Secrets, strAt(mapAt(vf, "secretKeyRef"), "name"))
		}
	}
	for _, v := range sliceAt(podSpec, "volumes") {
		vm := asMap(v)
		refs.ConfigMaps = appendUniq(refs.ConfigMaps, strAt(mapAt(vm, "configMap"), "name"))
		refs.Secrets = appendUniq(refs.Secrets, strAt(mapAt(vm, "secret"), "secretName"))
		refs.PVCs = appendUniq(refs.PVCs, strAt(mapAt(vm, "persistentVolumeClaim"), "claimName"))
	}
	return refs
}

// matchingServices returns the names of services whose (non-empty) selector is
// a subset of podLabels.
func matchingServices(services []map[string]any, podLabels map[string]string) []string {
	var out []string
	for _, svc := range services {
		sel := mapAt(mapAt(svc, "spec"), "selector")
		if len(sel) == 0 {
			continue
		}
		match := true
		for k, v := range sel {
			if vs, _ := v.(string); podLabels[k] != vs {
				match = false
				break
			}
		}
		if match {
			out = appendUniq(out, strAt(mapAt(svc, "metadata"), "name"))
		}
	}
	return out
}

// assembleRefs orders the derived refs dependency-first: config/secret/pvc,
// then the root workload, then services.
func assembleRefs(refs References, services []string, rootRef string) []string {
	var out []string
	for _, c := range refs.ConfigMaps {
		out = append(out, "ConfigMap/"+c)
	}
	for _, s := range refs.Secrets {
		out = append(out, "Secret/"+s)
	}
	for _, p := range refs.PVCs {
		out = append(out, "PersistentVolumeClaim/"+p)
	}
	out = append(out, rootRef)
	for _, s := range services {
		out = append(out, "Service/"+s)
	}
	return out
}

// filterApplyEligible keeps only steps whose plan is apply-eligible, preserving
// order. Healthy/unfixable references drop out of the coordinated set.
func filterApplyEligible(steps []coordinate.Step) []coordinate.Step {
	out := make([]coordinate.Step, 0, len(steps))
	for _, s := range steps {
		if s.Plan.ApplyEligible {
			out = append(out, s)
		}
	}
	return out
}
