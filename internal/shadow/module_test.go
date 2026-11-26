package shadow

import "testing"

func TestOOMModuleRequiresPassingMetricsAssessment(t *testing.T) {
	t.Run("pass", func(t *testing.T) {
		got := assessEvidenceModule("OOMKilled", &FailureEvidence{MemoryAssessment: &MemoryAssessment{Status: "passed", Summary: "peak under limit"}}, Attempt{Ready: true})
		if got.Module != "oom-memory" || got.Status != EvidenceModulePassed {
			t.Fatalf("expected OOM module pass, got %#v", got)
		}
	})
	t.Run("missing metrics is inconclusive", func(t *testing.T) {
		got := assessEvidenceModule("OOMKilled", &FailureEvidence{}, Attempt{Ready: true})
		if got.Status != EvidenceModuleInconclusive {
			t.Fatalf("missing memory measurements must be inconclusive, got %#v", got)
		}
	})
	t.Run("unsafe curve fails", func(t *testing.T) {
		got := assessEvidenceModule("OOMKilled", &FailureEvidence{MemoryAssessment: &MemoryAssessment{Status: "failed", Summary: "memory trend is unsafe"}}, Attempt{Ready: true})
		if got.Status != EvidenceModuleFailed {
			t.Fatalf("unsafe memory evidence must fail, got %#v", got)
		}
	})
}

func TestConfigModuleRequiresValueFreeResolvedKeys(t *testing.T) {
	passing := &FailureEvidence{ConfigMaps: []ConfigMapReferenceEvidence{{Name: "app-config", Key: "DB_URL", SourceStatus: "key-present", SandboxStatus: "key-present"}}}
	if got := assessEvidenceModule("CreateContainerConfigError", passing, Attempt{Ready: true}); got.Status != EvidenceModulePassed {
		t.Fatalf("resolved reference plus stable candidate should pass, got %#v", got)
	}
	missing := &FailureEvidence{ConfigMaps: []ConfigMapReferenceEvidence{{Name: "app-config", Key: "DB_URL", SourceStatus: "key-present", SandboxStatus: "key-missing"}}}
	if got := assessEvidenceModule("CreateContainerConfigError", missing, Attempt{Ready: false}); got.Status != EvidenceModuleFailed {
		t.Fatalf("missing candidate key should fail, got %#v", got)
	}
	unknown := &FailureEvidence{ConfigMaps: []ConfigMapReferenceEvidence{{Name: "app-config", Key: "DB_URL", SourceStatus: "unknown", SandboxStatus: "unknown"}}}
	if got := assessEvidenceModule("CreateContainerConfigError", unknown, Attempt{Ready: true}); got.Status != EvidenceModuleInconclusive {
		t.Fatalf("unreadable reference must be inconclusive, got %#v", got)
	}
	if got := assessEvidenceModule("CreateContainerConfigError", &FailureEvidence{}, Attempt{Ready: true}); got.Status != EvidenceModuleInconclusive {
		t.Fatalf("missing reference inventory must not imply success, got %#v", got)
	}
}

func TestProbeModuleRequiresConfiguredProbeAndStableCandidate(t *testing.T) {
	evidence := &FailureEvidence{Probes: []ProbeEvidence{{Container: "api", Type: "readiness", Protocol: "tcp", Port: "8080"}}}
	if got := assessEvidenceModule("ProbeFailure", evidence, Attempt{Ready: true, Restarts: 0}); got.Status != EvidenceModulePassed {
		t.Fatalf("stable candidate with a configured probe should pass, got %#v", got)
	}
	if got := assessEvidenceModule("ProbeFailure", evidence, Attempt{Ready: false}); got.Status != EvidenceModuleFailed {
		t.Fatalf("candidate that did not remain ready should fail, got %#v", got)
	}
	if got := assessEvidenceModule("ProbeFailure", &FailureEvidence{}, Attempt{Ready: true}); got.Status != EvidenceModuleInconclusive {
		t.Fatalf("candidate without probe evidence must be inconclusive, got %#v", got)
	}
}

func TestImageModuleRequiresRequestedRuntimeAndImageIdentityMatch(t *testing.T) {
	passing := &FailureEvidence{Containers: []ContainerEvidence{{Name: "api", RequestedImage: "registry.test/api:v2", RuntimeImage: "registry.test/api:v2", ImageID: "containerd://sha256:abc"}}}
	if got := assessEvidenceModule("ImagePullBackOff", passing, Attempt{Ready: true}); got.Status != EvidenceModulePassed {
		t.Fatalf("matching runtime image evidence should pass, got %#v", got)
	}
	mismatch := &FailureEvidence{Containers: []ContainerEvidence{{Name: "api", RequestedImage: "registry.test/api:v2", RuntimeImage: "registry.test/api:v1", ImageID: "containerd://sha256:abc"}}}
	if got := assessEvidenceModule("ImagePullBackOff", mismatch, Attempt{Ready: true}); got.Status != EvidenceModuleFailed {
		t.Fatalf("runtime image mismatch should fail, got %#v", got)
	}
	unknown := &FailureEvidence{Containers: []ContainerEvidence{{Name: "api", RequestedImage: "registry.test/api:v2", RuntimeImage: "registry.test/api:v2"}}}
	if got := assessEvidenceModule("ImagePullBackOff", unknown, Attempt{Ready: true}); got.Status != EvidenceModuleInconclusive {
		t.Fatalf("missing image ID must be inconclusive, got %#v", got)
	}
}

func TestUnsupportedFailureModuleCannotBeVerified(t *testing.T) {
	got := assessEvidenceModule("CrashLoopBackOff", &FailureEvidence{}, Attempt{Ready: true})
	if got.Status != EvidenceModuleUnsupported || got.Module != "" {
		t.Fatalf("failure without an MVP-specific module must be explicitly unsupported, got %#v", got)
	}
}

func TestEvidenceModuleGatePreventsUnsupportedVerifiedVerdict(t *testing.T) {
	attempt := Attempt{Ready: true}
	applyEvidenceModuleGate(&attempt, EvidenceModuleResult{Status: EvidenceModuleUnsupported, Summary: "no module"})
	if attempt.Ready || !attempt.Inconclusive || statusForAttempt(attempt) != VerificationInconclusive {
		t.Fatalf("unsupported module must withhold verified status as inconclusive, got %#v", attempt)
	}
}

func TestEvidenceModuleGatePreservesFailureAndPassingStatuses(t *testing.T) {
	failed := Attempt{Ready: true}
	applyEvidenceModuleGate(&failed, EvidenceModuleResult{Status: EvidenceModuleFailed, Summary: "unsafe"})
	if failed.Ready || failed.Inconclusive || statusForAttempt(failed) != VerificationFailed {
		t.Fatalf("failed module must produce failed verdict, got %#v", failed)
	}
	passed := Attempt{Ready: true}
	applyEvidenceModuleGate(&passed, EvidenceModuleResult{Status: EvidenceModulePassed})
	if !passed.Ready || statusForAttempt(passed) != VerificationVerified {
		t.Fatalf("passed module must preserve verified stability result, got %#v", passed)
	}
}

func TestEvidenceModuleGateStopsRetryWhenProofIsInconclusiveOnFailedCandidate(t *testing.T) {
	attempt := Attempt{Ready: false, Inconclusive: false, Message: "image pull failed"}
	applyEvidenceModuleGate(&attempt, EvidenceModuleResult{Module: "image-pull", Status: EvidenceModuleInconclusive, Summary: "runtime image identity was not observed"})
	if attempt.Ready || !attempt.Inconclusive || attempt.Message != "failure-specific evidence is not sufficient: runtime image identity was not observed" {
		t.Fatalf("inconclusive module evidence must stop retries regardless of candidate phase: %#v", attempt)
	}
}

func TestEvidenceModuleSummaryDoesNotIncludeConfigValues(t *testing.T) {
	evidence := &FailureEvidence{ConfigMaps: []ConfigMapReferenceEvidence{{Name: "app-config", Key: "DB_URL", SourceStatus: "key-present", SandboxStatus: "key-present"}}}
	got := assessEvidenceModule("CreateContainerConfigError", evidence, Attempt{Ready: true})
	if got.Status != EvidenceModulePassed {
		t.Fatalf("expected config evidence pass, got %#v", got)
	}
	if got.Summary == "" || got.Summary == "postgres://password@db" {
		t.Fatalf("module summary must be a safe status summary, got %q", got.Summary)
	}
}
