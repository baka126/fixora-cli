package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPDBSourceDirtySameFileRefused(t *testing.T) {
	o, r, c, f := cliPDBFixture(t)
	o.preview = false
	o.delivery = "pr"
	o.yes = true
	o.repoPath = t.TempDir()
	file := filepath.Join(o.repoPath, "pdb.yaml")
	initial := []byte("apiVersion: policy/v1\nkind: PodDisruptionBudget\nmetadata: {name: protect, namespace: team}\nspec: {minAvailable: 3}\n")
	if e := os.WriteFile(file, initial, 0600); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"init"}, {"add", "pdb.yaml"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "baseline"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = o.repoPath
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatal(e, string(b))
		}
	}
	dirty := append(initial, []byte("# existing user changes\n")...)
	os.WriteFile(file, dirty, 0600)
	var out, err bytes.Buffer
	if code := runPDBFixRuntime(context.Background(), &out, &err, o, r, f); code != 1 || !strings.Contains(err.String(), "clean worktree") {
		t.Fatalf("dirty source accepted: %d %s %s", code, out.String(), err.String())
	}
	got, _ := os.ReadFile(file)
	if !bytes.Equal(got, dirty) {
		t.Fatal("overwrote user changes")
	}
	assertNoPDBWrites(t, c)
}
func TestPDBSourceChangesBetweenPreviewAndWrite(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "pdb.yaml")
	os.WriteFile(file, []byte("before\n"), 0600)
	files, e := captureServiceSourceFiles(root, []string{file})
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(file, []byte("after\n"), 0600)
	if e = checkPDBSourceFiles(files); e == nil {
		t.Fatal("changed source not refused")
	}
	b, _ := os.ReadFile(file)
	if string(b) != "after\n" {
		t.Fatal("source changed during check")
	}
}
