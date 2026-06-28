package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
)

type fakeCompletionChecker struct {
	job     kube.JobState
	jobErr  error
	cron    kube.CronJobState
	cronErr error
	events  []kube.Event
}

func (f fakeCompletionChecker) JobStatus(_ context.Context, _, _ string, _ time.Duration) (kube.JobState, error) {
	return f.job, f.jobErr
}
func (f fakeCompletionChecker) CronJobStatus(_ context.Context, _, _ string) (kube.CronJobState, error) {
	return f.cron, f.cronErr
}
func (f fakeCompletionChecker) GetEvents(_ context.Context, _, _ string) ([]kube.Event, error) {
	return f.events, nil
}

func jobFinding() analyzer.Finding {
	return analyzer.Finding{ResourceKind: "Job", ResourceName: "migrate", Namespace: "prod"}
}
func cronFinding() analyzer.Finding {
	return analyzer.Finding{ResourceKind: "CronJob", ResourceName: "nightly", Namespace: "prod"}
}

func TestVerifyCompletionJobSucceeded(t *testing.T) {
	out := VerifyCompletion(context.Background(), fakeCompletionChecker{job: kube.JobState{Complete: true}}, jobFinding(), fix.Plan{}, time.Minute)
	if out.Class != CompletionSucceeded {
		t.Fatalf("got %q want %q", out.Class, CompletionSucceeded)
	}
	if out.Rollback.Command != "" {
		t.Fatalf("succeeded outcome must carry no remediation: %#v", out.Rollback)
	}
}

func TestVerifyCompletionJobFailedAttachesDeleteRemedy(t *testing.T) {
	checker := fakeCompletionChecker{
		job: kube.JobState{Failed: true, Detail: "succeeded 0, failed 6, active 0"},
		events: []kube.Event{
			{Type: "Warning", Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit", InvolvedObject: kube.ObjectReference{Kind: "Job", Name: "migrate", Namespace: "prod"}},
			{Type: "Warning", Reason: "Noise", Message: "same name service noise", InvolvedObject: kube.ObjectReference{Kind: "Service", Name: "migrate", Namespace: "prod"}},
		},
	}
	out := VerifyCompletion(context.Background(), checker, jobFinding(), fix.Plan{}, time.Minute)
	if out.Class != CompletionFailed {
		t.Fatalf("got %q want %q", out.Class, CompletionFailed)
	}
	if len(out.Events) != 1 {
		t.Fatalf("expected 1 matched event, got %#v", out.Events)
	}
	if out.Rollback.Binary != "kubectl" || len(out.Rollback.Args) == 0 || out.Rollback.Args[0] != "delete" {
		t.Fatalf("job failure must attach a kubectl delete remedy, got %#v", out.Rollback)
	}
}

func TestVerifyCompletionJobPendingIsNonBlocking(t *testing.T) {
	out := VerifyCompletion(context.Background(), fakeCompletionChecker{job: kube.JobState{}}, jobFinding(), fix.Plan{}, time.Minute)
	if out.Class != CompletionPending {
		t.Fatalf("got %q want %q", out.Class, CompletionPending)
	}
	if out.Rollback.Command != "" {
		t.Fatalf("pending job must not attach remediation: %#v", out.Rollback)
	}
}

func TestVerifyCompletionJobUnknownOnError(t *testing.T) {
	out := VerifyCompletion(context.Background(), fakeCompletionChecker{jobErr: errString("forbidden")}, jobFinding(), fix.Plan{}, time.Minute)
	if out.Class != CompletionUnknown {
		t.Fatalf("got %q want %q", out.Class, CompletionUnknown)
	}
}

func TestVerifyCompletionMissingNameIsInvalid(t *testing.T) {
	finding := analyzer.Finding{ResourceKind: "Job", Namespace: "prod"}
	out := VerifyCompletion(context.Background(), fakeCompletionChecker{}, finding, fix.Plan{}, time.Minute)
	if out.Class != RolloutInvalid {
		t.Fatalf("got %q want %q", out.Class, RolloutInvalid)
	}
}

func TestVerifyCompletionCronJobHealthy(t *testing.T) {
	out := VerifyCompletion(context.Background(), fakeCompletionChecker{cron: kube.CronJobState{Schedule: "0 * * * *", LastSuccessful: "2026-06-27T00:00:00Z"}}, cronFinding(), fix.Plan{}, time.Minute)
	if out.Class != CronJobHealthy {
		t.Fatalf("got %q want %q", out.Class, CronJobHealthy)
	}
}

func TestVerifyCompletionCronJobFailingAttachesSuspendRemedy(t *testing.T) {
	checker := fakeCompletionChecker{
		cron: kube.CronJobState{Schedule: "0 * * * *", RecentJobFailed: true, RecentJobName: "nightly-123", Detail: "most recent job nightly-123: succeeded 0, failed 1, active 0"},
		events: []kube.Event{
			{Type: "Warning", Reason: "BackoffLimitExceeded", Message: "failed owned job", InvolvedObject: kube.ObjectReference{Kind: "Job", Name: "nightly-123", Namespace: "prod"}},
			{Type: "Warning", Reason: "CronNoise", Message: "cronjob event noise", InvolvedObject: kube.ObjectReference{Kind: "CronJob", Name: "nightly", Namespace: "prod"}},
		},
	}
	out := VerifyCompletion(context.Background(), checker, cronFinding(), fix.Plan{}, time.Minute)
	if out.Class != CronJobFailing {
		t.Fatalf("got %q want %q", out.Class, CronJobFailing)
	}
	if len(out.Events) != 1 || !strings.Contains(out.Events[0], "failed owned job") {
		t.Fatalf("expected only failed Job event enrichment, got %#v", out.Events)
	}
	if out.Rollback.Binary != "kubectl" || len(out.Rollback.Args) == 0 || out.Rollback.Args[0] != "patch" {
		t.Fatalf("cronjob failing must attach a kubectl patch suspend remedy, got %#v", out.Rollback)
	}
}

func TestCompletionRemediationTrimsKind(t *testing.T) {
	job := completionRemediation(analyzer.Finding{ResourceKind: " Job ", ResourceName: "migrate", Namespace: "prod"})
	if job.Binary != "kubectl" || len(job.Args) == 0 || job.Args[0] != "delete" {
		t.Fatalf("padded Job kind must attach delete remediation, got %#v", job)
	}

	cron := completionRemediation(analyzer.Finding{ResourceKind: " CronJob ", ResourceName: "nightly", Namespace: "prod"})
	if cron.Binary != "kubectl" || len(cron.Args) == 0 || cron.Args[0] != "patch" {
		t.Fatalf("padded CronJob kind must attach suspend remediation, got %#v", cron)
	}
}

func TestVerifyCompletionCronJobSuspended(t *testing.T) {
	out := VerifyCompletion(context.Background(), fakeCompletionChecker{cron: kube.CronJobState{Suspended: true, Schedule: "0 * * * *"}}, cronFinding(), fix.Plan{}, time.Minute)
	if out.Class != CronJobSuspended {
		t.Fatalf("got %q want %q", out.Class, CronJobSuspended)
	}
	if out.Rollback.Command != "" {
		t.Fatalf("suspended cronjob must not attach remediation: %#v", out.Rollback)
	}
}

func TestVerifyCompletionCronJobUnknownOnError(t *testing.T) {
	out := VerifyCompletion(context.Background(), fakeCompletionChecker{cronErr: errString("forbidden")}, cronFinding(), fix.Plan{}, time.Minute)
	if out.Class != CronJobUnknown {
		t.Fatalf("got %q want %q", out.Class, CronJobUnknown)
	}
}
