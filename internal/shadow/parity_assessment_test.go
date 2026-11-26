package shadow

import (
	"strings"
	"testing"
)

func TestOperationalParityDeductsUnavailableReferencesAndTraffic(t *testing.T) {
	evidence := &FailureEvidence{ConfigMaps: []ConfigMapReferenceEvidence{
		{Name: "app-config", Key: "DB_URL", SourceStatus: "key-present", SandboxStatus: "unresolved"},
		{Name: "app-config", Key: "DB_URL", SourceStatus: "key-present", SandboxStatus: "unresolved"},
		{Name: "optional-config", Key: "MODE", SourceStatus: "key-missing", SandboxStatus: "unresolved", Optional: true},
	}}
	got := assessOperationalParity(evidence, EvidenceModuleResult{Module: "probe", Status: EvidenceModulePassed}, false, false)
	if got.Score != 75 {
		t.Fatalf("expected one required reference deduction and no-traffic deduction (75), got %#v", got)
	}
	if len(got.Deductions) != 2 || got.Deductions[0].Category != "no-production-traffic" || got.Deductions[1].Category != "unavailable-config" || got.Deductions[1].Points != 15 {
		t.Fatalf("deductions should be stable and deduplicated: %#v", got.Deductions)
	}
}

func TestOperationalParityDeductsDistinctOmittedSecretReferencesWithoutValues(t *testing.T) {
	evidence := &FailureEvidence{Secrets: []SecretReferenceEvidence{
		{Name: "app-credentials", Key: "password", Status: "omitted-by-isolation"},
		{Name: "app-credentials", Key: "password", Status: "omitted-by-isolation"},
		{Name: "optional-token", Key: "token", Status: "omitted-by-isolation", Optional: true},
	}}
	got := assessOperationalParity(evidence, EvidenceModuleResult{Status: EvidenceModulePassed}, false, true)
	if got.Score != 85 || len(got.Deductions) != 1 || got.Deductions[0].Category != "unavailable-secret" {
		t.Fatalf("expected one distinct required secret deduction, got %#v", got)
	}
	if strings.Contains(got.Deductions[0].Summary, "password") || strings.Contains(got.Deductions[0].Summary, "credentials") {
		t.Fatalf("parity summary must not disclose secret reference details: %#v", got.Deductions[0])
	}
}

func TestOperationalParityReportsDependencyAndMissingModuleDeductions(t *testing.T) {
	got := assessOperationalParity(&FailureEvidence{}, EvidenceModuleResult{Status: EvidenceModuleUnsupported}, true, false)
	if got.Score != 65 {
		t.Fatalf("expected dependency, missing module, and no-traffic deductions (65), got %#v", got)
	}
	if len(got.Deductions) != 3 {
		t.Fatalf("expected three documented deductions, got %#v", got.Deductions)
	}
}

func TestOperationalParityDeductsIncompleteModuleProof(t *testing.T) {
	got := assessOperationalParity(&FailureEvidence{}, EvidenceModuleResult{Module: "oom-memory", Status: EvidenceModuleInconclusive}, false, true)
	if got.Score != 90 || len(got.Deductions) != 1 || got.Deductions[0].Category != "missing-evidence-module" {
		t.Fatalf("incomplete module proof should lower operational parity by 10: %#v", got)
	}
}

func TestOperationalParityKeepsUnknownsAsLimitationsAndFloorsAtZero(t *testing.T) {
	unknown := &FailureEvidence{ConfigMaps: []ConfigMapReferenceEvidence{{Name: "unknown", Key: "key", SourceStatus: "unknown", SandboxStatus: "unknown"}}}
	got := assessOperationalParity(unknown, EvidenceModuleResult{Module: "probe", Status: EvidenceModulePassed}, false, false)
	if got.Score != 90 || len(got.Deductions) != 1 || len(got.Limitations) != 2 {
		t.Fatalf("unknown reference should be a limitation, only traffic deducts: %#v", got)
	}
	got = assessOperationalParity(&FailureEvidence{ConfigMaps: []ConfigMapReferenceEvidence{{Name: "one", Key: "a", SourceStatus: "not-found"}, {Name: "two", Key: "b", SourceStatus: "not-found"}, {Name: "three", Key: "c", SourceStatus: "key-missing"}, {Name: "four", Key: "d", SourceStatus: "key-missing"}, {Name: "five", Key: "e", SourceStatus: "key-missing"}, {Name: "six", Key: "f", SourceStatus: "key-missing"}}}, EvidenceModuleResult{Status: EvidenceModuleUnsupported}, true, false)
	if got.Score != 0 {
		t.Fatalf("operational parity must floor at zero, got %#v", got)
	}
}

func TestOperationalParityDoesNotDeductWhenRequiredDependencyWasExercised(t *testing.T) {
	got := assessOperationalParity(&FailureEvidence{}, EvidenceModuleResult{Module: "config-reference", Status: EvidenceModulePassed}, false, true)
	if got.Score != 100 || len(got.Deductions) != 0 {
		t.Fatalf("all observed parity dimensions pass, got %#v", got)
	}
}
