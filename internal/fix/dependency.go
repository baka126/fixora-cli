package fix

import (
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/util/validation"
	"strings"
)

// ConnectionConfig identifies an operator-approved ConfigMap connection source.
type ConnectionConfig struct {
	Container, ConfigMap string
	Keys                 []string
}

func WithConnectionConfigPatch(p Plan, c ConnectionConfig) Plan {
	if p.Recovered || c.Container == "" || len(validation.IsDNS1123Label(c.Container)) > 0 || len(validation.IsDNS1123Subdomain(c.ConfigMap)) > 0 || len(c.Keys) == 0 || len(c.Keys) > 2 {
		return p
	}
	kind, name, ok := strings.Cut(p.Resource, "/")
	if !ok || kind != "Deployment" || len(validation.IsDNS1123Subdomain(name)) > 0 || len(validation.IsDNS1123Label(p.Namespace)) > 0 {
		return p
	}
	for _, reason := range p.BlockedReasons {
		if strings.Contains(reason, "GitOps") {
			return p
		}
	}
	env := []any{}
	seen := map[string]bool{}
	for _, key := range c.Keys {
		if (key != "DB_HOST" && key != "DB_PORT") || seen[key] {
			return p
		}
		seen[key] = true
		env = append(env, map[string]any{"name": key, "value": nil, "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": c.ConfigMap, "key": key}}})
	}
	patch := map[string]any{"apiVersion": "apps/v1", "kind": kind, "metadata": map[string]any{"name": name, "namespace": p.Namespace}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": c.Container, "env": env}}}}}}
	data, err := yaml.Marshal(patch)
	if err != nil {
		return p
	}
	p.Strategy = "env"
	p.PatchTemplate = string(data)
	p.Patches = []Patch{{Type: "strategic-merge", Target: p.Resource, Description: "Use the operator-approved ConfigMap for the database endpoint.", Preview: p.PatchTemplate}}
	p.Guardrails = append(p.Guardrails, "operator-approved-connection-source", "shadow-required")
	return Concretize(p, ConcreteOptions{})
}
