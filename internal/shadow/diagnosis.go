package shadow

import (
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

const (
	FailureClassOriginalStillPresent = "original-issue-still-present"
	FailureClassSecondaryFailure     = "secondary-failure-after-original-fix"
	FailureClassExpectedWorkload     = "expected-workload-failure"
	FailureClassCandidateRegression  = "candidate-regression"
	FailureClassProbeMisconfig       = "probe-misconfiguration"
	FailureClassTimeout              = "verification-timeout"
	FailureClassUnknown              = "unknown"
)

type FailureDiagnosis struct {
	Class                   string
	Summary                 string
	OriginalSymptomResolved bool
	DeliveryBlocked         bool
	Details                 []string
}

func DiagnoseFailure(result Result, finding analyzer.Finding, plan fix.Plan) FailureDiagnosis {
	return DiagnoseFailureForPatch(result, finding, plan, plan.PatchYAML())
}

func DiagnoseFailureForPatch(result Result, finding analyzer.Finding, plan fix.Plan, patch string) FailureDiagnosis {
	diagnosis := FailureDiagnosis{
		Class:           FailureClassUnknown,
		Summary:         "Shadow verification failed before the candidate became ready.",
		DeliveryBlocked: true,
	}
	if result.Verified {
		diagnosis.Summary = "Shadow verification passed."
		diagnosis.DeliveryBlocked = false
		return diagnosis
	}

	original := canonicalReason(finding.Status)
	terminal, hasTerminal := terminalAttempt(result)
	shadowReason := ""
	shadowText := ""
	if hasTerminal {
		shadowReason = canonicalReason(attemptFailureReason(terminal))
		shadowText = strings.ToLower(joinAttemptEvidence(terminal))
	}
	sourceText := strings.ToLower(joinFindingEvidence(finding) + "\n" + patch)

	if shadowReason == "timeout" {
		diagnosis.Class = FailureClassTimeout
		diagnosis.Summary = "Shadow verification timed out before readiness could be proven."
		diagnosis.Details = append(diagnosis.Details, "Increase --shadow-timeout only after checking pod events, image pulls, readiness probes, and dependency startup time.")
		return diagnosis
	}

	if original != "" && shadowReason == original {
		diagnosis.Class = FailureClassOriginalStillPresent
		diagnosis.Summary = "The shadow clone still shows the original failure."
		diagnosis.Details = append(diagnosis.Details, "Treat the patch as ineffective for the reported root cause.")
		return diagnosis
	}

	if original == "execformaterror" && strings.Contains(shadowText, "exec format error") {
		diagnosis.Class = FailureClassOriginalStillPresent
		diagnosis.Summary = "The shadow clone still shows the original architecture failure."
		diagnosis.Details = append(diagnosis.Details, "The replacement image or scheduling constraint did not actually match the node platform.")
		return diagnosis
	}

	if original == "execformaterror" && shadowReason != "" && shadowReason != original {
		diagnosis.OriginalSymptomResolved = !strings.Contains(shadowText, "exec format error")
		if shadowReason == "oomkilled" {
			if looksLikeStressWorkload(sourceText + "\n" + shadowText) {
				diagnosis.Class = FailureClassExpectedWorkload
				diagnosis.Summary = "The architecture symptom appears resolved, but the shadow workload OOMKilled under stress-like behavior."
				diagnosis.Details = append(diagnosis.Details,
					"This may be expected for stress/load-test images or arguments, so shadow cannot prove a production-safe fix from image replacement alone.",
					"Keep delivery blocked until the source fix either rebuilds the original image for the node architecture or includes a reviewed memory/resource policy that also passes shadow.",
				)
				return diagnosis
			}
			diagnosis.Class = FailureClassSecondaryFailure
			diagnosis.Summary = "The architecture symptom appears resolved, but shadow exposed an OOMKilled failure."
			diagnosis.Details = append(diagnosis.Details,
				"Treat this as a second failure to diagnose, not as a verified architecture fix.",
				"Prefer a same-repository multi-arch image or rebuild first; if OOMKilled remains, create a combined resource right-sizing patch and re-run shadow.",
			)
			return diagnosis
		}
		diagnosis.Class = FailureClassCandidateRegression
		diagnosis.Summary = "The original architecture symptom appears resolved, but the candidate introduced or exposed a different failure."
		diagnosis.Details = append(diagnosis.Details, "Reject this candidate unless the new failure is explained and fixed in a follow-up patch that passes shadow.")
		return diagnosis
	}

	// Readiness never passed but the shadow surfaced no recognized error signal
	// in its logs/events: the workload is running yet the probe never reported
	// ready. That points at a probe misconfiguration, not a candidate regression.
	if hasTerminal && attemptReachedRunning(terminal) && shadowReason == "" && !hasRecognizedErrorSignal(shadowText) {
		diagnosis.Class = FailureClassProbeMisconfig
		diagnosis.Summary = "The shadow ran without a recognized error but never passed its readiness probe; this looks like a probe misconfiguration."
		diagnosis.Details = append(diagnosis.Details,
			"Check the readiness/liveness probe path, port, scheme, initialDelaySeconds, and timeoutSeconds against what the container actually serves.",
			"Delivery stays blocked: shadow could not prove readiness, so the candidate is not verified.")
		return diagnosis
	}

	if shadowReason != "" {
		diagnosis.Class = FailureClassCandidateRegression
		diagnosis.Summary = "The shadow clone failed with " + displayReason(shadowReason) + "."
		diagnosis.Details = append(diagnosis.Details, "Review shadow logs/events and generate a revised patch before delivery.")
	}
	return diagnosis
}

func attemptReachedRunning(attempt Attempt) bool {
	return strings.EqualFold(strings.TrimSpace(attempt.Phase), "Running")
}

// hasRecognizedErrorSignal reports whether folded shadow text contains any
// known crash/error fingerprint.
//
// ponytail: the authoritative classifier analyzer.classifyLogSignal is
// unexported, so importing it here would require widening that package's API
// for one boolean. We replicate the minimal "any recognized error signal?"
// check against the same fingerprints instead.
func hasRecognizedErrorSignal(folded string) bool {
	signals := []string{
		"exec format error",
		"permission denied",
		"no space left on device", "disk quota exceeded",
		"no such host", "could not resolve host", "temporary failure in name resolution", "server misbehaving",
		"tls: handshake failure", "x509:", "certificate signed by unknown authority", "certificate has expired",
		"authentication failed", "invalid credentials", "password authentication failed", "401 unauthorized", "access denied",
		"could not connect to server", "econnrefused", "connection refused",
		"context deadline exceeded", "i/o timeout", "request timed out",
		"yaml: line", "json: cannot unmarshal", "failed to parse config", "invalid configuration", "toml:",
		"environment variable", "missing required env", "missing env var",
		"panic:", "goroutine ", "traceback (most recent call last)", "fatal error:", "segmentation fault", "unhandled exception", "referenceerror:", "nameerror:",
		"oomkilled", "crashloopbackoff", "imagepullbackoff", "errimagepull", "createcontainerconfigerror", "back-off",
	}
	for _, s := range signals {
		if strings.Contains(folded, s) {
			return true
		}
	}
	return false
}

func terminalAttempt(result Result) (Attempt, bool) {
	if len(result.Attempts) == 0 {
		return Attempt{}, false
	}
	return result.Attempts[len(result.Attempts)-1], true
}

func attemptFailureReason(attempt Attempt) string {
	if strings.TrimSpace(attempt.ExitReason) != "" {
		return attempt.ExitReason
	}
	if strings.Contains(strings.ToLower(attempt.Message), "timed out") {
		return "timeout"
	}
	return ""
}

func canonicalReason(reason string) string {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, "_", "")
	normalized = strings.ReplaceAll(normalized, " ", "")
	switch {
	case strings.Contains(normalized, "execformaterror"):
		return "execformaterror"
	case strings.Contains(normalized, "oomkilled"):
		return "oomkilled"
	case strings.Contains(normalized, "imagepullbackoff"):
		return "imagepullbackoff"
	case strings.Contains(normalized, "errimagepull"):
		return "errimagepull"
	case strings.Contains(normalized, "crashloopbackoff"):
		return "crashloopbackoff"
	case strings.Contains(normalized, "createcontainerconfigerror"):
		return "createcontainerconfigerror"
	case strings.Contains(normalized, "timeout"):
		return "timeout"
	default:
		return normalized
	}
}

func displayReason(reason string) string {
	switch reason {
	case "execformaterror":
		return "ExecFormatError"
	case "oomkilled":
		return "OOMKilled"
	case "imagepullbackoff":
		return "ImagePullBackOff"
	case "errimagepull":
		return "ErrImagePull"
	case "crashloopbackoff":
		return "CrashLoopBackOff"
	case "createcontainerconfigerror":
		return "CreateContainerConfigError"
	case "timeout":
		return "timeout"
	default:
		return reason
	}
}

func joinAttemptEvidence(attempt Attempt) string {
	var b strings.Builder
	b.WriteString(attempt.ExitReason)
	b.WriteByte('\n')
	b.WriteString(attempt.Message)
	b.WriteByte('\n')
	for _, log := range attempt.Logs {
		b.WriteString(log)
		b.WriteByte('\n')
	}
	for _, event := range attempt.Events {
		b.WriteString(event)
		b.WriteByte('\n')
	}
	return b.String()
}

func joinFindingEvidence(finding analyzer.Finding) string {
	var b strings.Builder
	b.WriteString(finding.Status)
	b.WriteByte('\n')
	b.WriteString(finding.Summary)
	b.WriteByte('\n')
	for _, ev := range finding.Evidence {
		b.WriteString(ev.Label)
		b.WriteString(": ")
		b.WriteString(ev.Value)
		b.WriteByte('\n')
	}
	for _, log := range finding.Logs {
		b.WriteString(log.Source)
		b.WriteString(": ")
		b.WriteString(log.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

func looksLikeStressWorkload(text string) bool {
	needles := []string{
		"stress-ng",
		"/stress",
		"stress:",
		"dispatching hogs",
		"vm-bytes",
		"vm-keep",
		"memtester",
	}
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
