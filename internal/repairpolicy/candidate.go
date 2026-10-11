package repairpolicy

import (
	"errors"
	"reflect"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
)

// Candidate extracts only explicitly approved, changed fields for one policy.
// It does not itself authorize execution; the family validator supplies proof.
func Candidate(s Snapshot, a *Authority, family string) (string, error) {
	if a == nil || a.digest != digest(a.object) || a.digest == "" {
		return "", errors.New("explicit repair baseline required")
	}
	if err := identityMatches(a.object, s.Target); err != nil {
		return "", err
	}
	current, ok := kube.WorkloadPodSpec(s.Object)
	if !ok {
		return "", errors.New("live template is unavailable")
	}
	approved, _ := kube.WorkloadPodSpec(a.object)
	changes := map[string]any{}
	switch family {
	case "placement":
		if err := matchBaselineContainers(current, approved); err != nil {
			return "", err
		}
		for _, key := range []string{"nodeSelector", "affinity", "tolerations"} {
			if v, exists := approved[key]; exists && !reflect.DeepEqual(copyValue(v), copyValue(current[key])) {
				if key == "nodeSelector" {
					delta := copyObject(mapAny(v))
					for oldKey := range mapAny(current[key]) {
						if _, exists := delta[oldKey]; !exists {
							delta[oldKey] = nil
						}
					}
					changes[key] = delta
				} else {
					changes[key] = copyValue(v)
				}
			}
		}
	case "security":
		if v, exists := approved["securityContext"]; exists {
			delta := changedLeaves(mapAny(current["securityContext"]), mapAny(v))
			if len(delta) > 0 {
				changes["securityContext"] = delta
			}
		}
		live, err := containerMap(current)
		if err != nil {
			return "", err
		}
		baseline, err := containerMap(approved)
		if err != nil {
			return "", err
		}
		containers := []any{}
		for _, raw := range approved["containers"].([]any) {
			b := mapAny(raw)
			name, _ := b["name"].(string)
			c := live[name]
			if c == nil || b["image"] != c["image"] {
				return "", errors.New("security baseline container identity or image differs")
			}
			delta := changedLeaves(mapAny(c["securityContext"]), mapAny(b["securityContext"]))
			if len(delta) > 0 {
				containers = append(containers, map[string]any{"name": name, "securityContext": delta})
			}
		}
		if len(live) != len(baseline) {
			return "", errors.New("security baseline containers differ")
		}
		if len(containers) > 0 {
			changes["containers"] = containers
		}
	default:
		return "", errors.New("repair policy must be placement or security")
	}
	if len(changes) == 0 {
		return "", errors.New("baseline has no changed fields for this repair policy")
	}
	obj, err := kube.WorkloadPatch(s.Target.Kind, s.Target.Name, s.Target.Namespace, changes)
	if err != nil {
		return "", err
	}
	raw, err := yaml.Marshal(obj)
	return string(raw), err
}
func changedLeaves(current, approved map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range approved {
		if reflect.DeepEqual(copyValue(value), copyValue(current[key])) {
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			delta := changedLeaves(mapAny(current[key]), nested)
			if len(delta) > 0 {
				out[key] = delta
			}
		} else {
			out[key] = copyValue(value)
		}
	}
	return out
}

func matchBaselineContainers(current, approved map[string]any) error {
	live, err := containerMap(current)
	if err != nil {
		return err
	}
	baseline, err := containerMap(approved)
	if err != nil {
		return err
	}
	if len(live) != len(baseline) {
		return errors.New("placement baseline container identities differ")
	}
	for name, c := range live {
		b := baseline[name]
		if b == nil || b["image"] != c["image"] {
			return errors.New("placement baseline container identity or image differs")
		}
	}
	return nil
}
