package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/pdbfix"
	"github.com/fixora/kubectl-fixora/internal/repo"
	"io"
	"os/exec"
	"strings"
)

func requireCleanPDBRepo(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain=v1", "--untracked-files=all")
	cmd.Dir = path
	b, e := cmd.Output()
	if e != nil {
		return errors.New("PDB source delivery requires a readable Git repository")
	}
	if len(strings.TrimSpace(string(b))) > 0 {
		return errors.New("PDB source delivery requires a clean worktree and index")
	}
	return nil
}
func runPDBSourceReview(ctx context.Context, out, errout io.Writer, o options, _ analyzer.Finding, _ pdbfix.Plan) int {
	if hasHelmSourceContext(o) {
		fmt.Fprintln(out, "Helm PDB values translation requires manual source review; no source files or PR created.")
		return 0
	}
	if o.repoPath == "" {
		fmt.Fprintln(errout, "PDB source review requires --repo; no source files or PR created.")
		return 2
	}
	mode, e := repo.Detect(o.repoPath)
	if e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	if mode.Type == "helm" {
		fmt.Fprintln(out, "Helm PDB values translation requires manual source review; no source files or PR created.")
		return 0
	}
	if e = requireCleanPDBRepo(ctx, o.repoPath); e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	fmt.Fprintln(out, "PDB source delivery requires manual integration: generic raw/Kustomize writers cannot preserve exact selector replacement and budget-field removal. No source files or PR created.")
	fmt.Fprintln(out, "Next: edit the existing PDB in its source of truth, replacing the selector exactly and retaining exactly one approved budget field; render and review the complete resource before reconciliation.")
	return 0
}
