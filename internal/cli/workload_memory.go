package cli

import (
	"math"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"github.com/fixora/kubectl-fixora/internal/workloadmemory"
)

func workloadMemorySession(finding analyzer.Finding, plan fix.Plan, result shadow.Result) workloadmemory.Session {
	session := workloadmemory.Session{At: time.Now().UTC(), FailureClass: finding.Status, Strategy: plan.Strategy, Parity: result.Parity}
	if result.Verified && result.Status == shadow.VerificationVerified {
		session.Outcome = workloadmemory.OutcomeVerified
		session.RepairMemoryLimitBytes = workloadmemory.SafeRepairMemoryLimit(result.VerifiedPatch)
	} else {
		switch result.Status {
		case shadow.VerificationFailed:
			session.Outcome = workloadmemory.OutcomeFailed
		case shadow.VerificationBlocked:
			session.Outcome = workloadmemory.OutcomeBlocked
		default:
			session.Outcome = workloadmemory.OutcomeInconclusive
		}
	}
	evidence := result.Evidence
	if evidence == nil {
		for i := len(result.Attempts) - 1; i >= 0; i-- {
			if result.Attempts[i].Evidence != nil {
				evidence = result.Attempts[i].Evidence
				break
			}
		}
	}
	if evidence != nil {
		if evidence.TimeToReadySeconds != nil && *evidence.TimeToReadySeconds > 0 {
			session.TimeToReadySeconds = *evidence.TimeToReadySeconds
		}
		if memory := evidence.MemoryAssessment; memory != nil {
			session.MemoryPeakBytes = memory.PeakBytes
			session.MemoryLimitBytes = memory.LimitBytes
			if memory.HeadroomPercent > 0 {
				session.MemoryHeadroomPercent = int(math.Round(memory.HeadroomPercent))
			}
		}
	}
	return session
}
