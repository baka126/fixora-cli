package repo

import (
	"context"
	"os"
	"os/exec"
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

func TestEnsureNoUnrelatedChangesBlocksDirtyTree(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.email", "fixora@example.com")
	gitTest(t, dir, "config", "user.name", "Fixora Test")
	if err := os.WriteFile(filepath.Join(dir, "allowed.yaml"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(dir, "allowed.yaml"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unrelated.yaml"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ensureNoUnrelatedChanges(context.Background(), dir, []string{filepath.Join(dir, "allowed.yaml")})
	if err == nil || !strings.Contains(err.Error(), "unrelated.yaml") {
		t.Fatalf("expected unrelated dirty file rejection, got %v", err)
	}
}

func TestEnsureNoUnrelatedChangesBlocksRenames(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.email", "fixora@example.com")
	gitTest(t, dir, "config", "user.name", "Fixora Test")
	if err := os.WriteFile(filepath.Join(dir, "allowed.yaml"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.yaml"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-m", "initial")
	gitTest(t, dir, "mv", "old.yaml", "new.yaml")
	err := ensureNoUnrelatedChanges(context.Background(), dir, []string{filepath.Join(dir, "allowed.yaml")})
	if err == nil || !strings.Contains(err.Error(), "old.yaml") {
		t.Fatalf("expected unrelated rename rejection, got %v", err)
	}
}

func TestEnsureNoUnrelatedChangesAllowsIntendedModifiedPath(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.email", "fixora@example.com")
	gitTest(t, dir, "config", "user.name", "Fixora Test")
	allowed := filepath.Join(dir, "allowed.yaml")
	if err := os.WriteFile(allowed, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-m", "initial")
	if err := os.WriteFile(allowed, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureNoUnrelatedChanges(context.Background(), dir, []string{allowed}); err != nil {
		t.Fatalf("intended modified path should be allowed: %v", err)
	}
}

func TestPrepareBranchRefusesToOverwriteExistingBranch(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.email", "fixora@example.com")
	gitTest(t, dir, "config", "user.name", "Fixora Test")
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("kind: Pod\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-m", "initial")
	gitTest(t, dir, "branch", "fixora/existing")
	err := PrepareBranch(context.Background(), dir, "fixora/existing", false, "")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected existing branch guard, got %v", err)
	}
}

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out)
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
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, " "), "advisory only") {
		t.Fatalf("expected advisory warning, got %#v", result.Warnings)
	}
}
