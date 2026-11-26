package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/asksession"
	"github.com/fixora/kubectl-fixora/internal/output"
)

const maxAskScopeResources = 32

type askScopeDescriptor struct {
	Namespace     string `json:"namespace,omitempty"`
	AllNamespaces bool   `json:"allNamespaces"`
}

type scopeResponder interface {
	RespondToScope(context.Context, ai.ScopeTurnInput) (ai.ScopeTurnResponse, error)
}

func scopeResponderFor(responder incidentResponder) scopeResponder {
	if scope, ok := responder.(scopeResponder); ok {
		return scope
	}
	return nil
}

func askScopeSessionError(opts options, session asksession.Session) error {
	if session.Scope == nil {
		return fmt.Errorf("saved Ask session is not bound to a scope")
	}
	if err := askSessionNamespaceError(opts, session.Namespace); err != nil {
		return err
	}
	allNSExplicit := opts.visited["all-namespaces"] || opts.visited["A"]
	if allNSExplicit && opts.allNS != session.Scope.AllNamespaces {
		return fmt.Errorf("session targets %s; do not change --all-namespaces while resuming it", askScopeLabel(sessionScopeDescriptor(session)))
	}
	selectorExplicit := opts.visited["selector"] || opts.visited["l"]
	if selectorExplicit && opts.labelSelector != session.Scope.LabelSelector {
		return fmt.Errorf("session uses its saved label selector; start a new Ask session to change the scope")
	}
	return nil
}

func askScopeEvidence(findings []analyzer.Finding, coverage []string) ([]ai.IncidentEvidence, []asksession.EvidenceRef) {
	ordered := append([]analyzer.Finding(nil), findings...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left := askFindingIdentity(ordered[i])
		right := askFindingIdentity(ordered[j])
		if left != right {
			return left < right
		}
		leftObserved := strings.EqualFold(ordered[i].Status, "Observed")
		rightObserved := strings.EqualFold(ordered[j].Status, "Observed")
		return !leftObserved && rightObserved
	})

	selected := make([]analyzer.Finding, 0, maxAskScopeResources)
	seenResources := make(map[string]bool, maxAskScopeResources)
	for _, finding := range ordered {
		identity := askFindingIdentity(finding)
		if seenResources[identity] {
			continue
		}
		if len(selected) == maxAskScopeResources {
			coverage = append(append([]string(nil), coverage...), "scope context capped at 32 resources; additional resources were not included")
			break
		}
		seenResources[identity] = true
		selected = append(selected, finding)
	}
	if len(selected) == 0 {
		return nil, nil
	}
	for i := range selected {
		selected[i].Logs = nil
		safeEvidence := make([]analyzer.Evidence, 0, len(selected[i].Evidence))
		for _, item := range selected[i].Evidence {
			label := strings.ToLower(item.Label)
			if strings.Contains(label, "command") || strings.Contains(label, "args") || strings.Contains(label, "environment value") || strings.Contains(label, "configmap value") || strings.Contains(label, "secret data") {
				continue
			}
			safeEvidence = append(safeEvidence, item)
		}
		selected[i].Evidence = safeEvidence
	}
	evidence, refs := askIncidentEvidence(selected[0], selected[1:]...)
	coverageCount := len(coverage)
	if coverageCount > 8 {
		coverageCount = 8
		coverage = coverage[:coverageCount]
	}
	if len(evidence)+coverageCount > 64 {
		evidence = evidence[:64-coverageCount]
		refs = refs[:len(evidence)]
	}
	evidence, refs = appendAskCoverageEvidence(evidence, refs, coverage)
	return evidence, refs
}

func scopeDescriptor(opts options) askScopeDescriptor {
	if opts.allNS {
		return askScopeDescriptor{AllNamespaces: true}
	}
	return askScopeDescriptor{Namespace: opts.namespace}
}

func sessionScopeDescriptor(session asksession.Session) askScopeDescriptor {
	if session.Scope != nil && session.Scope.AllNamespaces {
		return askScopeDescriptor{AllNamespaces: true}
	}
	return askScopeDescriptor{Namespace: session.Namespace}
}

func deterministicAskScopeResponse(scope askScopeDescriptor, evidence []ai.IncidentEvidence) ai.ScopeTurnResponse {
	where := "namespace " + scope.Namespace
	if scope.AllNamespaces {
		where = "all namespaces"
	}
	response := ai.ScopeTurnResponse{
		Answer:      fmt.Sprintf("AI is unavailable. Collected %d bounded evidence signals for %s.", len(evidence), where),
		Observed:    []ai.IncidentClaim{},
		LikelyCause: ai.IncidentClaim{EvidenceIDs: []string{}},
		Unknowns:    []string{"This evidence summary is not a complete cluster-health or root-cause assessment."},
		FollowUp:    "",
	}
	limit := len(evidence)
	if limit > 3 {
		limit = 3
	}
	for _, item := range evidence[:limit] {
		response.Observed = append(response.Observed, ai.IncidentClaim{Text: item.Resource + ": " + item.Signal + " — " + item.Value, EvidenceIDs: []string{item.ID}})
	}
	return response
}

func runAskScopeTurn(ctx context.Context, stdout, stderr io.Writer, opts options, clusterID string, store asksession.Store, request string, session *asksession.Session, findings []analyzer.Finding, responder scopeResponder, aiAvailable bool) int {
	evidence, refs := askScopeEvidence(findings, opts.askCoverage)
	scope := scopeDescriptor(opts)
	if session != nil {
		scope = sessionScopeDescriptor(*session)
	}
	if len(evidence) == 0 {
		report := askReport{Status: "no-evidence", Request: request, Scope: &scope, PreviewOnly: true, NoChangesMade: true, AIAvailable: false, Coverage: append([]string(nil), opts.askCoverage...), Next: "No safe live evidence was collected for this scope; narrow the question or review scan permissions."}
		if opts.output != "text" {
			return output.Write(stdout, opts.output, report)
		}
		fmt.Fprintf(stdout, "No usable live evidence for %s. No changes made.\n", askScopeLabel(scope))
		writeAskCoverage(stdout, report.Coverage)
		fmt.Fprintln(stdout, report.Next)
		return 0
	}

	var prior []ai.PriorIncidentTurn
	if session != nil {
		for _, turn := range session.Turns {
			prior = append(prior, ai.PriorIncidentTurn{Request: turn.Request, AnswerSummary: turn.AnswerSummary, FollowUp: turn.FollowUp})
		}
	}
	input := ai.ScopeTurnInput{Request: request, Namespace: scope.Namespace, AllNamespaces: scope.AllNamespaces, PriorTurns: prior, Evidence: evidence, ObservedAt: time.Now().UTC()}
	response := deterministicAskScopeResponse(scope, evidence)
	if responder != nil {
		aiResponse, err := responder.RespondToScope(ctx, input)
		if err != nil {
			fmt.Fprintf(stderr, "warning: AI unavailable (%s); summarizing bounded scope evidence.\n", ai.FailureSummary(err))
			aiAvailable = false
		} else {
			response = aiResponse
			aiAvailable = true
		}
	} else {
		aiAvailable = false
	}
	if !aiAvailable {
		response = deterministicAskScopeResponse(scope, evidence)
	}

	if session == nil {
		namespace := scope.Namespace
		if scope.AllNamespaces {
			namespace = "cluster"
		}
		session = &asksession.Session{ClusterID: clusterID, Namespace: namespace, Scope: &asksession.Scope{AllNamespaces: scope.AllNamespaces, LabelSelector: opts.labelSelector}, Status: "ScopeInvestigation"}
	} else if session.Scope == nil {
		fmt.Fprintln(stderr, "error: saved Ask session is not a scope conversation")
		return 1
	}
	session.Scope = &asksession.Scope{AllNamespaces: scope.AllNamespaces, LabelSelector: opts.labelSelector}
	session.AnalyzerFilters = append([]string(nil), opts.askScopeFilters...)
	session.EvidenceRefs = refs
	session.OpenQuestion = response.FollowUp
	session.Summary = truncateIntentSummary(response.Answer, 256)
	session.Turns = append(session.Turns, asksession.Turn{Request: request, AnswerSummary: response.Answer, FollowUp: response.FollowUp, At: time.Now().UTC()})
	if session.ID == "" {
		created, err := store.Create(*session)
		if err != nil {
			fmt.Fprintf(stderr, "error creating Ask scope session: %v\n", err)
			return 1
		}
		*session = created
	} else if err := store.Save(*session); err != nil {
		fmt.Fprintf(stderr, "error saving Ask scope session; prior session contents were preserved: %v\n", err)
		return 1
	}
	return writeAskScopeConversation(stdout, opts, request, *session, scope, response, refs, aiAvailable)
}

func askScopeLabel(scope askScopeDescriptor) string {
	if scope.AllNamespaces {
		return "all namespaces"
	}
	return "namespace " + scope.Namespace
}

func writeAskScopeConversation(stdout io.Writer, opts options, request string, session asksession.Session, scope askScopeDescriptor, response ai.ScopeTurnResponse, refs []asksession.EvidenceRef, aiAvailable bool) int {
	report := askReport{
		Status: "scope-conversation", Request: request, Scope: &scope, PreviewOnly: true, NoChangesMade: true,
		SessionID: session.ID, Answer: response.Answer, Observed: response.Observed, LikelyCause: response.LikelyCause,
		Unknowns: response.Unknowns, FollowUp: response.FollowUp, EvidenceRefs: refs, AIAvailable: aiAvailable,
		Coverage: append([]string(nil), opts.askCoverage...), Next: fmt.Sprintf("Continue with `kubectl fixora ask --session %s \"<follow-up>\"`.", session.ID),
	}
	if askWantsFix(request) {
		report.Next = "No active finding was selected, so no guarded repair preview is available. Name a workload with an active finding to preview a fix."
	}
	if opts.output != "" && opts.output != "text" {
		return output.Write(stdout, opts.output, report)
	}
	fmt.Fprintf(stdout, "Ask scope: %s\n%s\n", askScopeLabel(scope), response.Answer)
	writeAskCoverage(stdout, report.Coverage)
	if !aiAvailable {
		fmt.Fprintln(stdout, "AI unavailable; showing a deterministic summary of collected evidence.")
	}
	claimLimit, evidenceLimit := 3, 5
	if opts.proof {
		claimLimit, evidenceLimit = len(response.Observed), len(refs)
	}
	for i, claim := range response.Observed {
		if i >= claimLimit {
			fmt.Fprintf(stdout, "%d additional observations; use --proof for details.\n", len(response.Observed)-claimLimit)
			break
		}
		fmt.Fprintf(stdout, "Observed: %s [%s]\n", claim.Text, strings.Join(claim.EvidenceIDs, ", "))
	}
	if response.LikelyCause.Text != "" {
		fmt.Fprintf(stdout, "Likely cause: %s [%s]\n", response.LikelyCause.Text, strings.Join(response.LikelyCause.EvidenceIDs, ", "))
	}
	for i, unknown := range response.Unknowns {
		if !opts.proof && i >= 3 {
			fmt.Fprintf(stdout, "%d additional unknowns; use --proof for details.\n", len(response.Unknowns)-i)
			break
		}
		fmt.Fprintf(stdout, "Unknown: %s\n", unknown)
	}
	incidentResponse := ai.IncidentTurnResponse{Observed: response.Observed, LikelyCause: response.LikelyCause}
	for _, ref := range askEvidenceRefsForDisplay(incidentResponse, refs, evidenceLimit) {
		fmt.Fprintf(stdout, "Evidence %s: %s · %s · %s\n", ref.ID, ref.Resource, ref.Signal, ref.At.Format(time.RFC3339))
	}
	if response.FollowUp != "" {
		fmt.Fprintf(stdout, "Follow-up: %s\n", response.FollowUp)
	}
	fmt.Fprintln(stdout, report.Next)
	fmt.Fprintln(stdout, "No changes made.")
	return 0
}
