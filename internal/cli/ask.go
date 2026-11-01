package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/asksession"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/output"
)

type targetResolver interface {
	ResolveTarget(context.Context, string, []ai.IntentCandidate) (ai.TargetIntent, error)
}

type askPreviewer func(context.Context, options, analyzer.Finding, string) int

type askReport struct {
	Status         string                   `json:"status"`
	Request        string                   `json:"request"`
	Interpretation string                   `json:"interpretation,omitempty"`
	Intent         ai.TargetIntent          `json:"intent,omitempty"`
	Target         *ai.IntentCandidate      `json:"target,omitempty"`
	Candidates     []ai.IntentCandidate     `json:"candidates,omitempty"`
	PreviewOnly    bool                     `json:"previewOnly"`
	NoChangesMade  bool                     `json:"noChangesMade"`
	Next           string                   `json:"next,omitempty"`
	SessionID      string                   `json:"sessionId,omitempty"`
	Answer         string                   `json:"answer,omitempty"`
	Observed       []ai.IncidentClaim       `json:"observed,omitempty"`
	LikelyCause    ai.IncidentClaim         `json:"likelyCause,omitempty"`
	Unknowns       []string                 `json:"unknowns,omitempty"`
	FollowUp       string                   `json:"followUp,omitempty"`
	EvidenceRefs   []asksession.EvidenceRef `json:"evidenceRefs,omitempty"`
	AIAvailable    bool                     `json:"aiAvailable"`
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
	continueToPreview := opts.askContinue || opts.askConversation
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
	if opts.output != "text" && !opts.askConversation {
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

type incidentResponder interface {
	RespondToIncident(context.Context, ai.IncidentTurnInput) (ai.IncidentTurnResponse, error)
}

// runAskSessionCommand is the command entry point for resumable Ask sessions.
func runAskSessionCommand(ctx context.Context, stdout, stderr io.Writer, opts options, a analyzer.Analyzer, reader kube.Reader, k kube.Kubectl, request string) int {
	if err := validateAskOptions(opts); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	request = strings.TrimSpace(request)
	if request == "" {
		fmt.Fprintln(stderr, "error: ask requires a natural-language request")
		return 2
	}
	clusterID, err := k.ClusterID(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "error: cannot identify the current Kubernetes cluster for Ask sessions: %v\n", err)
		return 1
	}
	path, err := asksession.DefaultPath(clusterID)
	if err != nil {
		fmt.Fprintf(stderr, "error: cannot create Ask session storage path: %v\n", err)
		return 1
	}
	store := asksession.Store{Path: path}
	if opts.askSession != "" {
		session, loadErr := store.Load(opts.askSession, clusterID)
		if loadErr != nil {
			fmt.Fprintf(stderr, "error: %v\n", loadErr)
			return 1
		}
		if err := askSessionNamespaceError(opts, session.Namespace); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 2
		}
		opts.namespace = session.Namespace
		ref := session.ResourceKind + "/" + session.ResourceName
		resource, getErr := reader.GetResource(ctx, session.Namespace, ref)
		if getErr != nil || resource == nil {
			fmt.Fprintf(stderr, "error: saved target %s/%s could not be revalidated; start a new Ask session\n", session.Namespace, ref)
			return 1
		}
		if currentUID := resourceUID(resource); session.ResourceUID != "" && currentUID != session.ResourceUID {
			fmt.Fprintln(stderr, "error: saved target identity changed; start a new Ask session")
			return 1
		}
		filters := mergeAskFilters(session.AnalyzerFilters, analyzer.SmartFiltersFor(ref, request+" "+session.Status))
		opts.filters = strings.Join(filters, ",")
		a = analyzer.New(reader, askAnalyzerOptions(opts, session.Namespace, filters))
	}

	client, aiErr := ai.NewFromEnv()
	var resolver targetResolver
	var responder incidentResponder
	if aiErr == nil && client.Provider != "noop" {
		resolver = client
		responder = client
		fmt.Fprintf(stderr, "AI provider %s (%s) will receive redacted incident evidence for this turn.\n", client.Provider, client.Model)
	} else if opts.askSession == "" {
		if aiErr != nil {
			fmt.Fprintf(stderr, "error: natural-language ask requires a configured AI provider: %s\n", aiErr.Error())
		} else {
			fmt.Fprintln(stderr, "error: natural-language ask requires a configured AI provider; the noop provider cannot resolve targets")
		}
		return 1
	} else if aiErr != nil {
		fmt.Fprintf(stderr, "warning: AI unavailable (%s); using current deterministic finding evidence.\n", ai.FailureSummary(aiErr))
	}

	report := a.ScanReport(ctx)
	if err := askScanError(report); err != nil {
		fmt.Fprintf(stderr, "error: could not refresh Ask incident evidence because the scan was incomplete: %s\n", err)
		return 1
	}
	return runAskSessionWithFindings(ctx, stdout, stderr, opts, clusterID, store, request, report.Findings, resolver, responder, func(ctx context.Context, previewOpts options, selected analyzer.Finding, ref string) int {
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

func runAskSessionWithFindings(ctx context.Context, stdout, stderr io.Writer, opts options, clusterID string, store asksession.Store, request string, findings []analyzer.Finding, resolver targetResolver, responder incidentResponder, preview askPreviewer) int {
	if err := validateAskOptions(opts); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	request = strings.TrimSpace(request)
	if request == "" {
		fmt.Fprintln(stderr, "error: ask requires a natural-language request")
		return 2
	}
	if opts.askSession == "" {
		if len(findings) == 0 {
			return writeAskNoFinding(stdout, opts, askReport{Status: "no-findings", Request: request, PreviewOnly: true, NoChangesMade: true, Next: "Use `kubectl fixora scan` to inspect the selected scope."})
		}
		conversationOpts := opts
		conversationOpts.askConversation = true
		return runAskWithFindings(ctx, stdout, stderr, conversationOpts, request, findings, resolver, func(turnCtx context.Context, turnOpts options, selected analyzer.Finding, ref string) int {
			return runAskIncidentTurn(turnCtx, stdout, stderr, turnOpts, clusterID, store, request, nil, selected, responder, preview, responder != nil)
		})
	}

	session, err := store.Load(opts.askSession, clusterID)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	var selected *analyzer.Finding
	for i := range findings {
		finding := &findings[i]
		if finding.Namespace == session.Namespace && strings.EqualFold(finding.ResourceKind, session.ResourceKind) && finding.ResourceName == session.ResourceName {
			selected = finding
			break
		}
	}
	if selected == nil {
		if len(session.OwnerChain) > 0 {
			fmt.Fprintln(stderr, "error: cannot verify the saved target owner chain from refreshed findings; refusing to classify this session as recovered")
			return 1
		}
		return runAskSessionRecovered(ctx, stdout, stderr, opts, store, session, request, false)
	}
	if session.ResourceUID != "" && selected.ResourceUID != session.ResourceUID {
		fmt.Fprintln(stderr, "error: saved target identity changed; start a new Ask session")
		return 1
	}
	if len(session.OwnerChain) > 0 && !sameOwnerChain(session.OwnerChain, selected.OwnerChain) {
		fmt.Fprintln(stderr, "error: saved target identity changed because its owner chain changed; start a new Ask session")
		return 1
	}
	if isHealthyAskFinding(selected.Status) {
		return runAskSessionRecovered(ctx, stdout, stderr, opts, store, session, request, true)
	}
	conversationOpts := opts
	conversationOpts.askConversation = true
	return runAskIncidentTurn(ctx, stdout, stderr, conversationOpts, clusterID, store, request, &session, *selected, responder, preview, responder != nil)
}

func runAskIncidentTurn(ctx context.Context, stdout, stderr io.Writer, opts options, clusterID string, store asksession.Store, request string, session *asksession.Session, finding analyzer.Finding, responder incidentResponder, preview askPreviewer, aiAvailable bool) int {
	evidence, refs := askIncidentEvidence(finding)
	input := ai.IncidentTurnInput{
		Request:  request,
		Target:   ai.IntentCandidate{ID: finding.ID, Namespace: finding.Namespace, ResourceKind: finding.ResourceKind, ResourceName: finding.ResourceName, Status: finding.Status, Summary: truncateIntentSummary(finding.Summary, 240)},
		Evidence: evidence, ObservedAt: time.Now().UTC(),
	}
	if session != nil {
		for _, turn := range session.Turns {
			input.PriorTurns = append(input.PriorTurns, ai.PriorIncidentTurn{Request: turn.Request, AnswerSummary: turn.AnswerSummary, FollowUp: turn.FollowUp})
		}
	}
	var response ai.IncidentTurnResponse
	if responder != nil {
		var err error
		response, err = responder.RespondToIncident(ctx, input)
		if err != nil {
			fmt.Fprintf(stderr, "warning: AI unavailable (%s); using current deterministic finding evidence.\n", ai.FailureSummary(err))
			aiAvailable = false
		}
	}
	if !aiAvailable {
		response = deterministicAskResponse(finding, evidence)
	}
	if session == nil {
		session = &asksession.Session{
			ClusterID: clusterID, Namespace: finding.Namespace, ResourceKind: finding.ResourceKind,
			ResourceName: finding.ResourceName, ResourceUID: finding.ResourceUID, OwnerChain: append([]string(nil), finding.OwnerChain...),
			FindingID: finding.ID, Status: finding.Status,
			AnalyzerFilters: analyzer.SmartFiltersFor(finding.ResourceKind+"/"+finding.ResourceName, request+" "+finding.Status),
		}
	} else {
		session.FindingID = finding.ID
		session.Status = finding.Status
		if session.ResourceUID == "" {
			session.ResourceUID = finding.ResourceUID
		}
		if len(session.OwnerChain) == 0 && len(finding.OwnerChain) > 0 {
			session.OwnerChain = append([]string(nil), finding.OwnerChain...)
		}
		session.AnalyzerFilters = mergeAskFilters(session.AnalyzerFilters, analyzer.SmartFiltersFor(finding.ResourceKind+"/"+finding.ResourceName, request+" "+finding.Status))
	}
	session.EvidenceRefs = refs
	session.OpenQuestion = response.FollowUp
	session.Summary = truncateIntentSummary(response.Answer, 256)
	session.Turns = append(session.Turns, asksession.Turn{Request: request, AnswerSummary: response.Answer, FollowUp: response.FollowUp, At: time.Now().UTC()})
	if session.ID == "" {
		created, err := store.Create(*session)
		if err != nil {
			fmt.Fprintf(stderr, "error creating Ask session: %v\n", err)
			return 1
		}
		*session = created
	} else if err := store.Save(*session); err != nil {
		fmt.Fprintf(stderr, "error saving Ask session; prior session contents were preserved: %v\n", err)
		return 1
	}

	target := ai.IntentCandidate{ID: finding.ID, Namespace: finding.Namespace, ResourceKind: finding.ResourceKind, ResourceName: finding.ResourceName, Status: finding.Status}
	if code := writeAskConversation(stdout, opts, request, *session, target, response, refs, aiAvailable); code != 0 {
		return code
	}
	if response.PreviewRequested || opts.askContinue {
		if preview == nil {
			fmt.Fprintln(stderr, "error: guarded fix preview is unavailable")
			return 1
		}
		previewOpts := guardedAskPreviewOptions(opts)
		return preview(ctx, previewOpts, finding, finding.ResourceKind+"/"+finding.ResourceName)
	}
	return 0
}

func runAskSessionRecovered(ctx context.Context, stdout, stderr io.Writer, opts options, store asksession.Store, session asksession.Session, request string, healthy bool) int {
	answer := "No active finding was returned by the selected analyzers on this turn. This does not prove the workload is healthy; the current checks did not reproduce the original incident."
	if healthy {
		answer = "The selected analyzers now report the workload as healthy. This reflects the checked signals and does not guarantee application-level health."
	}
	session.Status = "Recovered"
	session.OpenQuestion = ""
	session.Summary = answer
	session.Turns = append(session.Turns, asksession.Turn{Request: request, AnswerSummary: answer, At: time.Now().UTC()})
	if err := store.Save(session); err != nil {
		fmt.Fprintf(stderr, "error saving Ask session; prior session contents were preserved: %v\n", err)
		return 1
	}
	report := askReport{Status: "recovered", Request: request, SessionID: session.ID, Answer: answer, Target: &ai.IntentCandidate{Namespace: session.Namespace, ResourceKind: session.ResourceKind, ResourceName: session.ResourceName, Status: session.Status}, PreviewOnly: true, NoChangesMade: true, AIAvailable: false, Next: "Resume this session later or run a broader scan to check other signals."}
	if opts.output != "" && opts.output != "text" {
		return output.Write(stdout, opts.output, report)
	}
	fmt.Fprintf(stdout, "Incident session: %s\n%s/%s (%s)\n%s\nNo changes made.\n", session.ID, session.ResourceKind, session.ResourceName, session.Namespace, answer)
	return 0
}

func deterministicAskResponse(finding analyzer.Finding, evidence []ai.IncidentEvidence) ai.IncidentTurnResponse {
	answer := "AI is unavailable. Current analyzer finding: " + analyzer.RedactFindingForAI(finding).Summary
	response := ai.IncidentTurnResponse{Answer: answer, Observed: []ai.IncidentClaim{}, LikelyCause: ai.IncidentClaim{EvidenceIDs: []string{}}, Unknowns: []string{"This deterministic finding summary is not a complete root-cause assessment."}, FollowUp: ""}
	if len(evidence) > 0 {
		response.Observed = []ai.IncidentClaim{{Text: analyzer.RedactFindingForAI(finding).Summary, EvidenceIDs: []string{evidence[0].ID}}}
	}
	return response
}

func askIncidentEvidence(finding analyzer.Finding) ([]ai.IncidentEvidence, []asksession.EvidenceRef) {
	safe := analyzer.RedactFindingForAI(finding)
	now := time.Now().UTC()
	resource := safe.ResourceKind + "/" + safe.ResourceName
	var evidence []ai.IncidentEvidence
	appendEvidence := func(signal, value, source string) {
		if len(evidence) >= 64 {
			return
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		id := fmt.Sprintf("ev-%02d", len(evidence)+1)
		value = truncateIntentSummary(value, 2048)
		evidence = append(evidence, ai.IncidentEvidence{ID: id, Resource: source, Signal: truncateIntentSummary(signal, 128), Value: value, ObservedAt: now})
	}
	for _, item := range safe.Evidence {
		appendEvidence(item.Label, item.Value, resource)
	}
	for _, item := range safe.Logs {
		appendEvidence(item.Source, item.Text, firstAskValue(safe.PodName, resource))
	}
	if len(evidence) == 0 {
		appendEvidence("findingSummary", safe.Summary, resource)
	}
	refs := make([]asksession.EvidenceRef, 0, len(evidence))
	for _, item := range evidence {
		refs = append(refs, asksession.EvidenceRef{ID: item.ID, Resource: item.Resource, Signal: item.Signal, At: item.ObservedAt})
	}
	return evidence, refs
}

func writeAskConversation(stdout io.Writer, opts options, request string, session asksession.Session, target ai.IntentCandidate, response ai.IncidentTurnResponse, refs []asksession.EvidenceRef, aiAvailable bool) int {
	report := askReport{Status: "conversation", Request: request, Target: &target, PreviewOnly: true, NoChangesMade: true, SessionID: session.ID, Answer: response.Answer, Observed: response.Observed, LikelyCause: response.LikelyCause, Unknowns: response.Unknowns, FollowUp: response.FollowUp, EvidenceRefs: refs, AIAvailable: aiAvailable, Next: fmt.Sprintf("Continue with `kubectl fixora ask --session %s \"<follow-up>\"`.", session.ID)}
	if opts.output != "" && opts.output != "text" {
		return output.Write(stdout, opts.output, report)
	}
	fmt.Fprintf(stdout, "Incident session: %s\n%s/%s (%s): %s\n%s\n", session.ID, target.ResourceKind, target.ResourceName, target.Namespace, target.Status, response.Answer)
	if !aiAvailable {
		fmt.Fprintln(stdout, "AI unavailable; answer uses deterministic analyzer evidence.")
	}
	if len(response.Observed) > 0 {
		fmt.Fprintln(stdout, "Observed")
		for _, claim := range response.Observed {
			fmt.Fprintf(stdout, "  %s [%s]\n", claim.Text, strings.Join(claim.EvidenceIDs, ", "))
		}
	}
	if strings.TrimSpace(response.LikelyCause.Text) != "" {
		fmt.Fprintf(stdout, "Likely cause\n  %s [%s]\n", response.LikelyCause.Text, strings.Join(response.LikelyCause.EvidenceIDs, ", "))
	}
	if len(response.Unknowns) > 0 {
		fmt.Fprintln(stdout, "Unknown")
		for _, unknown := range response.Unknowns {
			fmt.Fprintf(stdout, "  %s\n", unknown)
		}
	}
	for _, ref := range refs {
		fmt.Fprintf(stdout, "Evidence %s: %s · %s · %s\n", ref.ID, ref.Resource, ref.Signal, ref.At.Format(time.RFC3339))
	}
	if response.FollowUp != "" {
		fmt.Fprintf(stdout, "Follow-up: %s\n", response.FollowUp)
	}
	fmt.Fprintln(stdout, report.Next)
	fmt.Fprintln(stdout, "No changes made.")
	return 0
}

func guardedAskPreviewOptions(opts options) options {
	opts.preview = true
	opts.useAI = true
	opts.noAI = false
	opts.apply = false
	opts.yes = false
	opts.autoFix = false
	opts.sourcePatch = false
	opts.editPatch = false
	opts.commit = false
	opts.shadowVerify = false
	opts.keepShadow = false
	opts.delivery = "patch"
	delete(opts.visited, "delivery")
	return opts
}

func askAnalyzerOptions(opts options, namespace string, filters []string) analyzer.Options {
	return analyzer.Options{Namespace: namespace, IncludeLogs: opts.includeLogs, Redact: opts.redact || opts.paranoid, Filters: filters, CheckSecretKeys: opts.checkSecretKeys, CheckCertExpiry: opts.checkCertExpiry}
}

func mergeAskFilters(existing []string, additional []string) []string {
	out := make([]string, 0, len(existing)+len(additional))
	seen := map[string]bool{}
	for _, filter := range append(append([]string(nil), existing...), additional...) {
		filter = strings.ToLower(strings.TrimSpace(filter))
		if filter != "" && !seen[filter] {
			seen[filter] = true
			out = append(out, filter)
		}
	}
	return out
}

func sameOwnerChain(left, right []string) bool {
	left = workloadOwnerChain(left)
	right = workloadOwnerChain(right)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func workloadOwnerChain(chain []string) []string {
	if len(chain) > 0 && strings.HasPrefix(chain[0], "Pod/") {
		return chain[1:]
	}
	return chain
}

func isHealthyAskFinding(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "Healthy") || strings.EqualFold(strings.TrimSpace(status), "OwnedPodsHealthy")
}

func resourceUID(resource map[string]any) string {
	metadata, _ := resource["metadata"].(map[string]any)
	uid, _ := metadata["uid"].(string)
	return uid
}

func firstAskValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "unknown"
}

type askSessionListItem struct {
	ID        string    `json:"id"`
	Target    string    `json:"target"`
	UpdatedAt time.Time `json:"updatedAt"`
	State     string    `json:"state"`
}

// runAskSessionsCommand manages local sessions without exposing conversation text.
func runAskSessionsCommand(stdout, stderr io.Writer, clusterID string, store asksession.Store, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: kubectl fixora ask sessions list | delete <ID>")
		return 2
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: kubectl fixora ask sessions list")
			return 2
		}
		sessions, err := store.List(clusterID)
		if err != nil {
			fmt.Fprintf(stderr, "error listing Ask sessions: %v\n", err)
			return 1
		}
		if len(sessions) == 0 {
			fmt.Fprintln(stdout, "No saved Ask sessions for this cluster.")
			return 0
		}
		fmt.Fprintln(stdout, "ID            TARGET                              LAST ACTIVITY          STATE")
		for _, session := range sessions {
			state := session.Status
			if state == "" {
				state = "open"
			}
			target := session.Namespace + "/" + session.ResourceKind + "/" + session.ResourceName
			fmt.Fprintf(stdout, "%-12s  %-35s  %-20s  %s\n", session.ID, target, session.UpdatedAt.Format(time.RFC3339), state)
		}
		return 0
	case "delete":
		if len(args) != 2 || !regexpAskSessionID(args[1]) {
			fmt.Fprintln(stderr, "usage: kubectl fixora ask sessions delete <ID>")
			return 2
		}
		if err := store.Delete(args[1], clusterID); err != nil {
			fmt.Fprintf(stderr, "error deleting Ask session: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Deleted Ask session %s.\n", args[1])
		return 0
	default:
		fmt.Fprintln(stderr, "usage: kubectl fixora ask sessions list | delete <ID>")
		return 2
	}
}

func regexpAskSessionID(id string) bool {
	if len(id) != 12 {
		return false
	}
	for _, char := range id {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func askSessionNamespaceError(opts options, sessionNamespace string) error {
	explicit := opts.visited["namespace"] || opts.visited["n"]
	if explicit && opts.namespace != sessionNamespace {
		return fmt.Errorf("session targets namespace %q; omit -n or use that namespace", sessionNamespace)
	}
	return nil
}
