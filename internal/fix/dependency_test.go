package fix

import (
	"gopkg.in/yaml.v3"
	"testing"
)

func TestConnectionConfigPatchClearsLiteralsAndPreservesOtherConfiguration(t *testing.T) {
	p := WithConnectionConfigPatch(Plan{Resource: "Deployment/client", Namespace: "demo", Strategy: "runtime"}, ConnectionConfig{Container: "client", ConfigMap: "approved-connection", Keys: []string{"DB_HOST", "DB_PORT"}})
	if !p.ApplyEligible || p.Strategy != "env" {
		t.Fatalf("want eligible env plan, got %+v", p)
	}
	var patch map[string]any
	if err := yaml.Unmarshal([]byte(p.PatchTemplate), &patch); err != nil {
		t.Fatal(err)
	}
	spec := patch["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	c := spec["containers"].([]any)[0].(map[string]any)
	if len(c) != 2 || c["name"] != "client" {
		t.Fatalf("only name/env should change: %+v", c)
	}
	env := c["env"].([]any)
	if len(env) != 2 {
		t.Fatalf("want two endpoint keys: %+v", env)
	}
	for _, entry := range env {
		e := entry.(map[string]any)
		value, exists := e["value"]
		ref := e["valueFrom"].(map[string]any)["configMapKeyRef"].(map[string]any)
		if !exists || value != nil || ref["name"] != "approved-connection" || ref["key"] != e["name"] {
			t.Fatalf("must clear literal and wire approved source: %+v", e)
		}
	}
}
func TestConnectionConfigPatchDeclinesUnsafeSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		plan   Plan
		config ConnectionConfig
	}{
		{"unapproved key", Plan{Resource: "Deployment/client", Namespace: "demo", Strategy: "runtime"}, ConnectionConfig{Container: "client", ConfigMap: "connection", Keys: []string{"PASSWORD"}}},
		{"duplicate keys", Plan{Resource: "Deployment/client", Namespace: "demo", Strategy: "runtime"}, ConnectionConfig{Container: "client", ConfigMap: "connection", Keys: []string{"DB_HOST", "DB_HOST"}}},
		{"recovered", Plan{Resource: "Deployment/client", Namespace: "demo", Strategy: "runtime", Recovered: true}, ConnectionConfig{Container: "client", ConfigMap: "connection", Keys: []string{"DB_HOST"}}},
		{"gitops", Plan{Resource: "Deployment/client", Namespace: "demo", Strategy: "runtime", BlockedReasons: []string{"GitOps-managed workload requires source-level patching."}}, ConnectionConfig{Container: "client", ConfigMap: "connection", Keys: []string{"DB_HOST"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if p := WithConnectionConfigPatch(tc.plan, tc.config); p.ApplyEligible {
				t.Fatal("unsafe source accepted")
			}
		})
	}
}
