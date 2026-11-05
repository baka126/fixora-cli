package repo

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

type realHelmDeps struct{ helmEffectFixture }

func (d *realHelmDeps) Renderer() HelmRenderer { return NewHelmRenderer() }
func TestHelmRealSourceCertification(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("real Helm unavailable")
	}
	for _, mode := range []string{"direct", "derived", "ordered", "alias", "hook-collateral", "resources", "probe", "config", "null-resource", "null-probe", "null-env", "shared-global", "schema"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			src := "../../test/e2e/fixtures/helm-source/dynamic"
			err := filepath.WalkDir(src, func(path string, e fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(src, path)
				dest := filepath.Join(root, "chart", rel)
				if e.IsDir() {
					return os.MkdirAll(dest, 0700)
				}
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				return os.WriteFile(dest, b, 0600)
			})
			if err != nil {
				t.Fatal(err)
			}
			c := HelmRenderContext{RepoPath: root, ChartPath: "chart", Release: "fixture", Namespace: "app", EditValuesPath: "chart/values.yaml", RenderMode: "install", KubeVersion: "1.33.0", APIVersions: []string{"apps/v1", "v1"}}
			valuesPath := filepath.Join(root, c.EditValuesPath)
			switch mode {
			case "shared-global":
				b, _ := os.ReadFile(valuesPath)
				os.WriteFile(valuesPath, []byte(strings.Replace(string(b), "global: {}", "global: {reference: example/app:v1}", 1)), 0600)
			case "schema":
				os.WriteFile(filepath.Join(root, "chart", "values.schema.json"), []byte(`{"type":"object","properties":{"choice":{"enum":["example/app:v1"]}}}`), 0600)
			case "null-env":
				b, _ := os.ReadFile(valuesPath)
				os.WriteFile(valuesPath, append(b, []byte("literal: old.example.invalid\n")...), 0600)
			case "derived":
				b, _ := os.ReadFile(valuesPath)
				os.WriteFile(valuesPath, []byte(strings.Replace(string(b), "format: direct", "format: derived", 1)), 0600)
			case "ordered":
				os.WriteFile(filepath.Join(root, "base.yaml"), []byte("choice: ignored/app:v0\n"), 0600)
				os.WriteFile(filepath.Join(root, "override.yaml"), []byte("choice: example/app:v1\n"), 0600)
				c.ValuesFiles = []string{"base.yaml", "override.yaml"}
				c.EditValuesPath = "override.yaml"
			case "hook-collateral":
				b, _ := os.ReadFile(valuesPath)
				os.WriteFile(valuesPath, []byte(strings.Replace(string(b), "collateral: false", "collateral: true", 1)), 0600)
			}
			in, err := CaptureHelmInputs(c)
			if err != nil {
				t.Fatal(err)
			}
			before, err := stableHelmRender(context.Background(), NewHelmRenderer(), in, nil, testHelmScope)
			if err != nil {
				t.Fatal(err)
			}
			name, container, next := "worker", "worker", "example/app:v2"
			if mode == "alias" {
				name, container, next = "helper", "helper", "example/helper:v2"
			}
			key := ResourceKey{"apps/v1", "Deployment", "app", name}
			live, err := before.Target(key)
			if err != nil {
				t.Fatal(err)
			}
			live = copyHelmObject(live)
			live["metadata"].(map[string]any)["uid"] = "owner"
			patch := "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: " + name + ", namespace: app}\nspec:\n  template:\n    spec:\n      containers:\n      - {name: " + container + ", image: " + next + "}\n"
			req := HelmSourceRequest{Context: c, Finding: analyzer.Finding{ResourceKind: "Deployment", ResourceName: name, Namespace: "app", ResourceUID: "owner"}, Plan: fix.Plan{Resource: "Deployment/" + name, Namespace: "app", Strategy: "image", TargetUID: "owner", PatchTemplate: patch}}

			prefix := "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: worker, namespace: app}\nspec:\n  template:\n    spec:\n      containers:\n      - name: worker\n"
			switch mode {
			case "resources":
				req.Plan.Strategy = "resources"
				req.Plan.PatchTemplate = prefix + "        resources: {limits: {memory: 128Mi}}\n"
			case "null-resource":
				req.Plan.Strategy = "resources"
				req.Plan.PatchTemplate = prefix + "        resources: {limits: {memory: null}}\n"
			case "probe":
				req.Plan.Strategy = "probe"
				req.Plan.PatchTemplate = prefix + "        readinessProbe: {httpGet: {path: /ready}}\n"
			case "null-probe":
				req.Plan.Strategy = "probe"
				req.Plan.PatchTemplate = prefix + "        readinessProbe: null\n"
				req.Mappings = []HelmMapping{{FieldPointer: "/spec/template/spec/containers/0/readinessProbe", ValuesPointer: "/health"}}
			case "null-env":
				req.Plan.Strategy = "env"
				req.Plan.PatchTemplate = prefix + "        env: [{name: HOST, value: null, valueFrom: {configMapKeyRef: {name: settings-old, key: HOST}}}]\n"
				req.Mappings = []HelmMapping{{FieldPointer: "/spec/template/spec/containers/0/env/0/value", ValuesPointer: "/literal"}}
			case "config":
				req.Plan.Strategy = "env"
				req.Plan.PatchTemplate = prefix + "        env: [{name: HOST, valueFrom: {configMapKeyRef: {name: settings-new, key: HOST}}}]\n"
			}
			req.Finding.GitOps.ManagedBy = "helm"
			req.Plan.Confidence = 99
			req.Plan.BlockedReasons = []string{fix.SourceOwnershipReviewReason}
			d := &realHelmDeps{helmEffectFixture: helmEffectFixture{live: live}}
			plan, err := PrepareHelmSource(context.Background(), req, d)
			if mode == "hook-collateral" || mode == "null-resource" || mode == "null-probe" || mode == "shared-global" || mode == "schema" {
				if err == nil || plan.Proof != nil {
					t.Fatal("hook collateral certified")
				}
				return
			}
			if err != nil || plan.Proof == nil {
				t.Fatal(plan, err)
			}
			if _, err = WriteValidatedHelmSource(context.Background(), plan, d, true); err != nil {
				t.Fatal(err)
			}
			t.Logf("real Helm %s certified %v", mode, plan.ChangedKeys)
		})
	}
}
