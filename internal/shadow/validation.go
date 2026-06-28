package shadow

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/fixora/kubectl-fixora/internal/image"
)

func ValidateRevisedPatch(originalPatch, revisedPatch, planType string) error {
	planType = strings.ToLower(strings.TrimSpace(planType))
	if !allowedRevisionStrategy(planType) {
		return PatchValidationError{Reasons: []string{fmt.Sprintf("unknown or unsafe strategy %q", planType)}}
	}
	original, err := parseSinglePatch(originalPatch)
	if err != nil {
		return PatchValidationError{Reasons: []string{"original patch is not valid YAML: " + err.Error()}}
	}
	revised, err := parseSinglePatch(revisedPatch)
	if err != nil {
		return PatchValidationError{Reasons: []string{err.Error()}}
	}
	reasons := validatePatchObject(revised)
	reasons = append(reasons, validateProjectedDiff(original, revised, planType)...)
	if len(reasons) > 0 {
		return PatchValidationError{Reasons: reasons}
	}
	return nil
}

// ValidateReviewedPatch validates an operator-edited concrete patch before it
// can be used for shadow verification or delivery. Unlike an AI revision, a
// reviewed patch may retain its resource identity, but that identity must not
// drift from Fixora's generated patch.
func ValidateReviewedPatch(originalPatch, reviewedPatch, planType string) error {
	planType = strings.ToLower(strings.TrimSpace(planType))
	if !allowedRevisionStrategy(planType) {
		return PatchValidationError{Reasons: []string{fmt.Sprintf("unknown or unsafe strategy %q", planType)}}
	}
	original, err := parseSinglePatch(originalPatch)
	if err != nil {
		return PatchValidationError{Reasons: []string{"original patch is not valid YAML: " + err.Error()}}
	}
	reviewed, err := parseSinglePatch(reviewedPatch)
	if err != nil {
		return PatchValidationError{Reasons: []string{err.Error()}}
	}
	reasons := validateReviewIdentity(original, reviewed)
	original = withoutIdentity(original)
	reviewed = withoutIdentity(reviewed)
	reasons = append(reasons, validatePatchObject(reviewed)...)
	reasons = append(reasons, validateProjectedDiff(original, reviewed, planType)...)
	if len(reasons) > 0 {
		sort.Strings(reasons)
		return PatchValidationError{Reasons: reasons}
	}
	return nil
}

func validateReviewIdentity(original, reviewed map[string]any) []string {
	var reasons []string
	for _, key := range []string{"apiVersion", "kind"} {
		if value, ok := original[key]; ok && fmt.Sprint(reviewed[key]) != fmt.Sprint(value) {
			reasons = append(reasons, key+" must match the generated patch")
		}
	}
	originalMeta, _ := nestedMap(original, "metadata")
	reviewedMeta, _ := nestedMap(reviewed, "metadata")
	for _, key := range []string{"name", "namespace"} {
		if value, ok := originalMeta[key]; ok && fmt.Sprint(reviewedMeta[key]) != fmt.Sprint(value) {
			reasons = append(reasons, "metadata."+key+" must match the generated patch")
		}
	}
	return reasons
}

// withoutIdentity drops the fields validateReviewIdentity already pinned
// (apiVersion, kind, metadata.name, metadata.namespace) so they don't trip the
// downstream change checks. It preserves the rest of metadata so
// validatePatchObject still rejects label/annotation/ownerReferences drift in a
// reviewed patch, keeping it as strict as ValidateRevisedPatch.
func withoutIdentity(obj map[string]any) map[string]any {
	out := make(map[string]any, len(obj))
	for key, value := range obj {
		switch key {
		case "apiVersion", "kind":
			continue
		case "metadata":
			meta, ok := value.(map[string]any)
			if !ok {
				out["metadata"] = value
				continue
			}
			trimmed := map[string]any{}
			for k, v := range meta {
				if k == "name" || k == "namespace" {
					continue
				}
				trimmed[k] = v
			}
			if len(trimmed) > 0 {
				out["metadata"] = trimmed
			}
		default:
			out[key] = value
		}
	}
	return out
}

func parseSinglePatch(patch string) (map[string]any, error) {
	patch = strings.TrimSpace(patch)
	if patch == "" {
		return nil, fmt.Errorf("empty YAML")
	}
	if hasMultipleYAMLDocuments(patch) {
		return nil, fmt.Errorf("multi-document YAML is not allowed")
	}
	data, err := yaml.ToJSON([]byte(patch))
	if err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("invalid YAML object: %w", err)
	}
	if len(obj) == 0 {
		return nil, fmt.Errorf("YAML must contain an object")
	}
	return obj, nil
}

func allowedRevisionStrategy(strategy string) bool {
	switch strategy {
	case "image", "fix-architecture", "resources", "env":
		return true
	default:
		return false
	}
}

func validatePatchObject(obj map[string]any) []string {
	var reasons []string
	meta, _ := nestedMap(obj, "metadata")
	for _, key := range []string{"name", "namespace", "labels", "annotations", "ownerReferences"} {
		if _, ok := meta[key]; ok {
			reasons = append(reasons, "metadata."+key+" changes are not allowed")
		}
	}
	if _, ok := obj["apiVersion"]; ok {
		reasons = append(reasons, "apiVersion changes are not allowed in revised patches")
	}
	if _, ok := obj["kind"]; ok {
		reasons = append(reasons, "kind changes are not allowed in revised patches")
	}
	if spec, ok := nestedMap(obj, "spec"); ok {
		if _, ok := spec["selector"]; ok {
			reasons = append(reasons, "service or workload selector changes are not allowed")
		}
		if template, ok := nestedMap(spec, "template"); ok {
			if templateMeta, ok := nestedMap(template, "metadata"); ok {
				for _, key := range []string{"name", "namespace", "labels", "annotations", "ownerReferences"} {
					if _, ok := templateMeta[key]; ok {
						reasons = append(reasons, "spec.template.metadata."+key+" changes are not allowed")
					}
				}
			}
		}
	}
	podSpec, ok := patchPodSpec(obj)
	if !ok {
		reasons = append(reasons, "patch must target a pod template spec")
		return reasons
	}
	for _, key := range []string{"serviceAccountName", "nodeSelector", "tolerations", "affinity"} {
		if _, ok := podSpec[key]; ok {
			reasons = append(reasons, "spec."+key+" changes are not allowed")
		}
	}
	for _, key := range []string{"hostNetwork", "hostPID", "hostIPC"} {
		if truthy(podSpec[key]) {
			reasons = append(reasons, "spec."+key+" is not allowed")
		}
	}
	if volumes, ok := podSpec["volumes"].([]any); ok && len(volumes) > 0 {
		for _, volume := range volumes {
			v, _ := volume.(map[string]any)
			if _, ok := v["hostPath"]; ok {
				reasons = append(reasons, "hostPath volumes are not allowed")
			} else if _, ok := v["secret"]; ok {
				reasons = append(reasons, "secret volumes are not allowed")
			} else if _, ok := v["downwardAPI"]; ok {
				reasons = append(reasons, "downwardAPI volumes are not allowed")
			} else {
				reasons = append(reasons, "volume changes are not allowed")
			}
		}
	}
	for _, section := range []string{"containers", "initContainers"} {
		for _, c := range sliceMaps(podSpec[section]) {
			if sc, ok := c["securityContext"].(map[string]any); ok && truthy(sc["privileged"]) {
				reasons = append(reasons, section+".securityContext.privileged is not allowed")
			}
		}
	}
	return reasons
}

func validateProjectedDiff(original, revised map[string]any, strategy string) []string {
	origSpec, ok1 := patchPodSpec(original)
	revSpec, ok2 := patchPodSpec(revised)
	if !ok1 || !ok2 {
		return []string{"patch must include a pod template spec"}
	}
	var reasons []string
	allowed := allowedSpecKeys(strategy)
	for key := range revSpec {
		if !allowed[key] {
			reasons = append(reasons, "spec."+key+" is not allowed for strategy "+strategy)
		}
	}
	if len(reasons) > 0 {
		return reasons
	}
	switch strategy {
	case "image", "fix-architecture":
		reasons = append(reasons, validateContainerKeys(origSpec, revSpec, map[string]bool{"name": true, "image": true}, strategy)...)
		reasons = append(reasons, validateImageRegistries(origSpec, revSpec, activePatchPolicy())...)
	case "resources":
		reasons = append(reasons, validateContainerKeys(origSpec, revSpec, map[string]bool{"name": true, "resources": true}, strategy)...)
		reasons = append(reasons, validateResourceCeiling(revSpec, activePatchPolicy())...)
	case "env":
		reasons = append(reasons, validateContainerKeys(origSpec, revSpec, map[string]bool{"name": true, "env": true, "envFrom": true}, strategy)...)
	}
	if reflect.DeepEqual(origSpec, revSpec) {
		reasons = append(reasons, "revised patch does not change the original patch")
	}
	sort.Strings(reasons)
	return reasons
}

func allowedSpecKeys(strategy string) map[string]bool {
	switch strategy {
	case "image", "fix-architecture", "resources", "env":
		return map[string]bool{"containers": true, "initContainers": true}
	default:
		return map[string]bool{}
	}
}

func validateContainerKeys(original, spec map[string]any, allowed map[string]bool, strategy string) []string {
	var reasons []string
	for _, section := range []string{"containers", "initContainers"} {
		originalNames := containerNames(original[section])
		originalCount := len(sliceMaps(original[section]))
		if value, ok := spec[section]; ok {
			items, ok := value.([]any)
			if !ok || len(items) == 0 {
				reasons = append(reasons, section+" must be a non-empty list")
				continue
			}
			for _, item := range items {
				if _, ok := item.(map[string]any); !ok {
					reasons = append(reasons, section+" entries must be objects")
				}
			}
		}
		revisedContainers := sliceMaps(spec[section])
		// Guard against appended containers. Name membership only catches this
		// when the original names are concrete; TODO_ placeholders (e.g. the
		// fix-architecture template) leave originalNames empty, so also cap the
		// count at the original so a revision cannot introduce extra containers.
		if len(revisedContainers) > originalCount {
			reasons = append(reasons, section+" must not add entries that are not in the original patch")
		}
		for _, c := range revisedContainers {
			name := stringValue(c["name"])
			if name == "" {
				reasons = append(reasons, section+" entries must include name")
			} else if len(originalNames) > 0 && !originalNames[name] {
				reasons = append(reasons, section+"."+name+" is not present in the original patch")
			}
			for key := range c {
				if !allowed[key] {
					reasons = append(reasons, section+"."+key+" is not allowed for strategy "+strategy)
				}
			}
		}
	}
	return reasons
}

func containerNames(value any) map[string]bool {
	out := map[string]bool{}
	for _, c := range sliceMaps(value) {
		if name := stringValue(c["name"]); name != "" {
			if strings.HasPrefix(name, "TODO_") {
				continue
			}
			out[name] = true
		}
	}
	return out
}

func patchPodSpec(obj map[string]any) (map[string]any, bool) {
	if spec, ok := nestedMap(obj, "spec"); ok {
		if template, ok := nestedMap(spec, "template"); ok {
			if tplSpec, ok := nestedMap(template, "spec"); ok {
				return tplSpec, true
			}
		}
		if _, ok := spec["containers"]; ok {
			return spec, true
		}
		if _, ok := spec["initContainers"]; ok {
			return spec, true
		}
	}
	if _, ok := obj["containers"]; ok {
		return obj, true
	}
	return nil, false
}

func sliceMaps(value any) []map[string]any {
	items, _ := value.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func truthy(value any) bool {
	b, ok := value.(bool)
	return ok && b
}

func stringValue(value any) string {
	s, _ := value.(string)
	return s
}

// quantityString renders a resource value (string or YAML number) for parsing.
func quantityString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		return ""
	}
}

// validateResourceCeiling rejects revised container cpu/memory requests or
// limits that exceed the policy ceiling. A 0 ceiling means unlimited.
func validateResourceCeiling(revised map[string]any, policy PatchPolicy) []string {
	var reasons []string
	for _, section := range []string{"containers", "initContainers"} {
		for _, c := range sliceMaps(revised[section]) {
			name := stringValue(c["name"])
			resources, ok := nestedMap(c, "resources")
			if !ok {
				continue
			}
			for _, kind := range []string{"requests", "limits"} {
				block, ok := nestedMap(resources, kind)
				if !ok {
					continue
				}
				for _, dim := range []string{"cpu", "memory"} {
					raw := quantityString(block[dim])
					if raw == "" {
						continue
					}
					q, err := resource.ParseQuantity(raw)
					if err != nil {
						reasons = append(reasons, fmt.Sprintf("%s.%s resources.%s.%s %q is not a valid quantity", section, name, kind, dim, raw))
						continue
					}
					switch dim {
					case "memory":
						if policy.MaxMemoryBytes > 0 && q.Value() > policy.MaxMemoryBytes {
							reasons = append(reasons, fmt.Sprintf("%s.%s resources.%s.memory %s exceeds the ceiling", section, name, kind, raw))
						}
					case "cpu":
						if policy.MaxCPUMillicores > 0 && q.MilliValue() > policy.MaxCPUMillicores {
							reasons = append(reasons, fmt.Sprintf("%s.%s resources.%s.cpu %s exceeds the ceiling", section, name, kind, raw))
						}
					}
				}
			}
		}
	}
	return reasons
}

// validateImageRegistries rejects revised container images whose registry host
// is not allowed. The allowed set is the policy allowlist plus every registry
// host already present in the original patch (operator-sanctioned).
// Registry hostnames are compared case-insensitively.
func validateImageRegistries(original, revised map[string]any, policy PatchPolicy) []string {
	// Build a lowercased allowed set: policy defaults + original image hosts.
	allowed := make([]string, 0, len(policy.AllowedRegistries))
	for _, p := range policy.AllowedRegistries {
		allowed = append(allowed, strings.ToLower(p))
	}
	for _, section := range []string{"containers", "initContainers"} {
		for _, c := range sliceMaps(original[section]) {
			img := stringValue(c["image"])
			if img == "" || strings.Contains(img, "TODO_") {
				continue
			}
			if host, err := image.Registry(img); err == nil {
				allowed = append(allowed, strings.ToLower(host))
			}
		}
	}
	var reasons []string
	for _, section := range []string{"containers", "initContainers"} {
		for _, c := range sliceMaps(revised[section]) {
			img := stringValue(c["image"])
			if img == "" || strings.Contains(img, "TODO_") {
				continue
			}
			name := stringValue(c["name"])
			host, err := image.Registry(img)
			if err != nil {
				reasons = append(reasons, fmt.Sprintf("%s.%s image %q is not a valid reference", section, name, img))
				continue
			}
			if !hostMatchesAny(strings.ToLower(host), allowed) {
				reasons = append(reasons, fmt.Sprintf("%s.%s image registry %q is not allowed", section, name, host))
			}
		}
	}
	return reasons
}
