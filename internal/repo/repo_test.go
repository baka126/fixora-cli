package repo

import (
	"context"
	"encoding/json"
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

func TestWriteSourcePatchHelmDoesNotMutateValues(t *testing.T) {
	dir := t.TempDir()
	writeFixtureChart(t, dir)
	valuesPath := filepath.Join(dir, "values.yaml")
	before, _ := os.ReadFile(valuesPath)
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "myapp", Namespace: "prod"}
	f.GitOps.HelmRelease = "rel"
	plan := fix.BuildPlan(f)
	res, err := WriteSourcePatch(dir, "", f, plan)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(valuesPath)
	if string(before) != string(after) {
		t.Fatalf("values.yaml must not be mutated by the Helm path")
	}
	if res.HelmSource == nil || len(res.HelmSource.ValuesFiles) == 0 {
		t.Fatalf("expected HelmSource populated, got %+v", res.HelmSource)
	}
	if res.Mode != "helm" {
		t.Fatalf("expected Mode==helm, got %q", res.Mode)
	}
}

func TestWriteSourcePatchHelmMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte("apiVersion: v2\nname: api\nversion: 0.1.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := filepath.Join(dir, "values.yaml")
	if err := os.WriteFile(values, []byte("image:\n  repository: api\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(values)
	finding := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "prod", Status: "ImagePullBackOff"}
	plan := fix.BuildPlan(finding)

	result, err := WriteSourcePatch(dir, "", finding, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "helm" {
		t.Fatalf("expected helm mode, got %#v", result)
	}
	// values.yaml must not be mutated
	after, _ := os.ReadFile(values)
	if string(before) != string(after) {
		t.Fatalf("values.yaml must not be mutated by the Helm path, got: %s", string(after))
	}
	// HelmSource must be populated
	if result.HelmSource == nil {
		t.Fatalf("expected HelmSource populated, got nil")
	}
	// Warnings must guide operator toward helm template verification
	if !containsString(result.Warnings, "helm template") {
		t.Fatalf("expected helm template warning, got %v", result.Warnings)
	}
}

func TestSourcePatchHelmSourceJSONTags(t *testing.T) {
	patch := SourcePatch{
		Path: "values.yaml",
		Mode: "helm",
		HelmSource: &HelmSourceLocation{
			Chart:          "api",
			ChartPath:      "charts/api",
			OwningSubchart: "worker",
			TemplateFile:   "templates/deployment.yaml",
			Release:        "rel",
			Namespace:      "prod",
			ValuesFiles:    []string{"values.yaml"},
			Pinpointed:     true,
		},
	}
	data, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		`"helmSource"`,
		`"chartPath":"charts/api"`,
		`"owningSubchart":"worker"`,
		`"templateFile":"templates/deployment.yaml"`,
		`"valuesFiles":["values.yaml"]`,
		`"pinpointed":true`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %s in JSON, got %s", want, got)
		}
	}
	for _, unwanted := range []string{`"ChartPath"`, `"OwningSubchart"`, `"TemplateFile"`, `"ValuesFiles"`, `"Notes"`} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("unexpected Go field name %s in JSON: %s", unwanted, got)
		}
	}
}

func TestResolveRepoPathDoesNotDoublePrefixRelativeRepo(t *testing.T) {
	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})
	if err := os.Mkdir("chart", 0o755); err != nil {
		t.Fatal(err)
	}
	got := resolveRepoPath("chart", filepath.Join("chart", "values.yaml"))
	want, err := filepath.Abs(filepath.Join("chart", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
