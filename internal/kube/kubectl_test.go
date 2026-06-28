package kube

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestClassifyRolloutResult(t *testing.T) {
	if ok, _, err := classifyRolloutResult("successfully rolled out", nil); !ok || err != nil {
		t.Fatalf("nil error must be healthy, got ok=%v err=%v", ok, err)
	}
	ok, _, err := classifyRolloutResult("", errors.New("error: timed out waiting for the condition"))
	if ok || err != nil {
		t.Fatalf("timeout must be (false,nil), got ok=%v err=%v", ok, err)
	}
	ok, _, err = classifyRolloutResult("error: deployment exceeded its progress deadline", errors.New("exit status 1"))
	if ok || err != nil {
		t.Fatalf("progress-deadline must be (false,nil), got ok=%v err=%v", ok, err)
	}
	ok, _, err = classifyRolloutResult("", errors.New("Error from server (NotFound): deployments.apps \"api\" not found"))
	if ok || err == nil {
		t.Fatalf("real error must propagate, got ok=%v err=%v", ok, err)
	}
}

func TestClassifyJobStatus(t *testing.T) {
	complete := classifyJobStatus(jobStatusJSON{
		Succeeded:  1,
		Conditions: []jobConditionJSON{{Type: "Complete", Status: "True"}},
	})
	if !complete.Complete || complete.Failed {
		t.Fatalf("Complete=True condition must classify complete, got %#v", complete)
	}

	failed := classifyJobStatus(jobStatusJSON{
		Failed:     3,
		Conditions: []jobConditionJSON{{Type: "Failed", Status: "True"}},
	})
	if failed.Complete || !failed.Failed {
		t.Fatalf("Failed=True condition must classify failed, got %#v", failed)
	}

	running := classifyJobStatus(jobStatusJSON{Succeeded: 0})
	if running.Complete || running.Failed {
		t.Fatalf("no terminal condition must classify pending, got %#v", running)
	}

	// A False condition must not count as terminal.
	pending := classifyJobStatus(jobStatusJSON{
		Conditions: []jobConditionJSON{{Type: "Complete", Status: "False"}},
	})
	if pending.Complete || pending.Failed {
		t.Fatalf("Complete=False must remain pending, got %#v", pending)
	}
}

func TestCronJobOwnsJobRequiresUID(t *testing.T) {
	item := jobListItemJSON{}
	item.Metadata.OwnerReferences = []ownerRefJSON{{Kind: "CronJob", Name: "nightly", UID: "old-uid"}}
	if cronJobOwnsJob(item, "nightly", "new-uid") {
		t.Fatal("job from recreated CronJob with stale UID must not match")
	}
	if !cronJobOwnsJob(item, "nightly", "old-uid") {
		t.Fatal("matching CronJob UID must match the owned Job")
	}
}

func TestJobStatusCanceledBeforeObservationReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (Kubectl{}).JobStatus(ctx, "migrate", "prod", time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("unobserved cancellation must propagate, got %v", err)
	}
}

func TestJobStatusPollErrorParentCancellationWinsAfterObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pollCtx, pollCancel := context.WithTimeout(ctx, time.Minute)
	last := JobState{Detail: "succeeded 0, failed 0, active 1"}
	cancel()
	defer pollCancel()

	state, err := jobStatusPollError(ctx, pollCtx, true, last, context.DeadlineExceeded)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation must propagate after observation, got state=%#v err=%v", state, err)
	}
}

func TestJobStatusPollErrorInternalTimeoutReturnsObservedState(t *testing.T) {
	ctx := context.Background()
	pollCtx, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	last := JobState{Detail: "succeeded 0, failed 0, active 1"}

	state, err := jobStatusPollError(ctx, pollCtx, true, last, context.DeadlineExceeded)
	if err != nil {
		t.Fatalf("internal timeout after observation must be non-blocking, got %v", err)
	}
	if state.Detail != last.Detail {
		t.Fatalf("internal timeout must return last observed state, got %#v", state)
	}
}
