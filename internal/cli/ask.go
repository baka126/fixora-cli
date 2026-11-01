package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/output"
)

type targetResolver interface {
	ResolveTarget(context.Context, string, []ai.IntentCandidate) (ai.TargetIntent, error)
}

type askPreviewer func(context.Context, options, analyzer.Finding, string) int

type askReport struct {
	Status         string               `json:"status"`
	Request        string               `json:"request"`
	Interpretation string               `json:"interpretation,omitempty"`
	Intent         ai.TargetIntent      `json:"intent,omitempty"`
	Target         *ai.IntentCandidate  `json:"target,omitempty"`
	Candidates     []ai.IntentCandidate `json:"candidates,omitempty"`
	PreviewOnly    bool                 `json:"previewOnly"`
	NoChangesMade  bool                 `json:"noChangesMade"`
	Next           string               `json:"next,omitempty"`
}

func runAskCommand(ctx context.Context, stdout, stderr io.Writer, opts options, a analyzer.Analyzer, reader kube.Reader, k kube.Kubectl, request string) int {
	if err := validateAskOptions(opts); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	request = strings.TrimSpace(request)
	if request == "" {
		fmt.Fprintln(stderr, "error: ask requires a natural-language request")
		return 2
	}
	report := a.ScanReport(ctx)
	if err := askScanError(report); err != nil {
		fmt.Fprintf(stderr, "error: could not resolve an ask target because the scan was incomplete: %s\n", err)
		return 1
	}
	findings := report.Findings
	if len(findings) == 0 {
		return runAskWithFindings(ctx, stdout, stderr, opts, request, findings, nil, nil)
	}
	resolver, err := ai.NewFromEnv()
	if err != nil {
		fmt.Fprintf(stderr, "error: natural-language ask requires a configured AI provider: %s\n", err.Error())
		return 1
	}
	if resolver.Provider == "noop" {
		fmt.Fprintln(stderr, "error: natural-language ask requires a configured AI provider; the noop provider cannot resolve targets")
		return 1
	}
	return runAskWithFindings(ctx, stdout, stderr, opts, request, findings, resolver, func(ctx context.Context, previewOpts options, selected analyzer.Finding, ref string) int {
		analysisCtx, cancel := fixAnalysisContext(ctx, previewOpts.timeout)
		defer cancel()
		finding, plan, prepared, err := prepareFixPlan(analysisCtx, reader, k, previewOpts, ref, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "error preparing guarded preview: %v\n", err)
			return 1
		}
		return runPreparedFix(ctx, stdout, stderr, prepared, reader, k, finding, plan, ref)
	})
}

func askScanError(report analyzer.ScanReport) error {
	if len(report.Skipped) == 0 {
		return nil
	}
	return fmt.Errorf("%s", report.Skipped[0].Reason)
}

func runAskWithFindings(ctx context.Context, stdout, stderr io.Writer, opts options, request string, findings []analyzer.Finding, resolver targetResolver, preview askPreviewer) int {
	if err := validateAskOptions(opts); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	request = strings.TrimSpace(request)
	if request == "" {
		fmt.Fprintln(stderr, "error: ask requires a natural-language request")
		return 2
	}
	if len(findings) == 0 {
		return writeAskNoFinding(stdout, opts, askReport{Status: "no-findings", Request: request, PreviewOnly: true, NoChangesMade: true, Next: "Use `kubectl fixora scan` to inspect the selected scope."})
	}
	if resolver == nil {
		fmt.Fprintln(stderr, "error: natural-language target resolver is unavailable")
		return 1
	}
	if preview == nil {
		fmt.Fprintln(stderr, "error: guarded fix preview is unavailable")
		return 1
	}
	if len(findings) > aiIntentCandidateLimit {
		fmt.Fprintf(stderr, "error: %d findings exceed the natural-language target limit of %d; narrow the request with --namespace or --filter\n", len(findings), aiIntentCandidateLimit)
		return 2
	}
	ordered := append([]analyzer.Finding(nil), findings...)
	sort.Slice(ordered, func(i, j int) bool {
		left := ordered[i].Namespace + "/" + ordered[i].ResourceKind + "/" + ordered[i].ResourceName + "/" + ordered[i].Status + "/" + ordered[i].ID
		right := ordered[j].Namespace + "/" + ordered[j].ResourceKind + "/" + ordered[j].ResourceName + "/" + ordered[j].Status + "/" + ordered[j].ID
		return left < right
	})
	candidates := make([]ai.IntentCandidate, 0, len(ordered))
	findingByID := make(map[string]analyzer.Finding, len(ordered))
	for i, finding := range ordered {
		candidate := ai.IntentCandidate{
			ID:           finding.ID,
			Namespace:    finding.Namespace,
			ResourceKind: finding.ResourceKind,
			ResourceName: finding.ResourceName,
			Status:       finding.Status,
			Summary:      truncateIntentSummary(finding.Summary, 240),
		}
		if strings.TrimSpace(candidate.ID) == "" || strings.TrimSpace(candidate.Namespace) == "" || strings.TrimSpace(candidate.ResourceKind) == "" || strings.TrimSpace(candidate.ResourceName) == "" || strings.TrimSpace(candidate.Status) == "" {
			fmt.Fprintf(stderr, "error: analyzer finding %d has incomplete resource identity; no target was sent to AI\n", i+1)
			return 1
		}
		if _, exists := findingByID[candidate.ID]; exists {
			fmt.Fprintln(stderr, "error: analyzer returned duplicate finding identities; no target was sent to AI")
			return 1
		}
		candidates = append(candidates, candidate)
		findingByID[candidate.ID] = finding
	}
	intent, err := resolver.ResolveTarget(ctx, request, candidates)
	if err != nil {
		message := ai.FailureSummary(err)
		if strings.Contains(err.Error(), "target resolution") || strings.Contains(err.Error(), "candidate list contains") {
			message = err.Error()
		}
		fmt.Fprintf(stderr, "error: AI target resolution failed: %s\n", message)
		return 1
	}
	if intent.Interpretation == "" || intent.CandidateIDs == nil {
		fmt.Fprintln(stderr, "error: AI target response was incomplete; no preview started")
		return 1
	}
	selectedIDs := make([]string, 0, len(intent.CandidateIDs))
	seen := make(map[string]struct{}, len(intent.CandidateIDs))
	for _, id := range intent.CandidateIDs {
		if _, ok := findingByID[id]; !ok {
			fmt.Fprintln(stderr, "error: AI selected a finding outside the live candidate set; no preview started")
			return 1
		}
		if _, ok := seen[id]; ok {
			fmt.Fprintln(stderr, "error: AI selected a finding more than once; no preview started")
			return 1
		}
		seen[id] = struct{}{}
		selectedIDs = append(selectedIDs, id)
	}
	if len(selectedIDs) == 0 {
		return writeAskNoFinding(stdout, opts, askReport{Status: "no-match", Request: request, Interpretation: intent.Interpretation, Intent: intent, PreviewOnly: true, NoChangesMade: true, Next: "Name a workload or narrow the scope, then try `kubectl fixora ask` again."})
	}
	matches := make([]ai.IntentCandidate, 0, len(selectedIDs))
	for _, candidate := range candidates {
		if _, ok := seen[candidate.ID]; ok {
			matches = append(matches, candidate)
		}
	}
	selected, code := chooseAskTarget(ctx, stdout, stderr, opts, request, intent, matches, findingByID)
	if code != 0 {
		return code
	}
	if selected == nil {
		return 0
	}
	if opts.output == "text" {
		fmt.Fprintf(stdout, "Fixora understood: %s\nTarget: %s/%s (%s)\n", intent.Interpretation, selected.ResourceKind, selected.ResourceName, selected.Namespace)
	}
	continueToPreview := opts.askContinue
	if !continueToPreview && opts.output == "text" && opts.interactive {
		continueToPreview = promptAskPreview(opts, stdout)
		if !continueToPreview {
			fmt.Fprintln(stdout, "No preview started; no changes made.")
			return 0
		}
	}
	if !continueToPreview {
		report := askReport{Status: "interpreted", Request: request, Interpretation: intent.Interpretation, Intent: intent, Target: selected, PreviewOnly: true, NoChangesMade: true, Next: "Add --continue to render the guarded fix preview."}
		return writeAskNoFinding(stdout, opts, report)
	}
	if opts.output != "text" {
		fmt.Fprintf(stderr, "Fixora understood: %s; target %s/%s in namespace %s. Rendering guarded preview only.\n", intent.Interpretation, selected.ResourceKind, selected.ResourceName, selected.Namespace)
	}
	selectedFinding := findingByID[selected.ID]
	ref := selected.ResourceKind + "/" + selected.ResourceName
	previewOpts := opts
	previewOpts.preview = true
	previewOpts.useAI = true
	previewOpts.noAI = false
	previewOpts.apply = false
	previewOpts.yes = false
	previewOpts.autoFix = false
	previewOpts.sourcePatch = false
	previewOpts.editPatch = false
	previewOpts.commit = false
	previewOpts.shadowVerify = false
	previewOpts.keepShadow = false
	previewOpts.delivery = "patch"
	delete(previewOpts.visited, "delivery")
	return preview(ctx, previewOpts, selectedFinding, ref)
}

func chooseAskTarget(ctx context.Context, stdout, stderr io.Writer, opts options, request string, intent ai.TargetIntent, matches []ai.IntentCandidate, findings map[string]analyzer.Finding) (*ai.IntentCandidate, int) {
	if len(matches) == 0 {
		fmt.Fprintln(stderr, "error: AI target resolution returned no valid candidates")
		return nil, 1
	}
	if opts.askSelect != "" {
		choice, err := strconv.Atoi(opts.askSelect)
		if err != nil || choice < 1 || choice > len(matches) {
			fmt.Fprintf(stderr, "error: --select must be a number from 1 to %d for the AI-matched candidates\n", len(matches))
			return nil, 2
		}
		return &matches[choice-1], 0
	}
	if len(matches) == 1 {
		return &matches[0], 0
	}
	report := askReport{Status: "ambiguous", Request: request, Interpretation: intent.Interpretation, Intent: intent, Candidates: matches, PreviewOnly: true, NoChangesMade: true, Next: "Use an interactive terminal to choose a target, or rerun with --select <number>."}
	if opts.output != "text" {
		return nil, output.Write(stdout, opts.output, report)
	}
	fmt.Fprintf(stdout, "Fixora found %d possible targets:\n", len(matches))
	for i, candidate := range matches {
		fmt.Fprintf(stdout, "  %d. %s/%s (%s) — %s\n", i+1, candidate.ResourceKind, candidate.ResourceName, candidate.Namespace, candidate.Status)
	}
	if !opts.interactive {
		fmt.Fprintln(stdout, "No preview started; choose with --select <number> and add --continue.")
		return nil, 2
	}
	fmt.Fprint(stdout, "Select target (1-N): ")
	line, err := bufio.NewReader(inputFor(opts)).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		fmt.Fprintf(stderr, "error: could not read target selection: %v\n", err)
		return nil, 2
	}
	choice, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || choice < 1 || choice > len(matches) {
		fmt.Fprintln(stderr, "error: invalid target selection")
		return nil, 2
	}
	return &matches[choice-1], 0
}

func promptAskPreview(opts options, stdout io.Writer) bool {
	fmt.Fprint(stdout, "Continue to guarded fix preview? [y/N]: ")
	line, err := bufio.NewReader(inputFor(opts)).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func writeAskNoFinding(stdout io.Writer, opts options, report askReport) int {
	if opts.output != "text" {
		return output.Write(stdout, opts.output, report)
	}
	switch report.Status {
	case "no-findings":
		fmt.Fprintln(stdout, "No active findings in the selected scope; no changes made.")
	case "no-match":
		fmt.Fprintf(stdout, "No active finding matched that request: %s\nNo changes made.\n", report.Interpretation)
		fmt.Fprintln(stdout, report.Next)
	case "interpreted":
		fmt.Fprintf(stdout, "Fixora understood: %s\nTarget: %s/%s (%s)\nNo preview started; no changes made.\n%s\n", report.Interpretation, report.Target.ResourceKind, report.Target.ResourceName, report.Target.Namespace, report.Next)
	}
	return 0
}

func validateAskOptions(opts options) error {
	if opts.noAI {
		return fmt.Errorf("ask requires AI; remove --no-ai")
	}
	if opts.apply || opts.yes || opts.autoFix || opts.sourcePatch || opts.editPatch || opts.commit || opts.shadowVerify || opts.keepShadow {
		return fmt.Errorf("ask is preview-only; remove --apply, --yes, --auto-fix, --source-patch, --edit-patch, --commit, --shadow, and --keep-shadow")
	}
	if opts.visited["delivery"] && !strings.EqualFold(strings.TrimSpace(opts.delivery), "patch") {
		return fmt.Errorf("ask is preview-only; use `fix` for cluster or PR delivery")
	}
	return nil
}

func truncateIntentSummary(summary string, maxBytes int) string {
	summary = strings.TrimSpace(summary)
	if len(summary) <= maxBytes {
		return summary
	}
	var builder strings.Builder
	for _, r := range summary {
		if builder.Len()+utf8.RuneLen(r) > maxBytes {
			break
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

const aiIntentCandidateLimit = 50

var _ targetResolver = ai.Client{}
