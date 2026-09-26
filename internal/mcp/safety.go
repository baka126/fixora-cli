package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/config"
	"github.com/fixora/kubectl-fixora/internal/redact"
	yaml "sigs.k8s.io/yaml"
)

const maxMCPResultBytes = 128 * 1024

var allowedResourceTypes = map[string]bool{
	"pod": true, "pods": true, "deployment": true, "deployments": true, "statefulset": true, "statefulsets": true, "daemonset": true, "daemonsets": true, "replicaset": true, "replicasets": true, "job": true, "jobs": true, "cronjob": true, "cronjobs": true,
	"service": true, "services": true, "ingress": true, "ingresses": true, "networkpolicy": true, "networkpolicies": true, "persistentvolumeclaim": true, "persistentvolumeclaims": true, "pvc": true, "node": true, "nodes": true, "hpa": true, "poddisruptionbudget": true, "poddisruptionbudgets": true, "pdb": true, "storageclass": true, "storageclasses": true,
}

func allowResourceType(resource string) error {
	if !allowedResourceTypes[strings.ToLower(strings.TrimSpace(resource))] {
		return fmt.Errorf("resource type is not available through MCP")
	}
	return nil
}
func allowResourceRef(resource string) error {
	parts := strings.Split(resource, "/")
	if len(parts) != 2 || parts[1] == "" || strings.ContainsAny(parts[1], " \\t\\n") {
		return fmt.Errorf("resource must be kind/name")
	}
	return allowResourceType(parts[0])
}

func safeResult(value any) (any, string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	if len(raw) > maxMCPResultBytes {
		return nil, "", fmt.Errorf("MCP result exceeds 128 KiB; narrow the request")
	}
	redacted := redact.KubernetesText(string(raw))
	data, err := yaml.YAMLToJSON([]byte(redacted))
	if err != nil {
		return nil, "", fmt.Errorf("could not safely encode MCP result")
	}
	var safe any
	if err := json.Unmarshal(data, &safe); err != nil {
		return nil, "", err
	}
	return safe, string(data), nil
}

func mcpPublicConfig(cfg config.Config) map[string]any {
	out := config.Public(cfg)
	// A custom provider URL can carry credentials in query parameters or userinfo.
	delete(out, "aiBaseURL")
	return out
}
