package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/output"
	"github.com/fixora/kubectl-fixora/internal/pdbfix"
	"github.com/fixora/kubectl-fixora/internal/termui"
	"io"
	"os"
	"strings"
	"time"
)

func validatePDBOptions(o options, f analyzer.Finding) error {
	if f.ResourceKind != "PodDisruptionBudget" {
		return errors.New("--pdb-policy requires an exact PodDisruptionBudget target")
	}
	if o.servicePolicy != "" || o.startupBaseline != "" || o.repairBaseline != "" || o.repairPolicy != "" || o.editPatch {
		return errors.New("PDB intent cannot be combined with Service/workload policy, baseline or --edit-patch")
	}
	if o.visited["timeout"] && o.timeout <= 0 {
		return errors.New("PDB --timeout must be positive")
	}
	return nil
}
func runPDBFix(ctx context.Context, out, errout io.Writer, o options, f analyzer.Finding) int {
	if e := validatePDBOptions(o, f); e != nil {
		fmt.Fprintln(errout, e)
		return 2
	}
	if o.pdbPolicy == "" {
		return runPDBFixRuntime(ctx, out, errout, o, nil, f)
	}
	k, e := kube.NewRequiredTypedClient(o.context, "PDB availability repairs")
	if e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	r, e := pdbfix.NewClient(k)
	if e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	return runPDBFixRuntime(ctx, out, errout, o, r, f)
}
func pdbBrief(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	rs := []rune(s)
	if len(rs) > 300 {
		return string(rs[:297]) + "..."
	}
	return s
}
func runPDBFixRuntime(parent context.Context, out, errout io.Writer, o options, r pdbfix.Runtime, f analyzer.Finding) int {
	if e := validatePDBOptions(o, f); e != nil {
		fmt.Fprintln(errout, e)
		return 2
	}
	duration := o.timeout
	if duration <= 0 {
		duration = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, duration)
	defer cancel()
	if o.pdbPolicy == "" {
		next := "Provide --pdb-policy <local-file> only when a correction is required and availability intent is independently approved."
		if len(f.Recommendations) > 0 {
			next = f.Recommendations[0].Description
		}
		data := map[string]any{"resource": f.ResourceKind + "/" + f.ResourceName, "namespace": f.Namespace, "status": f.Status, "cause": f.Summary, "next": next, "scope": "diagnosis-only"}
		if f.AI != nil && !f.AI.Unstructured {
			data["ai"] = f.AI
		}
		if o.proof || o.verbose {
			data["evidence"] = f.Evidence
		}
		if o.output != "text" {
			return output.Write(out, o.output, data)
		}
		fmt.Fprintf(out, "%s/%s (%s): %s\nCause: %s\n", f.ResourceKind, f.ResourceName, f.Namespace, f.Status, pdbBrief(f.Summary))
		printPDBAI(out, f, o)
		if o.proof || o.verbose {
			for _, e := range f.Evidence {
				fmt.Fprintf(out, "  %s: %s\n", e.Label, e.Value)
			}
		}
		fmt.Fprintf(out, "Next: %s\nNo repair proposed; no changes made.\n", pdbBrief(next))
		return 0
	}
	a, e := pdbfix.LoadPolicy(o.pdbPolicy)
	if e != nil {
		fmt.Fprintln(errout, "PDB policy:", e)
		return 1
	}
	if _, e = a.ProposalContext(f.Namespace, f.ResourceName); e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	proposal := ""
	if f.AI != nil && !f.AI.Unstructured {
		proposal = f.AI.PatchYAML
	}
	p, e := pdbfix.Prepare(ctx, r, a, proposal)
	if e != nil {
		fmt.Fprintf(errout, "PDB repair requires review: %v\nNo changes made.\n", e)
		return 1
	}
	if o.output != "text" {
		return output.Write(out, o.output, p)
	}
	fmt.Fprintf(out, "%s (%s): %s\nCause: %s\n", p.Resource, p.Namespace, f.Status, pdbBrief(f.Summary))
	printPDBAI(out, f, o)
	fmt.Fprintf(out, "Proposed correction (review):\n%s", p.Diff)
	if o.proof || o.verbose {
		fmt.Fprintln(out, "Proof: approved availability limits; exact owned stable healthy replicas; complete PDB and autoscaler inventories. Preflight does not test evictions or guarantee future health.")
		for _, e := range f.Evidence {
			fmt.Fprintf(out, "  %s: %s\n", e.Label, e.Value)
		}
	}
	if o.preview {
		fmt.Fprintln(out, "Preview only; no resources or source files changed.")
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
		fmt.Fprintln(errout, "PDB delivery must be patch, cluster or pr")
		return 2
	}
	if hasHelmSourceContext(o) || mode == "pr" {
		return runPDBSourceReview(ctx, out, errout, o, f, p)
	}
	if mode == "patch" {
		if o.outFile != "" {
			if e = os.WriteFile(o.outFile, []byte(p.Diff), 0600); e != nil {
				fmt.Fprintln(errout, e)
				return 1
			}
			fmt.Fprintf(out, "Wrote reviewed patch: %s\n", o.outFile)
		}
		fmt.Fprintln(out, "Review only; production unchanged. Use --delivery=cluster for an explicitly confirmed unmanaged PDB correction.")
		return 0
	}
	if p.Status == "source-review-required" {
		fmt.Fprintln(errout, "Source-managed PDB: direct mutation blocked; use reviewed source delivery.")
		return 1
	}
	if !o.yes && !termui.Confirm("Apply the approved PDB correction after preflight?", inputFor(o), out) {
		fmt.Fprintln(out, "Cancelled; no changes made.")
		return 0
	}
	receipt, e := pdbfix.Verify(ctx, r, p)
	if e != nil {
		fmt.Fprintf(errout, "PDB preflight failed: %v\nNo changes made.\n", e)
		return 1
	}
	fmt.Fprintln(out, "Preflight passed; eviction behavior has not been tested.")
	applied, result, e := pdbfix.Apply(ctx, r, p, receipt)
	fmt.Fprintf(out, "%s: %s\n", result.Status, result.Summary)
	if e != nil {
		fmt.Fprintln(errout, "PDB delivery:", e)
		return 1
	}
	result, e = pdbfix.Observe(ctx, r, p, applied)
	fmt.Fprintf(out, "%s: %s\n", result.Status, result.Summary)
	if e == nil {
		return 0
	}
	fmt.Fprintln(errout, "PDB observation:", e)
	if !o.yes && termui.ConfirmRollback("restore Fixora-edited fields to the original PDB policy (which may not meet the approved repair limits)", inputFor(o), errout) {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.WithoutCancel(parent), 30*time.Second)
		defer rollbackCancel()
		restored, re := pdbfix.Restore(rollbackCtx, r, applied)
		fmt.Fprintf(out, "%s: %s\n", restored.Status, restored.Summary)
		if re != nil {
			fmt.Fprintln(errout, "PDB rollback:", re)
		}
	}
	return 1
}
func printPDBAI(w io.Writer, f analyzer.Finding, o options) {
	if f.AI == nil || f.AI.Unstructured {
		return
	}
	cause := f.AI.RootCause
	if cause == "" {
		cause = f.AI.Summary
	}
	if cause != "" {
		if !o.proof && !o.verbose {
			cause = pdbBrief(cause)
		}
		fmt.Fprintln(w, "AI:", cause)
	}
	if (o.proof || o.verbose) && f.AI.RecommendedFix != "" {
		fmt.Fprintln(w, "AI suggestion:", f.AI.RecommendedFix)
	}
}
func withPDBProposalIntent(f analyzer.Finding, path string) (analyzer.Finding, error) {
	if f.ResourceKind != "PodDisruptionBudget" {
		return f, errors.New("PDB policy requires exact PDB target")
	}
	a, e := pdbfix.LoadPolicy(path)
	if e != nil {
		return f, e
	}
	data, e := a.ProposalContext(f.Namespace, f.ResourceName)
	if e != nil {
		return f, e
	}
	f.Evidence = append(f.Evidence, analyzer.Evidence{Label: "Approved PDB intent (proposal context only)", Value: data})
	return f, nil
}
func withPDBWorkloadContext(ctx context.Context, r kube.Reader, f analyzer.Finding, path string) analyzer.Finding {
	a, e := pdbfix.LoadPolicy(path)
	if e != nil {
		return f
	}
	target := a.Target()
	wf := f
	wf.ResourceKind = target.Kind
	wf.ResourceName = target.Name
	wf = withAIWorkloadContext(ctx, r, wf)
	f.Evidence = wf.Evidence
	return f
}
