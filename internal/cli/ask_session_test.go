package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/asksession"
)

func TestAskSessionEndToEnd(t *testing.T) {
	store := asksession.Store{Path: t.TempDir() + "/sessions.json"}
	cluster := strings.Repeat("c", 64)
	finding := askFinding("incident-1", "api")
	finding.ResourceUID = "uid-api"
	finding.OwnerChain = []string{"ReplicaSet/api-1", "Deployment/api"}
	finding.Evidence = []analyzer.Evidence{{Label: "Exit code", Value: "1"}}
	resolver := &askTestResolver{intent: ai.TargetIntent{CandidateIDs: []string{finding.ID}, Interpretation: "The API container is restarting."}}
	responder := &askTestIncidentResponder{responses: []ai.IncidentTurnResponse{
		{Answer: "The process exits after startup.", Observed: []ai.IncidentClaim{{Text: "Exit code is 1.", EvidenceIDs: []string{"ev-01"}}}, LikelyCause: ai.IncidentClaim{Text: "The configured process exits unsuccessfully.", EvidenceIDs: []string{"ev-01"}}, Unknowns: []string{"The intended command is unknown."}, FollowUp: "Did its command recently change?"},
		{Answer: "The fresh evidence still shows an exit code of 1. I can prepare a guarded preview.", Observed: []ai.IncidentClaim{{Text: "The current exit code is 1.", EvidenceIDs: []string{"ev-01"}}}, LikelyCause: ai.IncidentClaim{Text: "The configured process still exits unsuccessfully.", EvidenceIDs: []string{"ev-01"}}, Unknowns: []string{"The intended command remains unknown."}, PreviewRequested: true},
	}}
	var stdout, stderr bytes.Buffer
	previewCalls := 0
	preview := func(_ context.Context, opts options, got analyzer.Finding, ref string) int {
		previewCalls++
		if !opts.preview || opts.apply || opts.yes || opts.autoFix || opts.commit || opts.sourcePatch || opts.editPatch || got.ID != finding.ID || ref != "Deployment/api" {
			t.Errorf("unsafe or mismatched preview args: opts=%#v finding=%#v ref=%q", opts, got, ref)
		}
		return 0
	}

	code := runAskSessionWithFindings(context.Background(), &stdout, &stderr, options{output: "text"}, cluster, store, "Why is api restarting?", []analyzer.Finding{finding}, resolver, responder, preview)
	if code != 0 || responder.calls != 1 || previewCalls != 0 {
		t.Fatalf("start: code=%d responses=%d previews=%d stdout=%q stderr=%q", code, responder.calls, previewCalls, stdout.String(), stderr.String())
	}
	sessions, err := store.List(cluster)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("session list after start: %#v, %v", sessions, err)
	}
	id := sessions[0].ID
	started, err := store.Load(id, cluster)
	if err != nil || len(started.Turns) != 1 {
		t.Fatalf("session after start: %#v, %v", started, err)
	}

	fresh := finding
	fresh.Evidence = []analyzer.Evidence{{Label: "Exit code", Value: "1"}, {Label: "Restart count", Value: "7"}}
	stdout.Reset()
	stderr.Reset()
	code = runAskSessionWithFindings(context.Background(), &stdout, &stderr, options{output: "text", askSession: id}, cluster, store, "Preview a fix for this issue", []analyzer.Finding{fresh}, nil, responder, preview)
	if code != 0 || responder.calls != 2 || previewCalls != 1 {
		t.Fatalf("resume: code=%d responses=%d previews=%d stdout=%q stderr=%q", code, responder.calls, previewCalls, stdout.String(), stderr.String())
	}
	if len(responder.inputs[1].PriorTurns) != 1 || len(responder.inputs[1].Evidence) != 2 || responder.inputs[1].Evidence[1].Signal != "Restart count" {
		t.Fatalf("follow-up did not receive saved context and fresh evidence: %#v", responder.inputs[1])
	}
	saved, err := store.Load(id, cluster)
	if err != nil || len(saved.Turns) != 2 || len(saved.EvidenceRefs) != 2 {
		t.Fatalf("saved resumed session: %#v, %v", saved, err)
	}
}
