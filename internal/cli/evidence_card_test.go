package cli

import (
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/repo"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

func TestEvidenceCardSummarizesVerifiedResultWithoutRawEvidence(t *testing.T) {
	ready := int64(8)
	finding := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "payments-api", Namespace: "prod", Status: "OOMKilled"}
	plan := fix.Plan{Strategy: "resources"}
	result := shadow.Result{
		Status: shadow.VerificationVerified, Verified: true, Parity: 91,
		Attempts: []shadow.Attempt{{Number: 1, Ready: true, Evidence: &shadow.FailureEvidence{
			TimeToReadySeconds: &ready,
			MemoryAssessment:   &shadow.MemoryAssessment{PeakBytes: 749 * 1024 * 1024, LimitBytes: 1024 * 1024 * 1024, HeadroomPercent: 26},
			ProbeFailureEvents: []string{"password=hunter2"},
		}}},
		FailureSummary: "password=hunter2", Warnings: []string{"token=private"},
	}
	got := evidenceCard(finding, plan, result, true)
	for _, want := range []string{"Target: Deployment/payments-api (prod)", "Finding: OOMKilled", "Verdict: VERIFIED", "Strategy: resources", "Attempts: 1", "Ready: 8s", "749Mi peak", "1Gi limit", "26% headroom", "Parity: 91%"} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence card missing %q:\n%s", want, got)
		}
	}
	for _, secret := range []string{"hunter2", "private", "password=", "token="} {
		if strings.Contains(got, secret) {
			t.Errorf("evidence card leaked raw evidence %q:\n%s", secret, got)
		}
	}
}

func TestEvidenceCardDistinguishesUnverifiedOutcomesAndOmitsUnavailableMeasurements(t *testing.T) {
	baseFinding := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "test", Status: "CrashLoopBackOff"}
	for _, tc := range []struct {
		status shadow.VerificationStatus
		want   string
	}{
		{shadow.VerificationFailed, "Verdict: FAILED"},
		{shadow.VerificationInconclusive, "Verdict: INCONCLUSIVE"},
		{shadow.VerificationBlocked, "Verdict: BLOCKED"},
		{"", "Verdict: NOT RUN"},
	} {
		got := evidenceCard(baseFinding, fix.Plan{Strategy: "collect-evidence"}, shadow.Result{Status: tc.status}, false)
		if !strings.Contains(got, tc.want) || strings.Contains(got, "Parity:") || strings.Contains(got, "Ready:") || strings.Contains(got, "Memory:") {
			t.Errorf("status %q card should contain %q and omit unsupported measurements:\n%s", tc.status, tc.want, got)
		}
	}
}

func TestVerifiedEvidenceCardIsSharedWithPullRequestBody(t *testing.T) {
	ready := int64(9)
	finding := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "prod", Status: "ProbeFailure"}
	result := shadow.Result{Status: shadow.VerificationVerified, Verified: true, Resource: "Deployment/api", Namespace: "prod", Parity: 87, Attempts: []shadow.Attempt{{Evidence: &shadow.FailureEvidence{TimeToReadySeconds: &ready}}}}
	body := prBody(result, repo.SourcePatch{Path: "deploy/api.yaml"}, finding, fix.Plan{Strategy: "probe"}, true)
	for _, want := range []string{"Verdict: VERIFIED", "Finding: ProbeFailure", "Strategy: probe", "Parity: 87%", "Ready: 9s", "Source patch: `deploy/api.yaml`"} {
		if !strings.Contains(body, want) {
			t.Errorf("verified PR evidence missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Shadow verification was not run") {
		t.Fatalf("verified PR incorrectly claims no verification:\n%s", body)
	}
}

func TestEvidenceCardReadsLatestAttemptEvidence(t *testing.T) {
	ready := int64(17)
	result := shadow.Result{Status: shadow.VerificationFailed, Attempts: []shadow.Attempt{
		{Number: 1, Evidence: &shadow.FailureEvidence{TimeToReadySeconds: int64Ptr(2)}},
		{Number: 2, Evidence: &shadow.FailureEvidence{TimeToReadySeconds: &ready}},
	}}
	got := evidenceCard(analyzer.Finding{ResourceKind: "Pod", ResourceName: "api", Namespace: "test"}, fix.Plan{}, result, false)
	if !strings.Contains(got, "Ready: 17s") || strings.Contains(got, "Ready: 2s") {
		t.Fatalf("card did not use the latest attempt evidence:\n%s", got)
	}
}
