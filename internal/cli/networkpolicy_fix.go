package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/networkpolicyfix"
	"github.com/fixora/kubectl-fixora/internal/output"
	"github.com/fixora/kubectl-fixora/internal/termui"
)

type networkPolicyReport struct {
	Status        string                         `json:"status"`
	Target        networkpolicyfix.Target        `json:"target"`
	Workload      networkpolicyfix.WorkloadRef   `json:"workload,omitempty"`
	SelectedPods  int                            `json:"selectedPods,omitempty"`
	ChangedFields []string                       `json:"changedFields,omitempty"`
	Verification  *networkpolicyfix.Verification `json:"verification,omitempty"`
	Outcome       *networkpolicyfix.Outcome      `json:"outcome,omitempty"`
	AIExplanation string                         `json:"aiExplanation,omitempty"`
	NoChangesMade bool                           `json:"noChangesMade"`
	Next          string                         `json:"next,omitempty"`
}
func runNetworkPolicyFix(ctx context.Context, out, errout io.Writer, opts options, resource string) int {
	kind, name, ok := strings.Cut(resource, "/")
	if !ok || name == "" || (strings.ToLower(kind) != "networkpolicy" && strings.ToLower(kind) != "netpol") || opts.namespace == "" {
		fmt.Fprintln(errout, "NetworkPolicy repair requires NetworkPolicy/<name> and -n <namespace>")
		return 2
	}
	if opts.networkPolicyPolicy == "" {
		fmt.Fprintln(errout, "NetworkPolicy replacement requires --networkpolicy-policy <local-file>; diagnosis remains read-only.")
		return 2
	}
	if opts.servicePolicy != "" || opts.pvcPolicy != "" || opts.repairPolicy != "" || opts.repairBaseline != "" || opts.startupBaseline != "" || opts.editPatch || opts.repoPath != "" || opts.gitops || hasHelmSourceContext(opts) || opts.outFile != "" {
		fmt.Fprintln(errout, "NetworkPolicy replacement cannot be combined with other repair or source-delivery policies")
		return 2
	}
	if opts.apply && opts.preview {
		fmt.Fprintln(errout, "--apply and --preview cannot be combined")
		return 2
	}
	if opts.apply && opts.output != "text" && !opts.yes {
		fmt.Fprintln(errout, "machine-readable apply requires --yes so confirmation cannot corrupt structured output")
		return 2
	}
	authority, err := networkpolicyfix.LoadAuthority(opts.networkPolicyPolicy)
	if err != nil {
		fmt.Fprintf(errout, "NetworkPolicy authority: %v\nNo changes made.\n", err)
		return 1
	}
	target := networkpolicyfix.Target{Namespace: opts.namespace, Name: name}
	if authority.Target() != target {
		fmt.Fprintln(errout, "NetworkPolicy authority target does not match the requested resource. No changes made.")
		return 1
	}
	typed, err := kube.NewRequiredTypedClient(opts.context, "NetworkPolicy repair")
	if err != nil {
		fmt.Fprintf(errout, "%v\n", err)
		return 1
	}
	runtime, err := networkpolicyfix.NewClient(typed)
	if err != nil {
		fmt.Fprintf(errout, "NetworkPolicy runtime: %v\n", err)
		return 1
	}
	plan, err := networkpolicyfix.Prepare(ctx, runtime, authority, target)
	if err != nil {
		fmt.Fprintf(errout, "NetworkPolicy repair requires review: %v\nNo changes made.\n", err)
		return 1
	}
	report := networkPolicyReport{Status: "preview", Target: target, Workload: plan.Workload, SelectedPods: plan.SelectedPodCount, ChangedFields: plan.ChangedFields, NoChangesMade: true, Next: "Review the exact policy diff, then use --apply to run isolated verification and request apply consent."}
	var aiSummary string
	if opts.useAI {
		aiOptions := opts
		aiOptions.redact = opts.redact || opts.paranoid
		if !aiOptions.redact && !aiOptions.unsafeAI {
			fmt.Fprintln(errout, "AI explanation skipped: add --redact or explicitly opt in with --unsafe-ai-no-redact.")
		}
		finding := analyzer.Finding{ID: "networkpolicy/" + target.Name, Namespace: target.Namespace, ResourceKind: "NetworkPolicy", ResourceName: target.Name, Status: "PolicyReplacementReview", Severity: "medium", Category: "networking", Summary: "Review an exact operator-approved NetworkPolicy spec replacement. AI may explain the evidence but cannot change policy intent or authorize delivery.", Evidence: []analyzer.Evidence{{Label: "Target policy UID", Value: string(plan.Candidate.UID)}, {Label: "Selected workload", Value: plan.Workload.Kind + "/" + plan.Workload.Name}, {Label: "Ready Pod count", Value: fmt.Sprint(plan.SelectedPodCount)}, {Label: "Changed fields", Value: "/spec"}}}
		augmentWithAI(ctx, &finding, aiOptions, errout)
		if finding.AI != nil {
			aiSummary = strings.TrimSpace(finding.AI.RootCause + " " + finding.AI.RecommendedFix)
			report.AIExplanation = aiSummary
		}
	}
	if !opts.apply {
		if opts.output != "text" {
			return output.Write(out, opts.output, report)
		}
		printNetworkPolicyPreview(out, opts, plan, target, aiSummary)
		return 0
	}
	if opts.output == "text" {
		fmt.Fprintf(out, "NetworkPolicy %s/%s\n", target.Namespace, target.Name)
		fmt.Fprintln(out, "Running isolated allow/deny tests for the approved policy spec...")
	}
	verification, err := networkpolicyfix.Verify(ctx, runtime, plan)
	report.Verification = &verification
	if err != nil {
		report.Status = verification.Status
		report.Next = "Resolve the verification or cleanup issue before retrying."
		fmt.Fprintf(errout, "NetworkPolicy verification failed: %v\n", err)
		if opts.output != "text" {
			_ = output.Write(out, opts.output, report)
		} else {
			fmt.Fprintf(out, "Verification: %s. No policy changes made.\n", verification.Status)
		}
		return 1
	}
	if opts.output == "text" {
		fmt.Fprintf(out, "Verified %d declared TCP cases; CNI allow/deny controls passed.\n", len(verification.Cases))
	}
	if !opts.yes && !termui.Confirm("Apply the approved NetworkPolicy spec replacement?", inputFor(opts), out) {
		report.Status = "cancelled"
		report.Next = "Re-run with --apply after reviewing the approved policy diff."
		if opts.output != "text" {
			_ = output.Write(out, opts.output, report)
		} else {
			fmt.Fprintln(out, "Cancelled; no policy changes made.")
		}
		return 0
	}
	applied, err := networkpolicyfix.Apply(ctx, runtime, plan, verification.Receipt)
	if err != nil {
		report.Status = "apply-uncertain"
		report.NoChangesMade = false
		report.Next = "Re-read the exact NetworkPolicy before considering any retry."
		fmt.Fprintf(errout, "NetworkPolicy apply: %v\n", err)
		if opts.output != "text" {
			_ = output.Write(out, opts.output, report)
		}
		return 1
	}
	outcome, _ := networkpolicyfix.Observe(ctx, runtime, applied)
	report.Status = outcome.Status
	report.Outcome = &outcome
	report.NoChangesMade = false
	report.Next = "Review the admitted NetworkPolicy spec and verify application traffic independently; Fixora reports only the declared policy tests."
	if opts.output != "text" {
		return output.Write(out, opts.output, report)
	}
	fmt.Fprintf(out, "Applied NetworkPolicy spec; %s. This does not claim application recovery.\n", outcome.Status)
	fmt.Fprintln(out, report.Next)
	return 0
}
func printNetworkPolicyPreview(out io.Writer, opts options, plan networkpolicyfix.Plan, target networkpolicyfix.Target, aiSummary string) {
	fmt.Fprintf(out, "NetworkPolicy %s/%s\n", target.Namespace, target.Name)
	fmt.Fprintf(out, "Selected workload: %s/%s (%d ready Pods)\n", plan.Workload.Kind, plan.Workload.Name, plan.SelectedPodCount)
	fmt.Fprintln(out, "Change: replace the complete policy spec from the approved local authority.")
	if aiSummary != "" {
		fmt.Fprintf(out, "AI assessment: %s\n", aiSummary)
		fmt.Fprintln(out, "AI is explanatory only; the local authority remains the sole source of policy intent.")
	}
	fmt.Fprintln(out, "Preview only; no changes made.")
	fmt.Fprintln(out, "Next: run with --apply to verify declared traffic cases in disposable namespaces and request confirmation.")
	if opts.proof {
		raw, _ := json.MarshalIndent(plan.Candidate.Spec, "", "  ")
		fmt.Fprintf(out, "\nApproved replacement spec:\n%s\n", raw)
	}
}
