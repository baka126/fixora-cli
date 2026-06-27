package shadow

import (
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

func TestDiagnoseFailureDetectsStressOOMAfterArchitectureFix(t *testing.T) {
	result := Result{Attempts: []Attempt{{
		Number:     1,
		ExitReason: "OOMKilled",
		Logs:       []string{"stress-ng: info: dispatching hogs: 1 vm"},
	}}}
	finding := analyzer.Finding{
		Status: "ExecFormatError",
		Evidence: []analyzer.Evidence{
			{Label: "Container image stress", Value: "polinux/stress-ng"},
		},
	}
	plan := fix.Plan{Strategy: "fix-architecture", PatchTemplate: "image: alexeiled/stress-ng@sha256:abc"}

	diagnosis := DiagnoseFailure(result, finding, plan)
	if diagnosis.Class != FailureClassExpectedWorkload {
		t.Fatalf("expected stress workload class, got %#v", diagnosis)
	}
	if !diagnosis.OriginalSymptomResolved {
		t.Fatalf("expected original architecture symptom to be marked resolved: %#v", diagnosis)
	}
	if !diagnosis.DeliveryBlocked {
		t.Fatalf("expected delivery to remain blocked: %#v", diagnosis)
	}
}

func TestDiagnoseFailureDetectsSecondaryOOMAfterArchitectureFix(t *testing.T) {
	result := Result{Attempts: []Attempt{{Number: 1, ExitReason: "OOMKilled", Logs: []string{"app allocated memory"}}}}
	finding := analyzer.Finding{Status: "ExecFormatError"}
	plan := fix.Plan{Strategy: "fix-architecture", PatchTemplate: "image: repo/api@sha256:abc"}

	diagnosis := DiagnoseFailure(result, finding, plan)
	if diagnosis.Class != FailureClassSecondaryFailure {
		t.Fatalf("expected secondary failure class, got %#v", diagnosis)
	}
	if !diagnosis.OriginalSymptomResolved {
		t.Fatalf("expected original architecture symptom to be marked resolved: %#v", diagnosis)
	}
}

func TestDiagnoseFailureDetectsOriginalArchitectureStillPresent(t *testing.T) {
	result := Result{Attempts: []Attempt{{Number: 1, ExitReason: "Error", Logs: []string{"exec /bin/app: exec format error"}}}}
	finding := analyzer.Finding{Status: "ExecFormatError"}
	plan := fix.Plan{Strategy: "fix-architecture"}

	diagnosis := DiagnoseFailure(result, finding, plan)
	if diagnosis.Class != FailureClassOriginalStillPresent {
		t.Fatalf("expected original issue class, got %#v", diagnosis)
	}
	if diagnosis.OriginalSymptomResolved {
		t.Fatalf("did not expect original symptom to be resolved: %#v", diagnosis)
	}
}

func TestDiagnoseFailureDetectsCandidateRegression(t *testing.T) {
	result := Result{Attempts: []Attempt{{Number: 1, ExitReason: "ImagePullBackOff", Events: []string{"failed to pull image"}}}}
	finding := analyzer.Finding{Status: "ExecFormatError"}
	plan := fix.Plan{Strategy: "fix-architecture"}

	diagnosis := DiagnoseFailure(result, finding, plan)
	if diagnosis.Class != FailureClassCandidateRegression {
		t.Fatalf("expected candidate regression class, got %#v", diagnosis)
	}
	if !diagnosis.OriginalSymptomResolved {
		t.Fatalf("expected original symptom to be marked resolved: %#v", diagnosis)
	}
}
