package analyzer

import (
	"strings"
	"testing"
)

// collisionGuardStatuses are the BuildPlan switch keys that new ConfigMap
// statuses must never collide with.
var collisionGuardStatuses = []string{
	"ImagePull", "OOMKilled", "CrashLoopBackOff", "CreateContainerConfigError",
	"ExecFormatError", "PermissionDenied", "NoEndpoints", "ConnectionRefused",
	"HPA", "Evicted", "Webhook",
}

func TestConfigMapInvalidFormatJSON(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"configmaps": {
			configMapFixture("prod", "bad-json", map[string]any{
				"app.json": "{bad",
			}, nil, nil, nil),
		},
		"pods": {},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeConfigMaps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "ConfigMapInvalidFormat")
	for _, f := range findings {
		if f.Status == "ConfigMapInvalidFormat" {
			for _, e := range f.Evidence {
				if strings.Contains(e.Value, "{bad") {
					t.Fatalf("finding evidence must not include the value body, got %q", e.Value)
				}
				if !strings.Contains(e.Value, "app.json") {
					t.Fatalf("finding evidence must include the key name, got %q", e.Value)
				}
			}
		}
	}
}

func TestConfigMapInvalidFormatYAML(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"configmaps": {
			configMapFixture("prod", "bad-yaml", map[string]any{
				"app.yaml": "a: [1, 2",
			}, nil, nil, nil),
		},
		"pods": {},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeConfigMaps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "ConfigMapInvalidFormat")
	for _, f := range findings {
		if f.Status == "ConfigMapInvalidFormat" {
			for _, e := range f.Evidence {
				if strings.Contains(e.Value, "a: [1, 2") {
					t.Fatalf("finding evidence must not include the value body, got %q", e.Value)
				}
				if !strings.Contains(e.Value, "app.yaml") {
					t.Fatalf("finding evidence must name the offending key, got %q", e.Value)
				}
			}
		}
	}
}

func TestConfigMapInvalidFormatIDsIncludeKey(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"configmaps": {
			configMapFixture("prod", "bad-formats", map[string]any{
				"app.json": "{bad",
				"app.yaml": "a: [1, 2",
			}, nil, nil, nil),
		},
		"pods": {},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeConfigMaps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	count := 0
	for _, f := range findings {
		if f.Status != "ConfigMapInvalidFormat" {
			continue
		}
		count++
		if ids[f.ID] {
			t.Fatalf("duplicate invalid-format finding ID %q", f.ID)
		}
		ids[f.ID] = true
		if !strings.Contains(f.ID, "app.json") && !strings.Contains(f.ID, "app.yaml") {
			t.Fatalf("invalid-format ID must include the offending key, got %q", f.ID)
		}
	}
	if count != 2 {
		t.Fatalf("expected two invalid-format findings, got %d: %#v", count, findings)
	}
}

func TestConfigMapValidFormatNoFinding(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"configmaps": {
			configMapFixture("prod", "ok-json", map[string]any{
				"ok.json": "{}",
			}, nil, nil, nil),
		},
		"pods": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "app"},
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name": "app",
						"envFrom": []any{map[string]any{
							"configMapRef": map[string]any{"name": "ok-json"},
						}},
					}},
				},
			},
		},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeConfigMaps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Status == "ConfigMapInvalidFormat" {
			t.Fatalf("did not expect ConfigMapInvalidFormat for valid JSON, got %#v", f)
		}
	}
}

func TestConfigMapSharedByTwoWorkloads(t *testing.T) {
	// One ConfigMap referenced via envFrom by pods owned by two distinct Deployments.
	ctx := scanContextWithItems(map[string][]map[string]any{
		"configmaps": {
			configMapFixture("prod", "shared-cfg", map[string]any{"key": "val"}, nil, nil, nil),
		},
		"pods": {
			{
				"metadata": map[string]any{
					"namespace": "prod",
					"name":      "web-abc",
					"ownerReferences": []any{map[string]any{
						"kind": "ReplicaSet",
						"name": "web-rs",
					}},
				},
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name": "web",
						"envFrom": []any{map[string]any{
							"configMapRef": map[string]any{"name": "shared-cfg"},
						}},
					}},
				},
			},
			{
				"metadata": map[string]any{
					"namespace": "prod",
					"name":      "worker-xyz",
					"ownerReferences": []any{map[string]any{
						"kind": "ReplicaSet",
						"name": "worker-rs",
					}},
				},
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name": "worker",
						"envFrom": []any{map[string]any{
							"configMapRef": map[string]any{"name": "shared-cfg"},
						}},
					}},
				},
			},
		},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeConfigMaps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "ConfigMapShared")
	for _, f := range findings {
		if f.Status == "ConfigMapShared" && f.ResourceName == "shared-cfg" {
			found := false
			for _, e := range f.Evidence {
				if strings.Contains(e.Value, "referenced by 2 workloads") {
					found = true
				}
			}
			if !found {
				t.Fatalf("ConfigMapShared evidence should mention 2 workloads, got %#v", f.Evidence)
			}
		}
	}
}

func TestConfigMapCollisionGuard(t *testing.T) {
	newStatuses := []string{"ConfigMapInvalidFormat", "ConfigMapShared"}
	for _, newStatus := range newStatuses {
		for _, existing := range collisionGuardStatuses {
			if strings.Contains(newStatus, existing) || strings.Contains(existing, newStatus) {
				t.Fatalf("new status %q collides with BuildPlan switch key %q", newStatus, existing)
			}
		}
	}
}
