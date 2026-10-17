package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
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
func runPDBSourceReview(ctx context.Context, out, errout io.Writer, o options, f analyzer.Finding, p pdbfix.Plan) int {
	if hasHelmSourceContext(o) {
		fmt.Fprintln(out, "Helm PDB values translation requires manual source review; no source files changed.")
		return 0
	}
	if o.repoPath == "" {
		fmt.Fprintln(errout, "PDB source delivery requires --repo and --yes")
		return 2
	}
	mode, e := repo.Detect(o.repoPath)
	if e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	if mode.Type == "helm" {
		fmt.Fprintln(out, "Helm PDB values translation requires manual source review; no source files changed.")
		return 0
	}
	if !o.yes {
		fmt.Fprintln(errout, "PDB PR delivery requires --yes to confirm source write, commit, push and review request")
		return 2
	}
	plan := fix.Plan{Resource: p.Resource, Namespace: p.Namespace, Strategy: "pdb-policy-review", Confidence: 100, PatchTemplate: p.Diff, BlockedReasons: []string{fix.SourceOwnershipReviewReason}}
	if e = requireCleanPDBRepo(ctx, o.repoPath); e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	source, e := repo.PreviewSourcePatch(o.repoPath, o.outFile, f, plan)
	if e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	paths := []string{source.Path}
	if mode.Kustomization != "" {
		paths = append(paths, mode.Kustomization)
	}
	files, e := captureServiceSourceFiles(o.repoPath, paths)
	if e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	if e = requireCleanPDBRepo(ctx, o.repoPath); e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	if e = checkPDBSourceFiles(files); e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}

	written, e := repo.WriteSourcePatch(o.repoPath, o.outFile, f, plan)
	if e != nil {
		fmt.Fprintf(errout, "PDB source write may be partial: %v\n", e)
		return 1
	}
	fmt.Fprintf(out, "Wrote reviewed PDB source patch: %s. Production reconciliation is not established.\n", written.Path)
	if e = repo.Validate(ctx, mode); e != nil {
		fmt.Fprintf(errout, "Source patch remains written; validation failed, no PR created: %v\n", e)
		return 1
	}
	branch := firstArg([]string{o.branch, defaultPRBranch(f, false)})
	title := firstArg([]string{o.prTitle, "fixora: review PDB availability for " + p.Resource})
	if e = repo.PrepareBranchFiles(ctx, o.repoPath, branch, true, title, paths); e != nil {
		fmt.Fprintln(errout, "Reviewed source retained; Git workflow:", e)
		return 1
	}
	pr, e := repo.OpenPullRequest(ctx, o.repoPath, branch, o.prBase, title, "Review this independently approved PDB selector/budget correction. This is source review only: runtime reconciliation, application quorum and actual eviction/drain behavior have not been verified.", true)
	if e != nil {
		fmt.Fprintln(errout, "Source commit/branch retained; push or review request:", e)
		return 1
	}
	if pr.Opened {
		fmt.Fprintln(out, "Review request opened:", pr.URL)
	} else {
		fmt.Fprintln(out, "Review branch prepared:", pr.Branch)
	}
	return 0
}

func checkPDBSourceFiles(files map[string]sourceFileState) error {
	for path, want := range files {
		got, e := readServiceSourceFile(path)
		if e != nil || got != want {
			return errors.New("Source changed since preview; no source write performed")
		}
	}
	return nil
}
