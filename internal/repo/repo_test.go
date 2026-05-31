package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

func TestWriteSourcePatchUpdatesKustomization(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte("resources:\n- deployment.yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	finding := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "prod", Status: "ImagePullBackOff"}
	plan := fix.BuildPlan(finding)

	result, err := WriteSourcePatch(dir, "", finding, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "kustomize" {
		t.Fatalf("expected kustomize mode, got %#v", result)
	}
	kustomization, err := os.ReadFile(filepath.Join(dir, "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kustomization), "fixora-patch.yaml") {
		t.Fatalf("expected kustomization to reference patch, got %s", string(kustomization))
	}
	if _, err := os.Stat(filepath.Join(dir, "fixora-patch.yaml")); err != nil {
		t.Fatalf("expected patch file: %v", err)
	}
}

func TestWriteSourcePatchAppendsHelmReviewBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte("apiVersion: v2\nname: api\nversion: 0.1.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := filepath.Join(dir, "values.yaml")
	if err := os.WriteFile(values, []byte("image:\n  repository: api\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	finding := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "prod", Status: "ImagePullBackOff"}
	plan := fix.BuildPlan(finding)

	result, err := WriteSourcePatch(dir, "", finding, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "helm" {
		t.Fatalf("expected helm mode, got %#v", result)
	}
	data, err := os.ReadFile(values)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "fixoraSuggestedPatch") {
		t.Fatalf("expected Helm review block, got %s", string(data))
	}
}
