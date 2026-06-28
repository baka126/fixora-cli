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
	CompletionSucceeded = "completion-succeeded"
	CompletionFailed    = "completion-failed"
	CompletionPending   = "completion-pending"
	CompletionUnknown   = "completion-unknown"

	CronJobHealthy   = "cronjob-healthy"
	CronJobFailing   = "cronjob-failing"
	CronJobSuspended = "cronjob-suspended"
	CronJobUnknown   = "cronjob-unknown"
)

// CompletionChecker is the testability seam for Job/CronJob verification.
// kube.Kubectl implements it.
type CompletionChecker interface {
	JobStatus(ctx context.Context, name, namespace string, timeout time.Duration) (kube.JobState, error)
	CronJobStatus(ctx context.Context, name, namespace string) (kube.CronJobState, error)
	GetEvents(ctx context.Context, namespace, fieldSelector string) ([]kube.Event, error)
}

// VerifyCompletion observes whether a Job completed or a CronJob is healthy
// after an apply. It never mutates the cluster and never triggers a run.
func VerifyCompletion(ctx context.Context, checker CompletionChecker, finding analyzer.Finding, plan fix.Plan, timeout time.Duration) RolloutOutcome {
	kind := strings.ToLower(strings.TrimSpace(finding.ResourceKind))
	name := strings.TrimSpace(finding.ResourceName)
	ns := strings.TrimSpace(finding.Namespace)
	if name == "" {
		return RolloutOutcome{Class: RolloutInvalid, Summary: "could not verify completion: missing resource name"}
	}
	switch kind {
	case "job":
		return verifyJob(ctx, checker, finding, name, ns, timeout)
	case "cronjob":
		return verifyCronJob(ctx, checker, finding, name, ns)
	default:
		return RolloutOutcome{Class: RolloutSkipped, Summary: "completion verification is not applicable to " + finding.ResourceKind}
	}
}

func verifyJob(ctx context.Context, checker CompletionChecker, finding analyzer.Finding, name, ns string, timeout time.Duration) RolloutOutcome {
	state, err := checker.JobStatus(ctx, name, ns, timeout)
	if err != nil {
		return RolloutOutcome{Class: CompletionUnknown, Summary: "could not verify Job completion: " + err.Error()}
	}
	if state.Complete {
		return RolloutOutcome{Class: CompletionSucceeded, Summary: "Job/" + name + " completed successfully (" + state.Detail + ")"}
	}
	if !state.Failed {
		return RolloutOutcome{Class: CompletionPending, Summary: "Job/" + name + " is still running after the timeout; not yet complete — check later (" + state.Detail + ")"}
	}
	outcome := RolloutOutcome{Class: CompletionFailed, Summary: "Job/" + name + " failed before completing (" + state.Detail + ")"}
	enrichCompletionFailure(ctx, checker, ns, "Job", name, state.Detail, &outcome)
	outcome.Rollback = completionRemediation(finding)
	return outcome
}

func verifyCronJob(ctx context.Context, checker CompletionChecker, finding analyzer.Finding, name, ns string) RolloutOutcome {
	state, err := checker.CronJobStatus(ctx, name, ns)
	if err != nil {
		return RolloutOutcome{Class: CronJobUnknown, Summary: "could not verify CronJob: " + err.Error()}
	}
	if state.Suspended {
		return RolloutOutcome{Class: CronJobSuspended, Summary: "CronJob/" + name + " is suspended and will not run until resumed"}
	}
	if state.RecentJobFailed {
		outcome := RolloutOutcome{Class: CronJobFailing, Summary: "CronJob/" + name + " is currently producing failed runs (" + state.Detail + "); note: validate-only — this run may predate the fix"}
		enrichCompletionFailure(ctx, checker, ns, "Job", state.RecentJobName, state.Detail, &outcome)
		outcome.Rollback = completionRemediation(finding)
		return outcome
	}
	summary := "CronJob/" + name + " accepted (schedule " + state.Schedule + ")"
	if state.LastSuccessful != "" {
		summary += "; last successful run " + state.LastSuccessful
	}
	summary += "; validate-only — the next scheduled run is not awaited"
	return RolloutOutcome{Class: CronJobHealthy, Summary: summary}
}

func enrichCompletionFailure(ctx context.Context, checker CompletionChecker, ns, kind, name, detail string, outcome *RolloutOutcome) {
	if events, evErr := checker.GetEvents(ctx, ns, ""); evErr == nil {
		for _, ev := range events {
			if completionEventMatches(ev, kind, name, ns) {
				outcome.Events = append(outcome.Events, ev.Type+" "+ev.Reason+": "+ev.Message)
			}
		}
	}
	outcome.CauseHints = rolloutCauseHints(append([]string{detail}, outcome.Events...))
}

func completionEventMatches(ev kube.Event, kind, name, namespace string) bool {
	involved := ev.InvolvedObject
	if !strings.EqualFold(strings.TrimSpace(involved.Kind), strings.TrimSpace(kind)) {
		return false
	}
	if strings.TrimSpace(involved.Name) != name {
		return false
	}
	return namespace == "" || strings.TrimSpace(involved.Namespace) == namespace
}

// completionRemediation builds the consent-gated, kind-specific "stop the
// bleeding" command. Args are set structurally so JSON-patch quoting is safe.
func completionRemediation(finding analyzer.Finding) Rollback {
	name := strings.TrimSpace(finding.ResourceName)
	ns := strings.TrimSpace(finding.Namespace)
	revert := "or re-apply the last known-good manifest / git revert the change"
	switch strings.ToLower(strings.TrimSpace(finding.ResourceKind)) {
	case "job":
		args := []string{"delete", "job", name}
		cmd := "kubectl delete job/" + name
		if ns != "" {
			args = append(args, "-n", ns)
			cmd += " -n " + ns
		}
		return Rollback{
			Resource:  "Job/" + name,
			Namespace: ns,
			Binary:    "kubectl",
			Args:      args,
			Command:   cmd,
			Warnings:  []string{"clears the failed Job so it can be recreated; this halts damage, it is not an undo", revert},
		}
	case "cronjob":
		patch := `{"spec":{"suspend":true}}`
		args := []string{"patch", "cronjob", name}
		cmd := "kubectl patch cronjob/" + name
		if ns != "" {
			args = append(args, "-n", ns)
			cmd += " -n " + ns
		}
		args = append(args, "-p", patch)
		cmd += " -p '" + patch + "'"
		return Rollback{
			Resource:  "CronJob/" + name,
			Namespace: ns,
			Binary:    "kubectl",
			Args:      args,
			Command:   cmd,
			Warnings:  []string{"suspends the CronJob to halt further runs; resume with suspend:false; this is not an undo", revert},
		}
	}
	return Rollback{}
}
