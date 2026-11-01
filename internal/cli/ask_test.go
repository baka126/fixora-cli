package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/asksession"
	"github.com/fixora/kubectl-fixora/internal/kube"
)

type askTestResolver struct {
	intent ai.TargetIntent
	err    error
	calls  int
	query  string
	items  []ai.IntentCandidate
}

func (r *askTestResolver) ResolveTarget(_ context.Context, query string, candidates []ai.IntentCandidate) (ai.TargetIntent, error) {
	r.calls++
	r.query = query
	r.items = append([]ai.IntentCandidate(nil), candidates...)
	return r.intent, r.err
}

func TestAskSelectsExactFindingAndContinuesToPreview(t *testing.T) {
	finding := askFinding("finding-1", "api")
	resolver := &askTestResolver{intent: ai.TargetIntent{CandidateIDs: []string{finding.ID}, Interpretation: "The payments API is failing."}}
	var out, errout bytes.Buffer
	previewCalls := 0
	preview := func(_ context.Context, opts options, got analyzer.Finding, ref string) int {
		previewCalls++
		if got.ID != finding.ID || ref != "Deployment/api" || !opts.preview || !opts.useAI || opts.apply || opts.sourcePatch || opts.yes || opts.autoFix {
			t.Errorf("ask must pass the exact target through an AI-enabled preview-only path: opts=%#v finding=%#v ref=%q", opts, got, ref)
		}
		return 0
	}
	opts := options{output: "text", namespace: "payments", interactive: true, promptInput: bufio.NewReader(strings.NewReader("y\n"))}
	code := runAskWithFindings(context.Background(), &out, &errout, opts, "fix the failing API", []analyzer.Finding{finding}, resolver, preview)
	if code != 0 || previewCalls != 1 || resolver.calls != 1 {
		t.Fatalf("ask failed to reach preview: code=%d calls=%d resolver=%d out=%q err=%q", code, previewCalls, resolver.calls, out.String(), errout.String())
	}
	if !strings.Contains(out.String(), "The payments API is failing.") || !strings.Contains(out.String(), "Deployment/api (payments)") {
		t.Fatalf("ask should show interpretation and exact target: %s", out.String())
	}
	if resolver.query != "fix the failing API" || len(resolver.items) != 1 || resolver.items[0].ID != finding.ID {
		t.Fatalf("resolver received wrong request/candidates: query=%q candidates=%#v", resolver.query, resolver.items)
	}
}

func TestAskProviderToPreviewEndToEnd(t *testing.T) {
	finding := askFinding("finding-1", "api")
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected provider path %q", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"candidateIds\":[\"finding-1\"],\"interpretation\":\"The payments API is crashing.\"}"}}]}`)
	}))
	defer server.Close()
	client := ai.Client{Provider: "openai-compatible", BaseURL: server.URL, Model: "test-model", HTTP: server.Client()}
	var out, errout bytes.Buffer
	previewCalls := 0
	code := runAskWithFindings(context.Background(), &out, &errout, options{output: "text", askContinue: true}, "fix the crashing API", []analyzer.Finding{finding}, client, func(_ context.Context, opts options, selected analyzer.Finding, ref string) int {
		previewCalls++
		if selected.ID != finding.ID || ref != "Deployment/api" || !opts.preview || !opts.useAI || opts.apply || opts.yes || opts.sourcePatch || opts.commit || opts.shadowVerify {
			t.Errorf("provider-selected target must enter the existing guarded preview only: opts=%#v target=%#v ref=%q", opts, selected, ref)
		}
		return 0
	})
	if code != 0 || providerCalls != 1 || previewCalls != 1 {
		t.Fatalf("ask flow failed: code=%d provider calls=%d preview calls=%d stdout=%q stderr=%q", code, providerCalls, previewCalls, out.String(), errout.String())
	}
	if !strings.Contains(out.String(), "The payments API is crashing.") || !strings.Contains(out.String(), "Deployment/api (payments)") {
		t.Fatalf("ask should present provider interpretation and exact target before preview: %s", out.String())
	}
}

func TestAskDoesNotReportNoFindingsWhenScanWasBlocked(t *testing.T) {
	reader := askScanReader{err: errors.New("pods are forbidden")}
	a := analyzer.New(reader, analyzer.Options{Namespace: "payments", Filters: []string{"pod"}})
	var out, errout bytes.Buffer
	code := runAskCommand(context.Background(), &out, &errout, options{output: "text", namespace: "payments"}, a, reader, kube.Kubectl{}, "fix the failing API")
	if code != 1 || !strings.Contains(errout.String(), "scan was incomplete") || strings.Contains(out.String(), "No active findings") {
		t.Fatalf("blocked scan must not be reported as a clean scope: code=%d stdout=%q stderr=%q", code, out.String(), errout.String())
	}
}

func TestAskRejectsPartialScanEvenWhenFindingsExist(t *testing.T) {
	report := analyzer.ScanReport{
		Findings: []analyzer.Finding{askFinding("finding-1", "api")},
		Skipped:  []analyzer.SkippedCheck{{Name: "services", Reason: "services are forbidden"}},
	}
	err := askScanError(report)
	if err == nil || !strings.Contains(err.Error(), "services are forbidden") {
		t.Fatalf("ask should fail closed when scan omitted part of the target scope, got %v", err)
	}
	if got := askScanError(analyzer.ScanReport{Findings: report.Findings}); got != nil {
		t.Fatalf("complete scan with findings should proceed, got %v", got)
	}
}

type askScanReader struct {
	kube.Reader
	err error
}

func (r askScanReader) GetPods(context.Context, string, bool) (kube.PodList, error) {
	return kube.PodList{}, r.err
}

func (askScanReader) GetPod(context.Context, string, string) (kube.Pod, error) {
	return kube.Pod{}, nil
}

func (askScanReader) GetResource(context.Context, string, string) (map[string]any, error) {
	return nil, nil
}

func (askScanReader) GetResourceItems(context.Context, string, bool, string) ([]map[string]any, error) {
	return nil, nil
}

func (askScanReader) GetEvents(context.Context, string, string) ([]kube.Event, error) {
	return nil, nil
}

func (askScanReader) GetNodes(context.Context) ([]kube.Node, error) {
	return nil, nil
}

func (askScanReader) Logs(context.Context, string, string, bool) (string, error) {
	return "", nil
}

func (askScanReader) Run(context.Context, ...string) ([]byte, error) {
	return nil, nil
}

func TestAskAmbiguityRequiresOperatorSelection(t *testing.T) {
	first, second := askFinding("finding-1", "api-a"), askFinding("finding-2", "api-b")
	resolver := &askTestResolver{intent: ai.TargetIntent{CandidateIDs: []string{first.ID, second.ID}, Interpretation: "Two API workloads match."}}
	var out, errout bytes.Buffer
	var selected string
	opts := options{output: "text", namespace: "payments", interactive: true, promptInput: bufio.NewReader(strings.NewReader("2\ny\n"))}
	code := runAskWithFindings(context.Background(), &out, &errout, opts, "fix the API", []analyzer.Finding{first, second}, resolver, func(_ context.Context, _ options, finding analyzer.Finding, _ string) int {
		selected = finding.ID
		return 0
	})
	if code != 0 || selected != second.ID {
		t.Fatalf("operator should select exact second target: code=%d selected=%q output=%s error=%s", code, selected, out.String(), errout.String())
	}
	if !strings.Contains(out.String(), "Deployment/api-a (payments)") || !strings.Contains(out.String(), "Deployment/api-b (payments)") {
		t.Fatalf("ambiguous choices must show exact targets: %s", out.String())
	}
}

func TestAskInvalidAmbiguityChoiceStopsBeforePreview(t *testing.T) {
	one, two := askFinding("finding-1", "api-a"), askFinding("finding-2", "api-b")
	resolver := &askTestResolver{intent: ai.TargetIntent{CandidateIDs: []string{one.ID, two.ID}, Interpretation: "Two API workloads match."}}
	var out, errout bytes.Buffer
	previewCalls := 0
	opts := options{output: "text", interactive: true, promptInput: bufio.NewReader(strings.NewReader("9\n"))}
	code := runAskWithFindings(context.Background(), &out, &errout, opts, "fix API", []analyzer.Finding{one, two}, resolver, func(context.Context, options, analyzer.Finding, string) int { previewCalls++; return 0 })
	if code != 2 || previewCalls != 0 {
		t.Fatalf("invalid selection must stop without preview: code=%d preview=%d stdout=%q stderr=%q", code, previewCalls, out.String(), errout.String())
	}
}

func TestAskCanSelectAnAmbiguousCandidateNonInteractively(t *testing.T) {
	one, two := askFinding("finding-1", "api-a"), askFinding("finding-2", "api-b")
	resolver := &askTestResolver{intent: ai.TargetIntent{CandidateIDs: []string{one.ID, two.ID}, Interpretation: "Two API workloads match."}}
	var out, errout bytes.Buffer
	selected := ""
	opts := options{output: "text", askSelect: "2", askContinue: true}
	code := runAskWithFindings(context.Background(), &out, &errout, opts, "fix API", []analyzer.Finding{one, two}, resolver, func(_ context.Context, opts options, finding analyzer.Finding, _ string) int {
		selected = finding.ID
		if !opts.preview {
			t.Error("selected target must enter preview mode")
		}
		return 0
	})
	if code != 0 || selected != two.ID {
		t.Fatalf("--select should disambiguate by exact finding ID: code=%d selected=%q out=%q err=%q", code, selected, out.String(), errout.String())
	}
}

func TestAskReturnsInterpretationWithoutContinueInNonInteractiveMode(t *testing.T) {
	finding := askFinding("finding-1", "api")
	resolver := &askTestResolver{intent: ai.TargetIntent{CandidateIDs: []string{finding.ID}, Interpretation: "The API is failing."}}
	var out, errout bytes.Buffer
	previewCalls := 0
	code := runAskWithFindings(context.Background(), &out, &errout, options{output: "text"}, "fix API", []analyzer.Finding{finding}, resolver, func(context.Context, options, analyzer.Finding, string) int { previewCalls++; return 0 })
	if code != 0 || previewCalls != 0 || !strings.Contains(out.String(), "--continue") {
		t.Fatalf("non-interactive ask must stop at interpretation unless continued: code=%d preview=%d out=%q err=%q", code, previewCalls, out.String(), errout.String())
	}
}

func TestAskStructuredOutputReturnsInterpretationWithoutContinue(t *testing.T) {
	finding := askFinding("finding-1", "api")
	resolver := &askTestResolver{intent: ai.TargetIntent{CandidateIDs: []string{finding.ID}, Interpretation: "The API is failing."}}
	var out, errout bytes.Buffer
	previewCalls := 0
	code := runAskWithFindings(context.Background(), &out, &errout, options{output: "json"}, "fix API", []analyzer.Finding{finding}, resolver, func(context.Context, options, analyzer.Finding, string) int { previewCalls++; return 0 })
	if code != 0 || previewCalls != 0 || !strings.Contains(out.String(), `"status": "interpreted"`) || !strings.Contains(out.String(), `"candidateIds"`) {
		t.Fatalf("structured output must be interpretation only by default: code=%d preview=%d out=%q err=%q", code, previewCalls, out.String(), errout.String())
	}
}

func TestAskPreviewDeclinedByOperatorDoesNotContinue(t *testing.T) {
	finding := askFinding("finding-1", "api")
	resolver := &askTestResolver{intent: ai.TargetIntent{CandidateIDs: []string{finding.ID}, Interpretation: "The API is failing."}}
	var out, errout bytes.Buffer
	previewCalls := 0
	opts := options{output: "text", interactive: true, promptInput: bufio.NewReader(strings.NewReader("n\n"))}
	code := runAskWithFindings(context.Background(), &out, &errout, opts, "fix API", []analyzer.Finding{finding}, resolver, func(context.Context, options, analyzer.Finding, string) int { previewCalls++; return 0 })
	if code != 0 || previewCalls != 0 || !strings.Contains(out.String(), "No preview started") {
		t.Fatalf("declining should stop before fix preview: code=%d preview=%d out=%q err=%q", code, previewCalls, out.String(), errout.String())
	}
}

func TestAskFailsClosedForNoMatchAndProviderFailure(t *testing.T) {
	finding := askFinding("finding-1", "api")
	for _, tc := range []struct {
		name     string
		resolver *askTestResolver
		findings []analyzer.Finding
		wantCode int
	}{
		{name: "no active findings", resolver: &askTestResolver{}, wantCode: 0},
		{name: "no match", resolver: &askTestResolver{intent: ai.TargetIntent{CandidateIDs: []string{}, Interpretation: "No candidate matches."}}, findings: []analyzer.Finding{finding}, wantCode: 0},
		{name: "provider error", resolver: &askTestResolver{err: errors.New("AI provider unavailable")}, findings: []analyzer.Finding{finding}, wantCode: 1},
		{name: "out of scope ID", resolver: &askTestResolver{intent: ai.TargetIntent{CandidateIDs: []string{"outside"}, Interpretation: "select other workload"}}, findings: []analyzer.Finding{finding}, wantCode: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errout bytes.Buffer
			previewCalls := 0
			code := runAskWithFindings(context.Background(), &out, &errout, options{output: "text", askContinue: true}, "fix API", tc.findings, tc.resolver, func(context.Context, options, analyzer.Finding, string) int { previewCalls++; return 0 })
			if code != tc.wantCode || previewCalls != 0 {
				t.Fatalf("%s must not preview: code=%d want=%d preview=%d stdout=%q stderr=%q", tc.name, code, tc.wantCode, previewCalls, out.String(), errout.String())
			}
		})
	}
}

func TestAskRejectsMutationFlagsBeforeCallingResolver(t *testing.T) {
	finding := askFinding("finding-1", "api")
	for _, tc := range []struct {
		name string
		opts options
	}{
		{name: "apply", opts: options{apply: true}},
		{name: "yes shortcut", opts: options{yes: true}},
		{name: "autofix", opts: options{autoFix: true}},
		{name: "source patch", opts: options{sourcePatch: true}},
		{name: "edit patch", opts: options{editPatch: true}},
		{name: "commit", opts: options{commit: true}},
		{name: "cluster delivery", opts: options{delivery: "cluster", visited: map[string]bool{"delivery": true}}},
		{name: "pr delivery", opts: options{delivery: "pr", visited: map[string]bool{"delivery": true}}},
		{name: "shadow verify", opts: options{shadowVerify: true}},
		{name: "keep shadow", opts: options{keepShadow: true}},
		{name: "disable AI", opts: options{noAI: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &askTestResolver{intent: ai.TargetIntent{CandidateIDs: []string{finding.ID}, Interpretation: "The API is failing."}}
			var out, errout bytes.Buffer
			previewCalls := 0
			code := runAskWithFindings(context.Background(), &out, &errout, tc.opts, "fix API", []analyzer.Finding{finding}, resolver, func(context.Context, options, analyzer.Finding, string) int { previewCalls++; return 0 })
			if code != 2 || resolver.calls != 0 || previewCalls != 0 {
				t.Fatalf("%s must be rejected before resolving or previewing: code=%d resolver=%d preview=%d stdout=%q stderr=%q", tc.name, code, resolver.calls, previewCalls, out.String(), errout.String())
			}
		})
	}
}

func TestAskRejectsDuplicateAndOversizedFindingSets(t *testing.T) {
	for _, tc := range []struct {
		name     string
		findings []analyzer.Finding
	}{
		{name: "duplicate IDs", findings: []analyzer.Finding{askFinding("same", "api-a"), askFinding("same", "api-b")}},
		{name: "more than 50", findings: askManyFindings(51)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &askTestResolver{}
			var out, errout bytes.Buffer
			previewCalls := 0
			code := runAskWithFindings(context.Background(), &out, &errout, options{askContinue: true}, "fix API", tc.findings, resolver, func(context.Context, options, analyzer.Finding, string) int { previewCalls++; return 0 })
			if code == 0 || resolver.calls != 0 || previewCalls != 0 {
				t.Fatalf("invalid candidate set must fail before AI: code=%d resolver=%d preview=%d err=%q", code, resolver.calls, previewCalls, errout.String())
			}
		})
	}
}

type askTestIncidentResponder struct {
	responses []ai.IncidentTurnResponse
	err       error
	calls     int
	inputs    []ai.IncidentTurnInput
}

func (r *askTestIncidentResponder) RespondToIncident(_ context.Context, input ai.IncidentTurnInput) (ai.IncidentTurnResponse, error) {
	r.calls++
	r.inputs = append(r.inputs, input)
	if r.err != nil {
		return ai.IncidentTurnResponse{}, r.err
	}
	if r.calls > len(r.responses) {
		return ai.IncidentTurnResponse{}, errors.New("no test response configured")
	}
	return r.responses[r.calls-1], nil
}

func TestAskSessionStartsAndResumesWithFreshEvidence(t *testing.T) {
	store := asksession.Store{Path: t.TempDir() + "/sessions.json"}
	cluster := strings.Repeat("a", 64)
	finding := askFinding("finding-1", "api")
	finding.ResourceUID = "deployment-uid"
	finding.OwnerChain = []string{"ReplicaSet/api-1", "Deployment/api"}
	finding.Evidence = []analyzer.Evidence{{Label: "Exit code", Value: "1"}}
	resolver := &askTestResolver{intent: ai.TargetIntent{CandidateIDs: []string{finding.ID}, Interpretation: "The API is restarting."}}
	responder := &askTestIncidentResponder{responses: []ai.IncidentTurnResponse{
		{Answer: "The container exits after start.", Observed: []ai.IncidentClaim{{Text: "Exit code is 1.", EvidenceIDs: []string{"ev-1"}}}, LikelyCause: ai.IncidentClaim{Text: "The configured process exits unsuccessfully.", EvidenceIDs: []string{"ev-1"}}, Unknowns: []string{"Intended startup behavior is unknown."}, FollowUp: "Did the command change recently?"},
		{Answer: "The fresh scan still reports a startup failure.", Observed: []ai.IncidentClaim{{Text: "Exit code remains 1.", EvidenceIDs: []string{"ev-1"}}}, LikelyCause: ai.IncidentClaim{Text: "The process still exits unsuccessfully.", EvidenceIDs: []string{"ev-1"}}, Unknowns: []string{}, FollowUp: ""},
	}}
	var out, errout bytes.Buffer
	preview := func(context.Context, options, analyzer.Finding, string) int {
		t.Fatal("non-repair question must not preview")
		return 0
	}
	code := runAskSessionWithFindings(context.Background(), &out, &errout, options{output: "text"}, cluster, store, "Why is api restarting?", []analyzer.Finding{finding}, resolver, responder, preview)
	if code != 0 || responder.calls != 1 {
		t.Fatalf("start failed: code=%d calls=%d stdout=%q stderr=%q", code, responder.calls, out.String(), errout.String())
	}
	listed, err := store.List(cluster)
	if err != nil || len(listed) != 1 {
		t.Fatalf("List() = %#v, %v", listed, err)
	}
	sessionID := listed[0].ID
	session, err := store.Load(sessionID, cluster)
	if err != nil || session.ResourceUID != "deployment-uid" || len(session.OwnerChain) != 2 || len(session.Turns) != 1 {
		t.Fatalf("saved session = %#v, %v", session, err)
	}
	fresh := finding
	fresh.Status = "CrashLoopBackOff"
	fresh.Evidence = []analyzer.Evidence{{Label: "Exit code", Value: "2"}}
	out.Reset()
	errout.Reset()
	code = runAskSessionWithFindings(context.Background(), &out, &errout, options{output: "text", askSession: sessionID}, cluster, store, "What changed since my last question?", []analyzer.Finding{fresh}, nil, responder, preview)
	if code != 0 || responder.calls != 2 {
		t.Fatalf("resume failed: code=%d calls=%d stdout=%q stderr=%q", code, responder.calls, out.String(), errout.String())
	}
	if len(responder.inputs[1].PriorTurns) != 1 || responder.inputs[1].Evidence[0].Value != "2" {
		t.Fatalf("resume did not combine saved conversation with fresh evidence: %#v", responder.inputs[1])
	}
	if !strings.Contains(out.String(), sessionID) || !strings.Contains(out.String(), "The fresh scan still reports") {
		t.Fatalf("resume output omitted session or answer: %s", out.String())
	}
}

func TestAskSessionRejectsChangedUIDOrOwnerChain(t *testing.T) {
	for _, tc := range []struct {
		name   string
		uid    string
		owners []string
	}{
		{name: "UID changed", uid: "replacement-uid", owners: []string{"ReplicaSet/api-1", "Deployment/api"}},
		{name: "owner changed", uid: "deployment-uid", owners: []string{"StatefulSet/api", "Deployment/api"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := asksession.Store{Path: t.TempDir() + "/sessions.json"}
			cluster := strings.Repeat("a", 64)
			session, err := store.Create(asksession.Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", ResourceUID: "deployment-uid", OwnerChain: []string{"ReplicaSet/api-1", "Deployment/api"}, FindingID: "finding-1", Status: "CrashLoopBackOff"})
			if err != nil {
				t.Fatal(err)
			}
			finding := askFinding("finding-1", "api")
			finding.ResourceUID = tc.uid
			finding.OwnerChain = tc.owners
			responder := &askTestIncidentResponder{responses: []ai.IncidentTurnResponse{{Answer: "answer", Observed: []ai.IncidentClaim{}, LikelyCause: ai.IncidentClaim{}, Unknowns: []string{}, FollowUp: ""}}}
			var out, errout bytes.Buffer
			code := runAskSessionWithFindings(context.Background(), &out, &errout, options{askSession: session.ID, output: "text"}, cluster, store, "Tell me more", []analyzer.Finding{finding}, nil, responder, nil)
			if code != 1 || responder.calls != 0 || !strings.Contains(errout.String(), "target identity changed") {
				t.Fatalf("changed target should fail closed: code=%d calls=%d stderr=%q", code, responder.calls, errout.String())
			}
		})
	}
}

func TestAskSessionReportsRecoveryAndRequiresActiveFindingAtStart(t *testing.T) {
	cluster := strings.Repeat("a", 64)
	store := asksession.Store{Path: t.TempDir() + "/sessions.json"}
	var out, errout bytes.Buffer
	code := runAskSessionWithFindings(context.Background(), &out, &errout, options{output: "text"}, cluster, store, "Why is api broken?", nil, &askTestResolver{}, &askTestIncidentResponder{}, nil)
	if code != 0 || !strings.Contains(out.String(), "No active findings") || lenMustList(t, store, cluster) != 0 {
		t.Fatalf("start without active finding created a session: code=%d output=%q", code, out.String())
	}
	created, err := store.Create(asksession.Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", FindingID: "finding-1", Status: "CrashLoopBackOff"})
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errout.Reset()
	code = runAskSessionWithFindings(context.Background(), &out, &errout, options{output: "text", askSession: created.ID}, cluster, store, "Is it healthy now?", nil, nil, &askTestIncidentResponder{}, nil)
	if code != 0 || !strings.Contains(out.String(), "No active finding") {
		t.Fatalf("recovered incident should be reported cautiously: code=%d output=%q error=%q", code, out.String(), errout.String())
	}
}

func TestAskSessionRepairRequestUsesGuardedPreview(t *testing.T) {
	cluster := strings.Repeat("a", 64)
	store := asksession.Store{Path: t.TempDir() + "/sessions.json"}
	finding := askFinding("finding-1", "api")
	finding.Evidence = []analyzer.Evidence{{Label: "Exit", Value: "1"}}
	resolver := &askTestResolver{intent: ai.TargetIntent{CandidateIDs: []string{finding.ID}, Interpretation: "The API is failing."}}
	responder := &askTestIncidentResponder{responses: []ai.IncidentTurnResponse{{Answer: "Preview the guarded fix.", Observed: []ai.IncidentClaim{{Text: "Exit code is 1.", EvidenceIDs: []string{"ev-1"}}}, LikelyCause: ai.IncidentClaim{Text: "The process fails.", EvidenceIDs: []string{"ev-1"}}, Unknowns: []string{}, FollowUp: "", PreviewRequested: true}}}
	var out, errout bytes.Buffer
	previewCalls := 0
	code := runAskSessionWithFindings(context.Background(), &out, &errout, options{output: "text"}, cluster, store, "Please preview a fix", []analyzer.Finding{finding}, resolver, responder, func(_ context.Context, opts options, got analyzer.Finding, ref string) int {
		previewCalls++
		if !opts.preview || opts.apply || opts.yes || opts.sourcePatch || opts.commit || got.ID != finding.ID || ref != "Deployment/api" {
			t.Errorf("Ask must use guarded preview only: opts=%#v finding=%#v ref=%q", opts, got, ref)
		}
		return 0
	})
	if code != 0 || previewCalls != 1 {
		t.Fatalf("guarded preview not invoked: code=%d preview=%d stdout=%q stderr=%q", code, previewCalls, out.String(), errout.String())
	}
	listed, err := store.List(cluster)
	if err != nil || len(listed) != 1 || listed[0].Status != finding.Status {
		t.Fatalf("session missing after preview: %#v %v", listed, err)
	}
}

func TestAskSessionProviderFailureKeepsPriorSession(t *testing.T) {
	cluster := strings.Repeat("a", 64)
	store := asksession.Store{Path: t.TempDir() + "/sessions.json"}
	finding := askFinding("finding-1", "api")
	finding.Evidence = []analyzer.Evidence{{Label: "Exit", Value: "1"}}
	resolver := &askTestResolver{intent: ai.TargetIntent{CandidateIDs: []string{finding.ID}, Interpretation: "The API is failing."}}
	first := &askTestIncidentResponder{responses: []ai.IncidentTurnResponse{{Answer: "The process exits after startup.", Observed: []ai.IncidentClaim{{Text: "Exit code is 1.", EvidenceIDs: []string{"ev-1"}}}, LikelyCause: ai.IncidentClaim{Text: "The process fails.", EvidenceIDs: []string{"ev-1"}}, Unknowns: []string{}, FollowUp: ""}}}
	var out, errout bytes.Buffer
	if code := runAskSessionWithFindings(context.Background(), &out, &errout, options{output: "text"}, cluster, store, "Why is api broken?", []analyzer.Finding{finding}, resolver, first, nil); code != 0 {
		t.Fatalf("start failed: %s", errout.String())
	}
	listed, err := store.List(cluster)
	if err != nil || len(listed) != 1 {
		t.Fatalf("List() = %#v, %v", listed, err)
	}
	out.Reset()
	errout.Reset()
	failed := &askTestIncidentResponder{err: errors.New("AI provider returned HTTP 503")}
	code := runAskSessionWithFindings(context.Background(), &out, &errout, options{output: "text", askSession: listed[0].ID}, cluster, store, "What does the exit code mean?", []analyzer.Finding{finding}, nil, failed, nil)
	if code != 0 || !strings.Contains(out.String(), "AI unavailable") {
		t.Fatalf("provider failure should fall back to current deterministic finding: code=%d output=%q stderr=%q", code, out.String(), errout.String())
	}
	session, err := store.Load(listed[0].ID, cluster)
	if err != nil || len(session.Turns) != 2 || !strings.Contains(session.Turns[1].AnswerSummary, finding.Summary) {
		t.Fatalf("provider failure discarded saved context or fallback: %#v %v", session, err)
	}
}

func TestAskSessionRejectsMissingAndCrossClusterIDs(t *testing.T) {
	cluster := strings.Repeat("a", 64)
	store := asksession.Store{Path: t.TempDir() + "/sessions.json"}
	session, err := store.Create(asksession.Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", FindingID: "finding-1", Status: "CrashLoopBackOff"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		id      string
		cluster string
	}{
		{name: "missing", id: "deadbeefdead", cluster: cluster},
		{name: "wrong cluster", id: session.ID, cluster: strings.Repeat("b", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			responder := &askTestIncidentResponder{}
			var out, errout bytes.Buffer
			code := runAskSessionWithFindings(context.Background(), &out, &errout, options{askSession: tc.id, output: "text"}, tc.cluster, store, "continue", []analyzer.Finding{askFinding("finding-1", "api")}, nil, responder, nil)
			if code != 1 || responder.calls != 0 {
				t.Fatalf("invalid session should fail before AI: code=%d calls=%d stdout=%q stderr=%q", code, responder.calls, out.String(), errout.String())
			}
		})
	}
}

func lenMustList(t *testing.T, store asksession.Store, cluster string) int {
	t.Helper()
	items, err := store.List(cluster)
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}

func askFinding(id, name string) analyzer.Finding {
	return analyzer.Finding{ID: id, Namespace: "payments", ResourceKind: "Deployment", ResourceName: name, Status: "CrashLoopBackOff", Summary: fmt.Sprintf("Deployment %s is failing", name)}
}

func askManyFindings(n int) []analyzer.Finding {
	findings := make([]analyzer.Finding, n)
	for i := range findings {
		findings[i] = askFinding(fmt.Sprintf("finding-%d", i), fmt.Sprintf("api-%d", i))
	}
	return findings
}

func TestAskSessionsListHidesConversationTextAndScopesCluster(t *testing.T) {
	store := asksession.Store{Path: t.TempDir() + "/sessions.json"}
	cluster := strings.Repeat("a", 64)
	other := strings.Repeat("b", 64)
	if _, err := store.Create(asksession.Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", Summary: "must not leak", Turns: []asksession.Turn{{Request: "private request", AnswerSummary: "private answer"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(asksession.Session{ClusterID: other, Namespace: "staging", ResourceKind: "Deployment", ResourceName: "web"}); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	code := runAskSessionsCommand(&out, &errout, cluster, store, []string{"list"})
	if code != 0 || strings.Contains(out.String(), "private request") || strings.Contains(out.String(), "private answer") || strings.Contains(out.String(), "must not leak") || strings.Contains(out.String(), "staging") {
		t.Fatalf("list leaked conversation or other-cluster session: code=%d out=%q err=%q", code, out.String(), errout.String())
	}
	if !strings.Contains(out.String(), "payments/Deployment/api") {
		t.Fatalf("list omitted target reference: %s", out.String())
	}
}

func TestAskSessionsDeleteAndValidateArguments(t *testing.T) {
	store := asksession.Store{Path: t.TempDir() + "/sessions.json"}
	cluster := strings.Repeat("a", 64)
	session, err := store.Create(asksession.Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api"})
	if err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	if code := runAskSessionsCommand(&out, &errout, cluster, store, []string{"delete", session.ID}); code != 0 {
		t.Fatalf("delete failed: %s", errout.String())
	}
	if _, err := store.Load(session.ID, cluster); err == nil {
		t.Fatal("session remains after delete")
	}
	for _, args := range [][]string{{}, {"delete"}, {"delete", "bad-id"}, {"unknown"}, {"list", "extra"}} {
		out.Reset()
		errout.Reset()
		if code := runAskSessionsCommand(&out, &errout, cluster, store, args); code != 2 {
			t.Fatalf("args %v should fail with usage, got %d out=%q err=%q", args, code, out.String(), errout.String())
		}
	}
}

func TestAskSessionNamespaceOverrideDistinguishesDefaultFromExplicit(t *testing.T) {
	parsed, _, err := parseFlags([]string{"--session", "a1b2c3d4e5f6", "-n", "default", "follow-up"})
	if err != nil || !parsed.visited["namespace"] {
		t.Fatalf("short namespace flag should be recorded as explicit: opts=%#v err=%v", parsed, err)
	}
	for _, tc := range []struct {
		name    string
		opts    options
		ns      string
		wantErr bool
	}{
		{name: "implicit default uses saved namespace", opts: options{namespace: "default"}, ns: "payments"},
		{name: "explicit default conflicts", opts: options{namespace: "default", visited: map[string]bool{"namespace": true}}, ns: "payments", wantErr: true},
		{name: "explicit matching namespace", opts: options{namespace: "payments", visited: map[string]bool{"namespace": true}}, ns: "payments"},
		{name: "explicit different namespace", opts: options{namespace: "staging", visited: map[string]bool{"n": true}}, ns: "payments", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := askSessionNamespaceError(tc.opts, tc.ns)
			if (got != nil) != tc.wantErr {
				t.Fatalf("askSessionNamespaceError() = %v, wantErr %v", got, tc.wantErr)
			}
		})
	}
}

func TestAskSessionCannotReportRecoveryWithoutOwnerIdentityWitness(t *testing.T) {
	store := asksession.Store{Path: t.TempDir() + "/sessions.json"}
	cluster := strings.Repeat("d", 64)
	_, err := store.Create(asksession.Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", ResourceUID: "uid-api", OwnerChain: []string{"Pod/api-1", "ReplicaSet/api-1", "Deployment/api"}, FindingID: "incident-1", Status: "CrashLoopBackOff"})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := store.List(cluster)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("List() = %#v, %v", sessions, err)
	}
	var stdout, stderr bytes.Buffer
	code := runAskSessionWithFindings(context.Background(), &stdout, &stderr, options{output: "text", askSession: sessions[0].ID}, cluster, store, "Is it healthy?", nil, nil, nil, nil)
	if code != 1 || !strings.Contains(stderr.String(), "owner chain") || strings.Contains(stdout.String(), "No active finding") {
		t.Fatalf("session without owner identity witness should fail closed: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestAskSessionReportsRecoveryFromHealthyPodAfterOwnerValidation(t *testing.T) {
	store := asksession.Store{Path: t.TempDir() + "/sessions.json"}
	cluster := strings.Repeat("e", 64)
	session, err := store.Create(asksession.Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", ResourceUID: "uid-api", OwnerChain: []string{"Pod/api-old", "ReplicaSet/api-rs", "Deployment/api"}, FindingID: "incident-1", Status: "CrashLoopBackOff"})
	if err != nil {
		t.Fatal(err)
	}
	healthy := askFinding("healthy-1", "api")
	healthy.ResourceUID = "uid-api"
	healthy.Status = "Healthy"
	healthy.OwnerChain = []string{"Pod/api-new", "ReplicaSet/api-rs", "Deployment/api"}
	responder := &askTestIncidentResponder{}
	var stdout, stderr bytes.Buffer
	code := runAskSessionWithFindings(context.Background(), &stdout, &stderr, options{output: "text", askSession: session.ID}, cluster, store, "Is it healthy now?", []analyzer.Finding{healthy}, nil, responder, nil)
	if code != 0 || responder.calls != 0 || !strings.Contains(stdout.String(), "report the workload as healthy") {
		t.Fatalf("healthy witness should close the incident after owner validation: code=%d calls=%d stdout=%q stderr=%q", code, responder.calls, stdout.String(), stderr.String())
	}
}

func TestAskConversationTextKeepsEvidenceCompactUnlessProofRequested(t *testing.T) {
	now := time.Now().UTC()
	refs := make([]asksession.EvidenceRef, 12)
	for i := range refs {
		refs[i] = asksession.EvidenceRef{ID: fmt.Sprintf("ev-%02d", i+1), Resource: "pod/api", Signal: "Signal", At: now}
	}
	response := ai.IncidentTurnResponse{
		Answer: "The API is restarting based on the latest scan.",
		Observed: []ai.IncidentClaim{
			{Text: "The exit code remains nonzero.", EvidenceIDs: []string{"ev-01"}},
			{Text: "The pod restarted several times.", EvidenceIDs: []string{"ev-02"}},
			{Text: "The image pull succeeded.", EvidenceIDs: []string{"ev-03"}},
			{Text: "The process started before exiting.", EvidenceIDs: []string{"ev-04"}},
			{Text: "The current node has capacity.", EvidenceIDs: []string{"ev-05"}},
		},
		LikelyCause: ai.IncidentClaim{Text: "The process exits after startup.", EvidenceIDs: []string{"ev-12"}},
		Unknowns:    []string{"The intended startup behavior is not known.", "The source change is not known.", "The restart duration is not known.", "The full application health is not known.", "The deployment history is not known."},
	}
	session := asksession.Session{ID: "a1b2c3d4e5f6"}
	target := ai.IntentCandidate{Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", Status: "CrashLoopBackOff"}
	var compact bytes.Buffer
	if code := writeAskConversation(&compact, options{output: "text"}, "why", session, target, response, refs, true); code != 0 {
		t.Fatalf("compact text returned %d", code)
	}
	if got := strings.Count(compact.String(), "Evidence ev-"); got != 5 || !strings.Contains(compact.String(), "7 additional evidence references") || !strings.Contains(compact.String(), "2 additional observations") || !strings.Contains(compact.String(), "2 additional unknowns") || !strings.Contains(compact.String(), "--proof") || !strings.Contains(compact.String(), "Evidence ev-12:") {
		t.Fatalf("default output should compact evidence, observations, and unknowns: rows=%d output=%q", got, compact.String())
	}
	var proof bytes.Buffer
	if code := writeAskConversation(&proof, options{output: "text", proof: true}, "why", session, target, response, refs, true); code != 0 {
		t.Fatalf("proof text returned %d", code)
	}
	if got := strings.Count(proof.String(), "Evidence ev-"); got != len(refs) || !strings.Contains(proof.String(), "The deployment history is not known") {
		t.Fatalf("--proof should show all evidence and response details: rows=%d output=%q", got, proof.String())
	}
	var structured bytes.Buffer
	if code := writeAskConversation(&structured, options{output: "json"}, "why", session, target, response, refs, true); code != 0 {
		t.Fatalf("structured output returned %d", code)
	}
	var report askReport
	if err := json.Unmarshal(structured.Bytes(), &report); err != nil || len(report.EvidenceRefs) != len(refs) || len(report.Observed) != len(response.Observed) || len(report.Unknowns) != len(response.Unknowns) {
		t.Fatalf("structured output was truncated: report=%#v err=%v", report, err)
	}
}
