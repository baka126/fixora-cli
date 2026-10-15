package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/output"
	"github.com/fixora/kubectl-fixora/internal/repo"
	"github.com/fixora/kubectl-fixora/internal/servicefix"
	"github.com/fixora/kubectl-fixora/internal/termui"
)

func runServiceFix(ctx context.Context, out, errout io.Writer, o options, f analyzer.Finding) int {
	if o.servicePolicy == "" {
		return runServiceFixRuntime(ctx, out, errout, o, nil, f)
	}
	client, err := kube.NewRequiredTypedClient(o.context, "verified Service routing repairs")
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	runtime, err := servicefix.NewClient(client)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	return runServiceFixRuntime(ctx, out, errout, o, runtime, f)
}
func runServiceFixRuntime(ctx context.Context, out, errout io.Writer, o options, r servicefix.Runtime, f analyzer.Finding) int {
	if o.servicePolicy == "" {
		p := servicefix.Plan{Resource: f.ResourceKind + "/" + f.ResourceName, Namespace: f.Namespace, Status: "prerequisite-required", Scope: "unverified", Prerequisites: []string{"Provide --service-policy <local-file> with approved backend, selector, port and digest-pinned probe image"}}
		if o.output != "text" {
			return output.Write(out, o.output, p)
		}
		fmt.Fprintf(out, "%s (%s): %s\nCause: %s\nNext: %s\nNo repair proposed; no changes made.\n", p.Resource, p.Namespace, f.Status, serviceCause(f), p.Prerequisites[0])
		return 0
	}
	if f.ResourceKind != "Service" {
		fmt.Fprintln(errout, "--service-policy requires an exact Service target")
		return 2
	}
	if o.startupBaseline != "" || o.repairBaseline != "" || o.repairPolicy != "" || o.editPatch {
		fmt.Fprintln(errout, "Service routing intent cannot be combined with workload baseline/policy or --edit-patch")
		return 2
	}
	a, err := servicefix.LoadPolicy(o.servicePolicy)
	if err != nil {
		fmt.Fprintf(errout, "Service policy: %v\n", err)
		return 1
	}
	proposal := ""
	if f.AI != nil {
		proposal = f.AI.PatchYAML
	}
	p, err := servicefix.Prepare(ctx, r, a, f, proposal)
	if err != nil {
		fmt.Fprintf(errout, "Service repair requires review: %v\nNo changes made.\n", err)
		return 1
	}
	if o.output != "text" {
		return output.Write(out, o.output, p)
	}
	fmt.Fprintf(out, "%s (%s): %s\nCause: %s\n", p.Resource, p.Namespace, f.Status, serviceCause(f))
	if p.Proof == nil {
		for _, next := range p.Prerequisites {
			fmt.Fprintf(out, "Next: %s\n", next)
		}
		return 0
	}
	fmt.Fprintf(out, "Proposed routing:\n%sVerification: unverified; exact backend and Service IP connectivity contract required.\n", p.Diff)
	if o.proof || o.verbose {
		fmt.Fprintln(out, "Proof: independently approved local intent; every selected Pod must be Ready and owned by the exact backend UID. Live membership is rechecked before delivery.")
	}
	if o.preview {
		fmt.Fprintln(out, "Preview only; no verifier or production resources created.")
		return 0
	}
	mode := strings.ToLower(strings.TrimSpace(o.delivery))
	if mode == "" {
		mode = "patch"
	}
	if o.apply && mode == "patch" {
		mode = "cluster"
	}
	if mode != "patch" && mode != "cluster" && mode != "pr" {
		fmt.Fprintln(errout, "Service delivery must be patch, cluster or pr")
		return 2
	}
	if mode == "cluster" && (p.Status == "source-review-required" || hasHelmSourceContext(o)) {
		fmt.Fprintln(errout, "Service is source-managed; direct delivery blocked. Use reviewed source delivery.")
		return 1
	}
	patch, err := p.PatchYAML()
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	var source repo.SourcePatch
	var sourcePlan fix.Plan
	var sourceFiles map[string]sourceFileState
	var repoMode repo.Mode
	if mode == "pr" || hasHelmSourceContext(o) {
		if hasHelmSourceContext(o) {
			fmt.Fprintln(out, "Helm Service values translation requires manual source review; no values write, verifier or PR created.")
			return 0
		}
		if o.repoPath == "" {
			fmt.Fprintln(errout, "Service PR delivery requires --repo and --yes")
			return 2
		}
		repoMode, err = repo.Detect(o.repoPath)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		if repoMode.Type == "helm" {
			fmt.Fprintln(out, "Helm Service values translation requires manual source review; no values write, verifier or PR created.")
			return 0
		}
		if !o.yes {
			fmt.Fprintln(errout, "Service PR delivery requires --yes to confirm source write, commit, push and review request")
			return 2
		}
		sourcePlan = fix.Plan{Resource: p.Resource, Namespace: p.Namespace, Strategy: "service-routing-review", Confidence: 100, PatchTemplate: patch, BlockedReasons: []string{fix.SourceOwnershipReviewReason}}
		source, err = repo.PreviewSourcePatch(o.repoPath, o.outFile, f, sourcePlan)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		paths := []string{source.Path}
		if repoMode.Kustomization != "" {
			paths = append(paths, repoMode.Kustomization)
		}
		sourceFiles, err = captureServiceSourceFiles(o.repoPath, paths)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		fmt.Fprintf(out, "Source review: %s; runtime reconciliation and consumer health are not established.\n", source.Path)
	}
	if !o.yes && !termui.Confirm("Create temporary routing verifier resources and deliver via "+mode+"?", inputFor(o), out) {
		fmt.Fprintln(out, "Cancelled; no changes made.")
		return 0
	}
	duration := 3 * time.Minute
	if o.visited["shadow-timeout"] {
		if o.shadowTimeout <= 0 {
			fmt.Fprintln(errout, "Service verification timeout must be positive")
			return 2
		}
		duration = o.shadowTimeout
	}
	verifyCtx, cancel := context.WithTimeout(ctx, duration)
	v, err := servicefix.Verify(verifyCtx, r, p)
	cancel()
	if err != nil {
		fmt.Fprintf(errout, "Service verification failed: %v\n", err)
		printServiceCleanup(errout, v)
		return 1
	}
	fmt.Fprintf(out, "Verification: %s; temporary resources cleaned up. Consumer recovery is not established.\n", v.Scope)
	if mode == "cluster" {
		applied, applyErr := servicefix.Apply(ctx, r, p, v.Receipt)
		if applyErr != nil {
			fmt.Fprintf(errout, "Service delivery: %v\n", applyErr)
			if applied != nil {
				fmt.Fprintln(errout, "Production change outcome may be partial; inspect the Service. No automatic retry or rollback.")
			}
			return 1
		}
		liveCtx, liveCancel := context.WithTimeout(ctx, duration)
		live, liveErr := servicefix.VerifyLive(liveCtx, r, p, applied)
		liveCancel()
		if liveErr == nil {
			fmt.Fprintf(out, "%s: %s\n", live.Status, live.Summary)
			return 0
		}
		fmt.Fprintf(errout, "Production Service was changed; verification failed: %v\n", liveErr)
		if live.Verification != nil {
			printServiceCleanup(errout, *live.Verification)
		}
		if o.yes {
			fmt.Fprintln(errout, "No automatic rollback; review current routing before restoring the previous fields.")
			return 1
		}
		if termui.ConfirmRollback("restore only Fixora-edited Service selector/targetPort fields", inputFor(o), errout) {
			rollbackCtx, rollbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			restored, restoreErr := servicefix.Restore(rollbackCtx, r, applied)
			rollbackCancel()
			if restoreErr != nil {
				fmt.Fprintf(errout, "Service rollback: %v\n", restoreErr)
			} else {
				fmt.Fprintf(errout, "%s: %s\n", restored.Status, restored.Summary)
			}
		}
		return 1
	}
	if err = p.Proof.CheckFresh(ctx, r); err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	if mode == "pr" {
		for path, expected := range sourceFiles {
			got, e := readServiceSourceFile(path)
			if e != nil || got != expected {
				fmt.Fprintln(errout, "Source changed during verification; no source write performed")
				return 1
			}
		}
		written, err := repo.WriteSourcePatch(o.repoPath, o.outFile, f, sourcePlan)
		if err != nil {
			fmt.Fprintf(errout, "Source write failed (may be partial): %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "Wrote reviewed routing patch: %s. Production Service unchanged.\n", written.Path)
		if len(written.Warnings) > 0 {
			for _, warning := range written.Warnings {
				fmt.Fprintf(out, "Source: %s\n", warning)
			}
		}
		if err = repo.Validate(ctx, repoMode); err != nil {
			fmt.Fprintf(errout, "Source patch remains written; validation failed, no PR created: %v\n", err)
			return 1
		}
		paths := []string{written.Path}
		if repoMode.Kustomization != "" {
			paths = append(paths, repoMode.Kustomization)
		}
		branch := firstArg([]string{o.branch, defaultPRBranch(f, false)})
		title := firstArg([]string{o.prTitle, "fixora: review Service routing for " + p.Resource})
		if err = repo.PrepareBranchFiles(ctx, o.repoPath, branch, true, title, paths); err != nil {
			fmt.Fprintf(errout, "Source patch remains written; Git workflow failed: %v\n", err)
			return 1
		}
		body := "Review the approved Service selector/targetPort routing patch. Every captured backend and temporary Service IP passed the " + v.Scope + ". Production routing, source reconciliation, dependent application recovery and CNI enforcement have not been established."
		pr, err := repo.OpenPullRequest(ctx, o.repoPath, branch, o.prBase, title, body, true)
		if err != nil {
			fmt.Fprintf(errout, "Source commit/branch retained; push or review request failed: %v\n", err)
			return 1
		}
		if pr.Opened {
			fmt.Fprintf(out, "Review request opened: %s\n", pr.URL)
		} else {
			fmt.Fprintf(out, "Branch prepared: %s; no review request opened.\n", pr.Branch)
		}
		return 0
	}
	file := firstArg([]string{o.outFile, "fixora-service-patch.yaml"})
	if err = os.WriteFile(file, []byte(patch), 0600); err != nil {
		fmt.Fprintf(errout, "Routing patch write failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "Wrote reviewed routing patch: %s. Production Service unchanged.\n", file)
	return 0
}
func serviceCause(f analyzer.Finding) string {
	cause := f.Summary
	if f.AI != nil && f.AI.RootCause != "" {
		cause = f.AI.RootCause
	}
	return strings.Join(strings.Fields(cause), " ")
}
func printServiceCleanup(w io.Writer, v servicefix.Verification) {
	for _, c := range v.Cleanup {
		if c.Status != "deleted" {
			fmt.Fprintf(w, "Cleanup incomplete: %s/%s (%s), UID %s.\n", c.Kind, c.Name, c.Namespace, c.UID)
		}
	}
}

type sourceFileState struct {
	Exists bool
	Digest [32]byte
}

func readServiceSourceFile(path string) (sourceFileState, error) {
	var s sourceFileState
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if !info.Mode().IsRegular() {
		return s, errors.New("reviewed source must be a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	s.Exists = true
	s.Digest = sha256.Sum256(raw)
	return s, nil
}
func captureServiceSourceFiles(root string, paths []string) (map[string]sourceFileState, error) {
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	out := map[string]sourceFileState{}
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
		if err != nil {
			return nil, err
		}
		abs = filepath.Join(parent, filepath.Base(abs))
		rel, err := filepath.Rel(base, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return nil, errors.New("reviewed source file must stay inside the repository")
		}
		state, err := readServiceSourceFile(abs)
		if err != nil {
			return nil, err
		}
		out[abs] = state
	}
	return out, nil
}

func withServiceProposalIntent(f analyzer.Finding, path string) (analyzer.Finding, error) {
	a, err := servicefix.LoadPolicy(path)
	if err != nil {
		return f, err
	}
	data, err := a.ProposalContext(f.ResourceKind, f.ResourceName, f.Namespace)
	if err != nil {
		return f, err
	}
	f.Evidence = append(f.Evidence, analyzer.Evidence{Label: "Approved Service routing intent (proposal context only)", Value: data})
	return f, nil
}
