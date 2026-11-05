package shadow

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateCandidatePatch checks a generated concrete patch before an MCP
// request can create shadow resources. Unlike revision validation, this has
// no earlier patch to compare with, so it enforces the strategy allowlist and
// exact target identity against the plan.
func ValidateCandidatePatch(patch, strategy, resource, namespace string) error {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if !allowedRevisionStrategy(strategy) {
		return PatchValidationError{Reasons: []string{"strategy is not eligible for shadow verification"}}
	}
	obj, err := parseSinglePatch(patch)
	if err != nil {
		return err
	}
	parts := strings.SplitN(resource, "/", 2)
	if len(parts) != 2 {
		return PatchValidationError{Reasons: []string{"resource must be kind/name"}}
	}
	meta, _ := nestedMap(obj, "metadata")
	var reasons []string
	if !strings.EqualFold(stringValue(obj["kind"]), parts[0]) {
		reasons = append(reasons, "patch kind differs from plan resource")
	}
	if stringValue(meta["name"]) != parts[1] {
		reasons = append(reasons, "patch name differs from plan resource")
	}
	if stringValue(meta["namespace"]) != namespace {
		reasons = append(reasons, "patch namespace differs from plan namespace")
	}
	reasons = append(reasons, validateCandidateShape(obj, parts[0])...)
	reasons = append(reasons, validatePatchObject(withoutIdentity(obj))...)
	spec, ok := patchPodSpec(obj)
	if ok {
		if !candidateHasStrategyField(spec, strategy) {
			reasons = append(reasons, "candidate patch does not contain a "+strategy+" change")
		}
		for key := range spec {
			if !allowedSpecKeys(strategy)[key] {
				reasons = append(reasons, "spec."+key+" is not allowed for strategy "+strategy)
			}
		}
		allowed := map[string]bool{"name": true}
		switch strategy {
		case "image", "fix-architecture":
			allowed["image"] = true
			reasons = append(reasons, validateImageRegistries(map[string]any{}, spec, activePatchPolicy())...)
		case "resources":
			allowed["resources"] = true
			reasons = append(reasons, validateResourceCeiling(spec, activePatchPolicy())...)
			reasons = append(reasons, validateCandidateResources(spec)...)
		case "env":
			allowed["env"] = true
			reasons = append(reasons, validateCandidateEnv(spec)...)
		case "probe":
			allowed["readinessProbe"] = true
			allowed["livenessProbe"] = true
			allowed["startupProbe"] = true
			reasons = append(reasons, validateProbeHandlers(spec)...)
		}
		reasons = append(reasons, validateContainerKeys(spec, spec, allowed, strategy)...)
	}
	if len(reasons) > 0 {
		sort.Strings(reasons)
		return PatchValidationError{Reasons: reasons}
	}
	return nil
}

func validateCandidateShape(obj map[string]any, kind string) []string {
	var reasons []string
	for key := range obj {
		if key != "apiVersion" && key != "kind" && key != "metadata" && key != "spec" {
			reasons = append(reasons, "top-level "+key+" is not allowed")
		}
	}
	meta, ok := nestedMap(obj, "metadata")
	if !ok {
		reasons = append(reasons, "metadata must be an object")
	} else {
		for key := range meta {
			if key != "name" && key != "namespace" {
				reasons = append(reasons, "metadata."+key+" is not allowed")
			}
		}
	}
	spec, ok := nestedMap(obj, "spec")
	if !ok {
		return append(reasons, "spec must be an object")
	}
	if strings.EqualFold(kind, "Pod") {
		return reasons
	}
	for key := range spec {
		if key != "template" {
			reasons = append(reasons, "spec."+key+" is not allowed")
		}
	}
	template, ok := nestedMap(spec, "template")
	if !ok {
		return append(reasons, "spec.template must be an object")
	}
	for key := range template {
		if key != "spec" {
			reasons = append(reasons, "spec.template."+key+" is not allowed")
		}
	}
	return reasons
}

func validateCandidateEnv(spec map[string]any) []string {
	var reasons []string
	for _, section := range []string{"containers", "initContainers"} {
		for _, container := range sliceMaps(spec[section]) {
			name := stringValue(container["name"])
			for _, env := range sliceMaps(container["env"]) {
				if len(env) != 2 || stringValue(env["name"]) == "" {
					reasons = append(reasons, fmt.Sprintf("%s.%s env entry must contain only name and valueFrom", section, name))
					continue
				}
				source, ok := nestedMap(env, "valueFrom")
				if !ok || len(source) != 1 {
					reasons = append(reasons, fmt.Sprintf("%s.%s env must use a ConfigMap key reference", section, name))
					continue
				}
				ref, ok := nestedMap(source, "configMapKeyRef")
				if !ok || len(ref) != 2 || stringValue(ref["name"]) == "" || stringValue(ref["key"]) == "" {
					reasons = append(reasons, fmt.Sprintf("%s.%s env must use only a named ConfigMap key", section, name))
				}
			}
		}
	}
	return reasons
}

func validateCandidateResources(spec map[string]any) []string {
	var reasons []string
	for _, section := range []string{"containers", "initContainers"} {
		for _, container := range sliceMaps(spec[section]) {
			resources, ok := nestedMap(container, "resources")
			if !ok {
				continue
			}
			for kind, raw := range resources {
				if kind != "requests" && kind != "limits" {
					reasons = append(reasons, "resources."+kind+" is not allowed")
					continue
				}
				dimensions, ok := raw.(map[string]any)
				if !ok {
					reasons = append(reasons, "resources."+kind+" must be a map")
					continue
				}
				for dimension := range dimensions {
					if dimension != "cpu" && dimension != "memory" {
						reasons = append(reasons, "resources."+kind+"."+dimension+" is not allowed")
					}
				}
			}
		}
	}
	return reasons
}

func candidateHasStrategyField(spec map[string]any, strategy string) bool {
	for _, section := range []string{"containers", "initContainers"} {
		for _, container := range sliceMaps(spec[section]) {
			switch strategy {
			case "image", "fix-architecture":
				if stringValue(container["image"]) != "" {
					return true
				}
			case "resources":
				if resources, ok := nestedMap(container, "resources"); ok {
					for _, kind := range []string{"requests", "limits"} {
						if values, ok := nestedMap(resources, kind); ok && (values["cpu"] != nil || values["memory"] != nil) {
							return true
						}
					}
				}
			case "env":
				if len(sliceMaps(container["env"])) > 0 {
					return true
				}
			case "probe":
				for _, key := range []string{"readinessProbe", "livenessProbe", "startupProbe"} {
					if probe, ok := nestedMap(container, key); ok && len(probe) > 0 {
						return true
					}
				}
			}
		}
	}
	return false
}
