package shadow

import (
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

func assessFindingEvidenceModule(finding analyzer.Finding, evidence *FailureEvidence, attempt Attempt) EvidenceModuleResult {
	status := finding.Status
	for _, item := range finding.Evidence {
		status += " " + item.Label + " " + item.Value
	}
	return assessEvidenceModule(status, evidence, attempt)
}

func assessEvidenceModule(findingStatus string, evidence *FailureEvidence, attempt Attempt) EvidenceModuleResult {
	status := strings.ToLower(strings.TrimSpace(findingStatus))
	switch {
	case strings.Contains(status, "oomkilled"):
		return assessOOMModule(evidence, attempt)
	case strings.Contains(status, "createcontainerconfigerror") || strings.Contains(status, "configerror"):
		return assessConfigModule(evidence, attempt)
	case strings.Contains(status, "probefailure") || (strings.Contains(status, "probe") && strings.Contains(status, "fail")):
		return assessProbeModule(evidence, attempt)
	case strings.Contains(status, "imagepullbackoff") || strings.Contains(status, "errimagepull"):
		return assessImageModule(evidence, attempt)
	default:
		return EvidenceModuleResult{Status: EvidenceModuleUnsupported, Summary: "no MVP-specific evidence module is available for this failure type"}
	}
}

func applyEvidenceModuleGate(attempt *Attempt, module EvidenceModuleResult) {
	if attempt == nil {
		return
	}
	switch module.Status {
	case EvidenceModulePassed:
		return
	case EvidenceModuleFailed:
		if !attempt.Ready {
			return
		}
		attempt.Ready = false
		attempt.Inconclusive = false
		attempt.Message = "failure-specific evidence failed: " + module.Summary
	case EvidenceModuleInconclusive:
		attempt.Ready = false
		attempt.Inconclusive = true
		attempt.Message = "failure-specific evidence is not sufficient: " + module.Summary
	case EvidenceModuleUnsupported:
		if !attempt.Ready {
			return
		}
		attempt.Ready = false
		attempt.Inconclusive = true
		attempt.Message = "failure-specific evidence is not sufficient: " + module.Summary
	default:
		attempt.Ready = false
		attempt.Inconclusive = true
		attempt.Message = "failure-specific evidence has an unknown status; verification is inconclusive"
	}
}

func assessOOMModule(evidence *FailureEvidence, attempt Attempt) EvidenceModuleResult {
	if evidence == nil || evidence.MemoryAssessment == nil {
		return EvidenceModuleResult{Module: "oom-memory", Status: EvidenceModuleInconclusive, Summary: "Metrics API memory evidence is unavailable"}
	}
	switch evidence.MemoryAssessment.Status {
	case "passed":
		if attempt.Ready {
			return EvidenceModuleResult{Module: "oom-memory", Status: EvidenceModulePassed, Summary: evidence.MemoryAssessment.Summary}
		}
		return EvidenceModuleResult{Module: "oom-memory", Status: failedOrInconclusive(attempt), Summary: "memory evidence passed, but the candidate did not complete the stability check"}
	case "failed":
		return EvidenceModuleResult{Module: "oom-memory", Status: EvidenceModuleFailed, Summary: evidence.MemoryAssessment.Summary}
	default:
		return EvidenceModuleResult{Module: "oom-memory", Status: EvidenceModuleInconclusive, Summary: evidence.MemoryAssessment.Summary}
	}
}

func assessConfigModule(evidence *FailureEvidence, attempt Attempt) EvidenceModuleResult {
	if evidence == nil || len(evidence.ConfigMaps) == 0 {
		return EvidenceModuleResult{Module: "config-reference", Status: EvidenceModuleInconclusive, Summary: "no supported ConfigMap key reference was collected"}
	}
	for _, ref := range evidence.ConfigMaps {
		if ref.Optional {
			continue
		}
		switch {
		case ref.SourceStatus == "unknown" || ref.SandboxStatus == "unknown":
			return EvidenceModuleResult{Module: "config-reference", Status: EvidenceModuleInconclusive, Summary: "a required ConfigMap reference could not be inspected"}
		case ref.SourceStatus != "key-present" || ref.SandboxStatus == "key-missing" || ref.SandboxStatus == "unresolved" || ref.SandboxStatus == "mismatch":
			return EvidenceModuleResult{Module: "config-reference", Status: EvidenceModuleFailed, Summary: "a required ConfigMap key is unavailable in the candidate environment"}
		case ref.SandboxStatus != "key-present":
			return EvidenceModuleResult{Module: "config-reference", Status: EvidenceModuleInconclusive, Summary: "candidate ConfigMap key presence was not established"}
		}
	}
	if !attempt.Ready {
		return EvidenceModuleResult{Module: "config-reference", Status: failedOrInconclusive(attempt), Summary: "ConfigMap keys are present, but the candidate did not complete the stability check"}
	}
	return EvidenceModuleResult{Module: "config-reference", Status: EvidenceModulePassed, Summary: "required ConfigMap keys are present in the candidate namespace"}
}

func assessProbeModule(evidence *FailureEvidence, attempt Attempt) EvidenceModuleResult {
	if evidence == nil || len(evidence.Probes) == 0 {
		return EvidenceModuleResult{Module: "probe", Status: EvidenceModuleInconclusive, Summary: "candidate probe configuration was not observed"}
	}
	if attempt.Inconclusive {
		return EvidenceModuleResult{Module: "probe", Status: EvidenceModuleInconclusive, Summary: "probe readiness could not be observed for the complete stability window"}
	}
	if !attempt.Ready || attempt.Restarts > 0 {
		return EvidenceModuleResult{Module: "probe", Status: EvidenceModuleFailed, Summary: "candidate did not remain Ready with zero restarts during the stability window"}
	}
	return EvidenceModuleResult{Module: "probe", Status: EvidenceModulePassed, Summary: "candidate probe remained Ready with zero restarts during the stability window"}
}

func assessImageModule(evidence *FailureEvidence, attempt Attempt) EvidenceModuleResult {
	if evidence == nil || len(evidence.Containers) == 0 {
		return EvidenceModuleResult{Module: "image-pull", Status: EvidenceModuleInconclusive, Summary: "candidate image runtime identity was not observed"}
	}
	matched := false
	for _, container := range evidence.Containers {
		if container.RequestedImage == "" && container.RuntimeImage == "" {
			continue
		}
		if container.RequestedImage == "" || container.RuntimeImage == "" || container.ImageID == "" {
			return EvidenceModuleResult{Module: "image-pull", Status: EvidenceModuleInconclusive, Summary: "candidate image request and runtime identity could not both be confirmed"}
		}
		if container.RequestedImage != container.RuntimeImage {
			return EvidenceModuleResult{Module: "image-pull", Status: EvidenceModuleFailed, Summary: "candidate runtime image does not match the requested image"}
		}
		matched = true
	}
	if !matched {
		return EvidenceModuleResult{Module: "image-pull", Status: EvidenceModuleInconclusive, Summary: "candidate image runtime identity was not observed"}
	}
	if !attempt.Ready {
		return EvidenceModuleResult{Module: "image-pull", Status: failedOrInconclusive(attempt), Summary: "image identity matched, but the candidate did not complete the stability check"}
	}
	return EvidenceModuleResult{Module: "image-pull", Status: EvidenceModulePassed, Summary: "candidate is Ready on the requested runtime image"}
}

func failedOrInconclusive(attempt Attempt) EvidenceModuleStatus {
	if attempt.Inconclusive {
		return EvidenceModuleInconclusive
	}
	return EvidenceModuleFailed
}
