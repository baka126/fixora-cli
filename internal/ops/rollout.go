package ops

import (
	"context"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
)

const (
	RolloutHealthy = "rollout-healthy"
	RolloutStuck   = "rollout-stuck"
	RolloutTimeout = "rollout-timeout"
	RolloutSkipped = "rollout-skipped"
	RolloutUnknown = "rollout-unknown"
	RolloutInvalid = "rollout-invalid"
)

// RolloutChecker is the testability seam for post-apply rollout verification.
// kube.Kubectl implements it.
type RolloutChecker interface {
	RolloutStatus(ctx context.Context, kind, name, namespace string, timeout time.Duration) (bool, string, error)
	GetEvents(ctx context.Context, namespace, fieldSelector string) ([]kube.Event, error)
}

type RolloutOutcome struct {
	Class      string   `json:"class"`
	Summary    string   `json:"summary"`
	Events     []string `json:"events,omitempty"`
	CauseHints []string `json:"causeHints,omitempty"`
	Rollback   Rollback `json:"rollback,omitempty"`
}

// VerifyRollout observes whether the live controller reached its availability
// target after an apply. It never mutates the cluster.
func VerifyRollout(ctx context.Context, checker RolloutChecker, finding analyzer.Finding, plan fix.Plan, timeout time.Duration) RolloutOutcome {
	kind := strings.ToLower(strings.TrimSpace(finding.ResourceKind))
	name := strings.TrimSpace(finding.ResourceName)
	ns := strings.TrimSpace(finding.Namespace)
	if name == "" {
		return RolloutOutcome{Class: RolloutInvalid, Summary: "could not verify rollout: missing resource name"}
	}
	if !rolloutSupportedKind(kind) {
		return RolloutOutcome{Class: RolloutSkipped, Summary: "rollout status is not applicable to " + displayKind(finding.ResourceKind) + "; verify completion/readiness manually"}
	}

	ok, output, err := checker.RolloutStatus(ctx, kind, name, ns, timeout)
	if err != nil {
		return RolloutOutcome{Class: RolloutUnknown, Summary: "could not verify rollout: " + err.Error()}
	}
	if ok {
		return RolloutOutcome{Class: RolloutHealthy, Summary: finding.ResourceKind + "/" + name + " rolled out successfully"}
	}

	class := RolloutTimeout
	summary := finding.ResourceKind + "/" + name + " did not complete its rollout within the timeout"
	if strings.Contains(strings.ToLower(output), "progress deadline") {
		class = RolloutStuck
		summary = finding.ResourceKind + "/" + name + " rollout stalled (progress deadline exceeded)"
	}
	outcome := RolloutOutcome{Class: class, Summary: summary}

	if events, evErr := checker.GetEvents(ctx, ns, ""); evErr == nil {
		for _, ev := range events {
			if rolloutEventMatches(ev, kind, name, ns) {
				outcome.Events = append(outcome.Events, ev.Type+" "+ev.Reason+": "+ev.Message)
			}
		}
	}
	outcome.CauseHints = rolloutCauseHints(append([]string{output}, outcome.Events...))
	outcome.Rollback = BuildRollback(finding, plan, true)
	return outcome
}

func rolloutEventMatches(ev kube.Event, kind, name, namespace string) bool {
	involved := ev.InvolvedObject
	if !strings.EqualFold(strings.TrimSpace(involved.Kind), strings.TrimSpace(kind)) {
		return false
	}
	if strings.TrimSpace(involved.Name) != name {
		return false
	}
	return namespace == "" || strings.TrimSpace(involved.Namespace) == namespace
}

func rolloutSupportedKind(kind string) bool {
	switch kind {
	case "deployment", "statefulset", "daemonset":
		return true
	default:
		return false
	}
}

func displayKind(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "this resource"
	}
	return kind
}

func rolloutCauseHints(texts []string) []string {
	joined := strings.ToLower(strings.Join(texts, "\n"))
	var hints []string
	if strings.Contains(joined, "disruption") || strings.Contains(joined, "poddisruptionbudget") {
		hints = append(hints, "a PodDisruptionBudget may be blocking the rollout")
	}
	if strings.Contains(joined, "unavailable") || strings.Contains(joined, "insufficient") {
		hints = append(hints, "not enough replicas became available (check maxUnavailable, resources, scheduling)")
	}
	if strings.Contains(joined, "progress deadline") {
		hints = append(hints, "the controller exceeded its progress deadline")
	}
	return hints
}
