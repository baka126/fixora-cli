package coordinate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"
)

func BuildInversePatch(before map[string]any, verifiedPatch string) (string, error) {
	obj, changes, err := rollbackPatch(verifiedPatch)
	if err != nil {
		return "", err
	}
	if err = rollbackIdentity(before, obj, ""); err != nil {
		return "", err
	}
	source, ok := kube.WorkloadPodSpec(before)
	if !ok {
		return "", errors.New("rollback source template unavailable")
	}
	inverse, changed, err := inverseFields(source, changes, "")
	if err != nil {
		return "", err
	}
	if !changed {
		return "", errors.New("candidate does not change any fields")
	}
	meta := objectMap(before["metadata"])
	kind, _ := before["kind"].(string)
	name, _ := meta["name"].(string)
	ns, _ := meta["namespace"].(string)
	wrapped, err := kube.WorkloadPatch(kind, name, ns, inverse.(map[string]any))
	if err != nil {
		return "", err
	}
	data, err := yaml.Marshal(wrapped)
	return string(data), err
}
func inverseFields(before, patch any, key string) (any, bool, error) {
	switch delta := patch.(type) {
	case map[string]any:
		old := objectMap(before)
		out := map[string]any{}
		changed := false
		for field, value := range delta {
			if strings.HasPrefix(field, "$") {
				return nil, false, errors.New("forward candidate contains a merge directive")
			}
			previous, exists := old[field]
			if value == nil {
				if exists && previous != nil {
					out[field] = previous
					changed = true
				}
				continue
			}
			// A new object-valued field must be removed as a whole. Leaving an
			// empty valueFrom/selector object can be rejected by the API.
			_, newObject := value.(map[string]any)
			if (!exists || previous == nil) && newObject {
				out[field] = nil
				changed = true
				continue
			}
			inv, modified, err := inverseFields(previous, value, field)
			if err != nil {
				return nil, false, err
			}
			if modified {
				out[field] = inv
				changed = true
			}
		}
		return out, changed, nil
	case []any:
		mergeKey := listKey(lastField(key))
		if mergeKey == "" {
			if reflect.DeepEqual(normalize(before), normalize(patch)) {
				return nil, false, nil
			}
			return before, true, nil
		}
		old, _ := before.([]any)
		out := []any{}
		seen := map[any]bool{}
		for _, raw := range delta {
			item, ok := raw.(map[string]any)
			if !ok || item[mergeKey] == nil {
				return nil, false, errors.New("named patch list is malformed")
			}
			identity := item[mergeKey]
			switch identity.(type) {
			case string, float64, int:
			default:
				return nil, false, errors.New("named patch identity is invalid")
			}
			if seen[identity] {
				return nil, false, errors.New("named patch identity is duplicated")
			}
			seen[identity] = true
			previous, found := namedItem(old, mergeKey, identity)
			if !found {
				out = append(out, map[string]any{mergeKey: identity, "$patch": "delete"})
				continue
			}
			inv, changed, err := inverseFields(previous, item, key)
			if err != nil {
				return nil, false, err
			}
			if changed {
				entry := inv.(map[string]any)
				entry[mergeKey] = identity
				out = append(out, entry)
			}
		}
		return out, len(out) > 0, nil
	default:
		if reflect.DeepEqual(normalize(before), normalize(patch)) {
			return nil, false, nil
		}
		return before, true, nil
	}
}
func lastField(path string) string {
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}
func listKey(field string) string {
	switch field {
	case "containers", "initContainers", "env", "volumes":
		return "name"
	case "volumeMounts":
		return "mountPath"
	case "ports":
		return "containerPort"
	}
	return ""
}
func namedItem(items []any, key string, value any) (map[string]any, bool) {
	var found map[string]any
	for _, v := range items {
		item := objectMap(v)
		if reflect.DeepEqual(normalize(item[key]), normalize(value)) {
			if found != nil {
				return nil, false
			}
			found = item
		}
	}
	return found, found != nil
}
func CheckAppliedFields(live map[string]any, patch, uid string) error {
	obj, changes, err := rollbackPatch(patch)
	if err != nil {
		return err
	}
	if err = rollbackIdentity(live, obj, uid); err != nil {
		return err
	}
	spec, ok := kube.WorkloadPodSpec(live)
	if !ok || !touchedMatch(changes, spec, nil, "") {
		return errors.New("touched fields changed; rollback or verification is refused")
	}
	return nil
}

// Added named entries are deleted by the inverse. An unrelated edit within one
// of those entries therefore makes rollback conflict instead of erasing it.
func CheckRollbackFields(live, before map[string]any, patch, uid string) error {
	if err := CheckAppliedFields(live, patch, uid); err != nil {
		return err
	}
	_, changes, err := rollbackPatch(patch)
	if err != nil {
		return err
	}
	spec, _ := kube.WorkloadPodSpec(live)
	source, _ := kube.WorkloadPodSpec(before)
	if !touchedMatch(changes, spec, source, "") {
		return errors.New("added entry changed since apply; rollback conflict")
	}
	return nil
}
func touchedMatch(changes, actual, before any, key string) bool {
	switch delta := changes.(type) {
	case map[string]any:
		got, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		old := objectMap(before)
		for field, value := range delta {
			if field == "$patch" {
				continue
			}
			observed, exists := got[field]
			if value == nil {
				if exists && observed != nil && observed != "" {
					return false
				}
				continue
			}
			if before != nil && old[field] == nil {
				if _, object := value.(map[string]any); object && !reflect.DeepEqual(normalizeAt(value, key+"/"+field), normalizeAt(observed, key+"/"+field)) {
					return false
				}
			}
			if !exists || !touchedMatch(value, observed, old[field], key+"/"+field) {
				return false
			}
		}
		return true
	case []any:
		got, ok := actual.([]any)
		if !ok {
			return len(delta) == 0 && actual == nil
		}
		mergeKey := listKey(lastField(key))
		if mergeKey == "" {
			return reflect.DeepEqual(normalizeAt(changes, key), normalizeAt(actual, key))
		}
		old, _ := before.([]any)
		for _, raw := range delta {
			item := objectMap(raw)
			observed, found := namedItem(got, mergeKey, item[mergeKey])
			if item["$patch"] == "delete" {
				if found {
					return false
				}
				continue
			}
			if !found {
				return false
			}
			previous, existed := namedItem(old, mergeKey, item[mergeKey])
			if before != nil && !existed && !reflect.DeepEqual(normalizeAt(item, key), normalizeAt(observed, key)) {
				return false
			}
			if !touchedMatch(item, observed, previous, key) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(normalizeAt(changes, key), normalizeAt(actual, key))
	}
}
func rollbackPatch(patch string) (map[string]any, map[string]any, error) {
	decoder := yaml.NewDecoder(strings.NewReader(patch))
	var obj map[string]any
	if err := decoder.Decode(&obj); err != nil {
		return nil, nil, err
	}
	var more any
	if err := decoder.Decode(&more); err != io.EOF {
		return nil, nil, errors.New("patch must have one document")
	}
	kind, _ := obj["kind"].(string)
	if err := kube.ValidateWorkloadPatchEnvelope(obj, kind); err != nil {
		return nil, nil, err
	}
	for field := range obj {
		if field != "apiVersion" && field != "kind" && field != "metadata" && field != "spec" {
			return nil, nil, fmt.Errorf("rollback patch cannot touch %s", field)
		}
	}
	meta := objectMap(obj["metadata"])
	if len(meta) != 2 {
		return nil, nil, errors.New("portable patch metadata requires only name and namespace")
	}
	spec, _ := kube.WorkloadPodSpec(obj)
	return obj, spec, nil
}
func rollbackIdentity(live, patch map[string]any, uid string) error {
	lm, pm := objectMap(live["metadata"]), objectMap(patch["metadata"])
	if live["kind"] != patch["kind"] || live["apiVersion"] != patch["apiVersion"] || lm["name"] != pm["name"] || lm["namespace"] != pm["namespace"] || (uid != "" && lm["uid"] != uid) {
		return errors.New("rollback target identity changed")
	}
	return nil
}
func objectMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func normalize(v any) any {
	raw, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

// Kubernetes canonicalizes quantities on readback; that is not a concurrent edit.
func normalizeAt(value any, path string) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, child := range v {
			out[key] = normalizeAt(child, path+"/"+key)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = normalizeAt(child, path)
		}
		return out
	default:
		if strings.Contains(path, "/resources/requests/") || strings.Contains(path, "/resources/limits/") {
			if q, err := resource.ParseQuantity(fmt.Sprint(v)); err == nil {
				return q.String()
			}
		}
		return normalize(value)
	}
}
