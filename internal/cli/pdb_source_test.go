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

func TestReviewPDBSourceWritersCannotChangeSelectorIntent(t *testing.T) {
	for _, mode := range []string{"raw", "kustomize"} {
		t.Run(mode, func(t *testing.T) {
			o, r, c, f := cliPDBFixture(t)
			o.preview = false
			o.delivery = "pr"
			o.yes = true
			o.repoPath = t.TempDir()
			raw := "apiVersion: policy/v1\nkind: PodDisruptionBudget\nmetadata: {name: protect, namespace: team}\nspec:\n  selector: {matchLabels: {old: absent}}\n  minAvailable: 3\n"
			pdb := filepath.Join(o.repoPath, "pdb.yaml")
			os.WriteFile(pdb, []byte(raw), 0600)
			if mode == "kustomize" {
				os.WriteFile(filepath.Join(o.repoPath, "kustomization.yaml"), []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [pdb.yaml]\n"), 0600)
			}
			for _, args := range [][]string{{"init"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "baseline"}} {
				cmd := exec.Command("git", args...)
				cmd.Dir = o.repoPath
				if b, e := cmd.CombinedOutput(); e != nil {
					t.Fatal(e, string(b))
				}
			}
			stub := t.TempDir()
			os.WriteFile(filepath.Join(stub, "kubectl"), []byte("#!/bin/sh\nexit 0\n"), 0700)
			t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
			var out, err bytes.Buffer
			code := runPDBFixRuntime(context.Background(), &out, &err, o, r, f)
			cmd := exec.Command("git", "status", "--porcelain=v1", "--untracked-files=all")
			cmd.Dir = o.repoPath
			b, e := cmd.Output()
			if e != nil {
				t.Fatal(e)
			}
			if len(bytes.TrimSpace(b)) != 0 {
				t.Errorf("PDB source writer changed files or index: %s", b)
			}
			if code != 0 || !strings.Contains(out.String(), "manual") {
				t.Errorf("expected explicit manual fallback without source/PR writes: %d %s %s", code, out.String(), err.String())
			}
			got, _ := os.ReadFile(pdb)
			if string(got) != raw {
				t.Error("original source PDB changed")
			}
			assertNoPDBWrites(t, c)
		})
	}
}
