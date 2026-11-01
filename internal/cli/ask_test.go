package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
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
