package cli

import (
	"fmt"
	"math"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"k8s.io/apimachinery/pkg/api/resource"
)

func evidenceCard(finding analyzer.Finding, plan fix.Plan, result shadow.Result, verified bool) string {
	var b strings.Builder
	b.WriteString("Fixora evidence\n")
	target := finding.ResourceKind + "/" + finding.ResourceName
	if result.Resource != "" {
		target = result.Resource
	}
	namespace := finding.Namespace
	if result.Namespace != "" {
		namespace = result.Namespace
	}
	fmt.Fprintf(&b, "Target: %s (%s)\n", cardField(target), cardField(namespace))
	if finding.Status != "" {
		fmt.Fprintf(&b, "Finding: %s\n", cardField(finding.Status))
	}
	verdict := "NOT RUN"
	switch result.Status {
	case shadow.VerificationVerified:
		verdict = "INCONCLUSIVE"
		if verified && result.Verified {
			verdict = "VERIFIED"
		}
	case shadow.VerificationFailed:
		verdict = "FAILED"
	case shadow.VerificationInconclusive:
		verdict = "INCONCLUSIVE"
	case shadow.VerificationBlocked:
		verdict = "BLOCKED"
	}
	fmt.Fprintf(&b, "Verdict: %s\n", verdict)
	if !verified && result.FailureClass != "" {
		fmt.Fprintf(&b, "Shadow result: %s\n", cardField(result.FailureClass))
	}
	if plan.Strategy != "" {
		fmt.Fprintf(&b, "Strategy: %s\n", cardField(plan.Strategy))
	}
	if result.Status == "" {
		b.WriteString("Shadow verification was not run.\n")
	} else {
		fmt.Fprintf(&b, "Attempts: %d\n", len(result.Attempts))
	}
	if result.Status != "" && (result.Parity > 0 || (result.Verified && verified)) {
		fmt.Fprintf(&b, "Parity: %d%%\n", result.Parity)
	}
	evidence := latestCardEvidence(result)
	if evidence != nil {
		if evidence.TimeToReadySeconds != nil && *evidence.TimeToReadySeconds >= 0 {
			fmt.Fprintf(&b, "Ready: %ds\n", *evidence.TimeToReadySeconds)
		}
		if memory := evidence.MemoryAssessment; memory != nil && (memory.PeakBytes > 0 || memory.LimitBytes > 0) {
			b.WriteString("Memory: ")
			if memory.PeakBytes > 0 {
				fmt.Fprintf(&b, "%s peak", formatEvidenceBytes(memory.PeakBytes))
			}
			if memory.LimitBytes > 0 {
				if memory.PeakBytes > 0 {
					b.WriteString(" / ")
				}
				fmt.Fprintf(&b, "%s limit", formatEvidenceBytes(memory.LimitBytes))
			}
			if memory.HeadroomPercent >= 0 && memory.LimitBytes > 0 && memory.PeakBytes > 0 {
				fmt.Fprintf(&b, " (%d%% headroom)", int(math.Round(memory.HeadroomPercent)))
			}
			b.WriteByte('\n')
		}
	}
	if !verified && result.Status != "" {
		b.WriteString("Production: unchanged\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func latestCardEvidence(result shadow.Result) *shadow.FailureEvidence {
	for i := len(result.Attempts) - 1; i >= 0; i-- {
		if result.Attempts[i].Evidence != nil {
			return result.Attempts[i].Evidence
		}
	}
	return result.Evidence
}

func formatEvidenceBytes(value int64) string {
	return resource.NewQuantity(value, resource.BinarySI).String()
}

func cardField(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(value) > 120 {
		value = value[:120]
	}
	return value
}
