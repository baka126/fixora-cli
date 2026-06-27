package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/memory"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"github.com/fixora/kubectl-fixora/internal/termui"
)

// interactiveFix reports whether the guided fix should run as the staged,
// interactive walkthrough rather than the scripted/non-interactive fallback.
func interactiveFix(opts options) bool {
	if opts.output != "text" || opts.preview || opts.yes {
		return false
	}
	if opts.promptInput != nil {
		return true
	}
	return isTerminal(os.Stdin)
}

// runFixWalkthrough drives the four-step guided fix: root cause, proposed fix,
// shadow validation, and delivery. Non-interactive runs use the fallback in
// runGuidedFix instead.
func runFixWalkthrough(ctx context.Context, stdout, stderr io.Writer, opts options, k kube.Kubectl, finding analyzer.Finding, plan fix.Plan, resourceArg string) int {
	uiOpts := termui.Options{Wide: true, NoColor: opts.noColor}
	in := inputFor(opts)

	// Step 1/4 — Root cause (proof hidden until [w]).
	fmt.Fprintln(stdout, "Step 1/4  Root cause")
	termui.Why(stdout, finding, plan, false, uiOpts)
	switch termui.PromptStep("Continue to the proposed fix?", in, stdout) {
	case termui.StepQuit:
		return 0
	case termui.StepShowProof:
		fmt.Fprintln(stdout)
		termui.Proof(stdout, finding, uiOpts)
		if termui.PromptStep("Continue to the proposed fix?", in, stdout) == termui.StepQuit {
			return 0
		}
	}

	// Step 2/4 — Proposed fix.
	fmt.Fprintln(stdout, "\nStep 2/4  Proposed fix")
	termui.Plan(stdout, plan, uiOpts)
	if strings.TrimSpace(plan.PatchYAML()) != "" {
		fmt.Fprint(stdout, termui.DisplayDiff(shadow.PatchDiff(plan.Resource, plan.PatchYAML()), opts.noColor))
	}
	if opts.outFile == "" {
		opts.outFile = "fixora-patch.yaml"
	}

	if !plan.ApplyEligible {
		// Review-only: shadow + direct apply are blocked; deliver via pr/file.
		fmt.Fprintln(stdout, "\nThis patch is review-only; shadow verification and direct apply are blocked:")
		for _, r := range plan.BlockedReasons {
			fmt.Fprintf(stdout, "  - %s\n", r)
		}
		if !hasConcreteReviewPatch(plan) {
			fmt.Fprintln(stdout, "\nNo concrete, safe patch is available yet.")
			if hint := nextConcreteFixHint(resourceArg, plan); hint != "" {
				fmt.Fprintf(stdout, "Next: %s\n", hint)
			}
			return 0
		}
		updated, err := writeReviewPatch(ctx, stdout, stderr, opts, plan)
		if err != nil {
			return fail(stderr, err.Error(), "")
		}
		return deliverWalkthrough(ctx, stdout, stderr, opts, k, finding, updated, shadow.Result{}, true)
	}

	// Confirm shadow validation (or just the fix when shadow is disabled), or
	// open the patch in an editor first.
	var proceed, edit bool
	if opts.shadowVerify {
		proceed, edit = termui.ConfirmShadowOrEdit(in, stdout)
	} else {
		proceed, edit = termui.ConfirmProceedOrEdit(in, stdout)
	}
	if !proceed {
		fmt.Fprintln(stdout, "fix cancelled")
		return 0
	}
	// writeReviewPatch owns the write + (optional) editor + ValidateReviewedPatch.
	// Drive its editor via editPatch and suppress its own re-prompt with yes (we
	// already asked). opts is passed by value, so this does not leak elsewhere.
	wopts := opts
	wopts.editPatch = edit
	wopts.yes = true
	updated, err := writeReviewPatch(ctx, stdout, stderr, wopts, plan)
	if err != nil {
		return fail(stderr, err.Error(), "")
	}
	plan = updated

	// Step 3/4 — Shadow validation (verify only; no delivery yet). Only
	// apply-eligible plans reach this branch (review-only routes above), so
	// skipping shadow does not open a non-eligible apply path.
	var result shadow.Result
	if opts.shadowVerify {
		fmt.Fprintln(stdout, "\nStep 3/4  Shadow validation")
		res, verified, code := verifyInShadow(ctx, stdout, stderr, opts, k, finding, plan, false)
		if !verified {
			return code
		}
		fmt.Fprintf(stdout, "Shadow validation PASSED (parity %d%%)\n", res.Parity)
		result = res
	} else {
		fmt.Fprintln(stdout, "\nStep 3/4  Shadow validation")
		fmt.Fprintln(stdout, "  skipped (--no-shadow); delivering an unverified patch.")
	}

	// Step 4/4 — Deliver.
	fmt.Fprintln(stdout, "\nStep 4/4  Deliver")
	return deliverWalkthrough(ctx, stdout, stderr, opts, k, finding, plan, result, false)
}

// deliverWalkthrough prompts for a delivery mode and routes through the shared
// delivery guards + helper. reviewOnly restricts the menu to pr/file. An explicit
// --delivery (or a legacy alias mapped onto it by reconcileDeliveryFlags) skips
// the menu and pre-selects the mode.
func deliverWalkthrough(ctx context.Context, stdout, stderr io.Writer, opts options, k kube.Kubectl, finding analyzer.Finding, plan fix.Plan, result shadow.Result, reviewOnly bool) int {
	// reconcileDeliveryFlags maps legacy --apply/--source-patch/--gitops onto
	// opts.delivery (without setting visited["delivery"]), so a non-default value
	// or an explicit --delivery=patch both count as explicit.
	explicit := opts.visited["delivery"] || opts.delivery != "patch"
	var choice termui.DeliveryChoice
	if explicit {
		switch shadow.DeliveryMode(strings.ToLower(strings.TrimSpace(opts.delivery))) {
		case shadow.DeliveryCluster:
			choice = termui.DeliverCluster
		case shadow.DeliveryPR:
			choice = termui.DeliverPR
		default:
			choice = termui.DeliverPatch
		}
		fmt.Fprintf(stdout, "Delivering via %s (from --delivery).\n", opts.delivery)
	} else {
		choice = termui.PromptDelivery(inputFor(opts), stdout, reviewOnly)
	}
	var mode shadow.DeliveryMode
	switch choice {
	case termui.DeliverCluster:
		if reviewOnly {
			return fail(stderr, "this review-only patch cannot be applied directly", "choose PR or patch-file delivery")
		}
		mode = shadow.DeliveryCluster
	case termui.DeliverPR:
		mode = shadow.DeliveryPR
		if opts.repoPath == "" {
			fmt.Fprint(stdout, "Path to your manifests repo (--repo): ")
			if line, ok := readPromptLine(opts); ok && line != "" {
				opts.repoPath = line
			}
		}
		opts.yes = true // interactive confirmation already given by menu selection
	case termui.DeliverPatch:
		fmt.Fprintf(stdout, "Patch written to %s\n", opts.outFile)
		return 0
	default:
		fmt.Fprintln(stdout, "delivery cancelled")
		return 0
	}
	if code := guardDelivery(stderr, opts, finding, mode); code != 0 {
		return code
	}
	rc := deliverVerifiedFix(ctx, stdout, stderr, opts, k, finding, plan, result, mode)
	if rc == 0 {
		_ = memory.Add(finding, plan, "guided-fix")
	}
	return rc
}

// readPromptLine reads a single line from the configured prompt input.
func readPromptLine(opts options) (string, bool) {
	line, err := bufio.NewReader(inputFor(opts)).ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimSpace(line), true
}
