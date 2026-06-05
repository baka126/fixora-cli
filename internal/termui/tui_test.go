package termui

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/table"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/repo"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

func TestTUIRowsFilterAndSelect(t *testing.T) {
	m := tuiModel{
		table: table.New(table.WithColumns([]table.Column{
			{Title: "SEV", Width: 8},
			{Title: "NS", Width: 12},
			{Title: "RESOURCE", Width: 28},
			{Title: "STATUS", Width: 20},
		})),
		report: analyzer.ScanReport{Findings: []analyzer.Finding{
			{ID: "low", Namespace: "dev", ResourceKind: "Deployment", ResourceName: "worker", Severity: "low", Status: "Pending", Summary: "waiting"},
			{ID: "high", Namespace: "prod", ResourceKind: "Deployment", ResourceName: "api", Severity: "high", Status: "CrashLoopBackOff", Summary: "panic"},
		}},
		filter: "api",
	}

	m.updateRows()
	m.syncSelected()

	if got := len(m.table.Rows()); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
	if m.selected.ID != "high" {
		t.Fatalf("selected = %q, want high", m.selected.ID)
	}
}

func TestContainsAny(t *testing.T) {
	if !containsAny("Service has no Endpoints", "endpoint") {
		t.Fatal("expected case-insensitive match")
	}
	if containsAny("Deployment crashed", "storage", "rbac") {
		t.Fatal("did not expect unrelated term match")
	}
}

func TestHelpTextIncludesShadowVerify(t *testing.T) {
	if !containsAny(helpText(), "shadow verify") {
		t.Fatal("expected TUI help to mention shadow verify")
	}
	if !containsAny(helpText(), "ai root cause") {
		t.Fatal("expected TUI help to mention AI root cause")
	}
	if !containsAny(helpText(), "github pr", "gitlab mr") {
		t.Fatal("expected TUI help to mention PR/MR delivery")
	}
}

func TestConfirmVerifiedDeliveryDefaultsNo(t *testing.T) {
	var out bytes.Buffer
	ok := ConfirmVerifiedDelivery(repo.ChangeSummary{Branch: "fixora/test", Files: []string{"fixora-patch.yaml"}, Provider: "GitHub PR"}, strings.NewReader("\n"), &out)
	if ok {
		t.Fatal("expected default no")
	}
	if !strings.Contains(out.String(), "Push verified PR/MR? [y/N]") {
		t.Fatalf("expected confirmation prompt, got %s", out.String())
	}
}

func TestTUIAIRequiresRedactionUnlessUnsafe(t *testing.T) {
	m := tuiModel{
		selected: analyzer.Finding{ID: "prod/api", Summary: "password=hunter2"},
		opts:     TUIOptions{Redact: false},
	}
	next, cmd := m.aiAnalyzeCmd()
	if cmd != nil {
		t.Fatal("expected AI command to be blocked")
	}
	got := next.(tuiModel)
	if !strings.Contains(got.message, "AI blocked") {
		t.Fatalf("expected AI blocked message, got %q", got.message)
	}
}

func TestVerifiedDeliveryCancelLeavesRepoUnchanged(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "fixora@example.com")
	runGit(t, dir, "config", "user.name", "Fixora Test")
	manifest := filepath.Join(dir, "deployment.yaml")
	if err := os.WriteFile(manifest, []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    spec:
      containers:
      - name: api
        image: old
`), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")
	beforeBranch := strings.TrimSpace(runGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD"))
	beforeHead := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	finding := analyzer.Finding{Namespace: "prod", ResourceKind: "Deployment", ResourceName: "api", Status: "ImagePullBackOff"}
	plan := fix.Concretize(fix.BuildPlan(finding), fix.ConcreteOptions{Container: "api", Image: "repo/api:v2"})
	cmd := verifiedDeliveryCommand{
		ctx:      context.Background(),
		repoPath: dir,
		branch:   "fixora/test",
		finding:  finding,
		plan:     plan,
		shadow:   shadow.Result{Verified: true, Resource: "Deployment/api", Namespace: "prod"},
		stdin:    strings.NewReader("\n"),
		stdout:   &bytes.Buffer{},
	}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")); got != beforeBranch {
		t.Fatalf("branch changed on cancel: got %q want %q", got, beforeBranch)
	}
	if got := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD")); got != beforeHead {
		t.Fatalf("commit changed on cancel: got %q want %q", got, beforeHead)
	}
	if status := strings.TrimSpace(runGit(t, dir, "status", "--porcelain")); status != "" {
		t.Fatalf("repo changed on cancel:\n%s", status)
	}
}

func TestVerifiedDeliveryConfirmCommitsPushesAndOpensReview(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, t.TempDir(), "init", "--bare", remote)
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "fixora@example.com")
	runGit(t, dir, "config", "user.name", "Fixora Test")
	runGit(t, dir, "remote", "add", "origin", remote)
	manifest := filepath.Join(dir, "deployment.yaml")
	if err := os.WriteFile(manifest, []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    spec:
      containers:
      - name: api
        image: old
`), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")
	toolDir := t.TempDir()
	gh := filepath.Join(toolDir, "gh")
	if err := os.WriteFile(gh, []byte("#!/usr/bin/env sh\necho https://example.test/pr/1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", toolDir+string(os.PathListSeparator)+oldPath)
	finding := analyzer.Finding{Namespace: "prod", ResourceKind: "Deployment", ResourceName: "api", Status: "ImagePullBackOff"}
	plan := fix.Concretize(fix.BuildPlan(finding), fix.ConcreteOptions{Container: "api", Image: "repo/api:v2"})
	var out bytes.Buffer
	cmd := verifiedDeliveryCommand{
		ctx:      context.Background(),
		repoPath: dir,
		branch:   "fixora/test",
		finding:  finding,
		plan:     plan,
		shadow:   shadow.Result{Verified: true, Resource: "Deployment/api", Namespace: "prod"},
		stdin:    strings.NewReader("y\n"),
		stdout:   &out,
	}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "https://example.test/pr/1") {
		t.Fatalf("expected fake PR URL, got %s", out.String())
	}
	if got := strings.TrimSpace(runGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")); got != "fixora/test" {
		t.Fatalf("expected delivery branch, got %q", got)
	}
	if status := strings.TrimSpace(runGit(t, dir, "status", "--porcelain")); status != "" {
		t.Fatalf("repo should be clean after delivery:\n%s", status)
	}
	remoteRefs := runGit(t, dir, "ls-remote", "--heads", "origin", "fixora/test")
	if !strings.Contains(remoteRefs, "refs/heads/fixora/test") {
		t.Fatalf("expected pushed branch, got %s", remoteRefs)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}
