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

type fakeRolloutChecker struct {
	ok     bool
	output string
	err    error
	events []kube.Event
}

func (f fakeRolloutChecker) RolloutStatus(_ context.Context, _, _, _ string, _ time.Duration) (bool, string, error) {
	return f.ok, f.output, f.err
}

func (f fakeRolloutChecker) GetEvents(_ context.Context, _, _ string) ([]kube.Event, error) {
	return f.events, nil
}

func deployFinding() analyzer.Finding {
	return analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "prod"}
}

func planWithRollback() fix.Plan {
	return fix.Plan{RollbackCommand: "kubectl rollout undo deployment/api -n prod"}
}

func TestVerifyRolloutHealthy(t *testing.T) {
	out := VerifyRollout(context.Background(), fakeRolloutChecker{ok: true, output: "deployment \"api\" successfully rolled out"}, deployFinding(), planWithRollback(), time.Minute)
	if out.Class != RolloutHealthy {
		t.Fatalf("got %q want %q", out.Class, RolloutHealthy)
	}
	if len(out.Events) != 0 || out.Rollback.Command != "" {
		t.Fatalf("healthy outcome must carry no events/rollback: %#v", out)
	}
}

func TestVerifyRolloutTimeout(t *testing.T) {
	checker := fakeRolloutChecker{
		ok:     false,
		output: "Waiting for deployment rollout to finish: 1 of 3 updated replicas are available",
		events: []kube.Event{
			{Type: "Warning", Reason: "FailedCreate", Message: "deployment api failed", InvolvedObject: kube.ObjectReference{Kind: "Deployment", Name: "api", Namespace: "prod"}},
			// Same name, different namespace: must NOT be matched (cross-namespace collision guard).
			{Type: "Warning", Reason: "FailedCreate", Message: "other-ns noise", InvolvedObject: kube.ObjectReference{Kind: "Deployment", Name: "api", Namespace: "staging"}},
			// Same name and namespace, different kind: must NOT be matched.
			{Type: "Warning", Reason: "FailedCreate", Message: "service noise", InvolvedObject: kube.ObjectReference{Kind: "Service", Name: "api", Namespace: "prod"}},
		},
	}
	out := VerifyRollout(context.Background(), checker, deployFinding(), planWithRollback(), time.Minute)
	if out.Class != RolloutTimeout {
		t.Fatalf("got %q want %q", out.Class, RolloutTimeout)
	}
	if len(out.Events) != 1 {
		t.Fatalf("expected 1 matched event (namespace-scoped), got %#v", out.Events)
	}
	if out.Rollback.Command == "" {
		t.Fatalf("failed rollout must attach a rollback offer")
	}
}

func TestVerifyRolloutStuckOnProgressDeadline(t *testing.T) {
	out := VerifyRollout(context.Background(), fakeRolloutChecker{ok: false, output: "error: deployment \"api\" exceeded its progress deadline"}, deployFinding(), planWithRollback(), time.Minute)
	if out.Class != RolloutStuck {
		t.Fatalf("got %q want %q", out.Class, RolloutStuck)
	}
}

func TestVerifyRolloutSkipsUnsupportedKind(t *testing.T) {
	f := analyzer.Finding{ResourceKind: "Pod", ResourceName: "api-0", Namespace: "prod"}
	out := VerifyRollout(context.Background(), fakeRolloutChecker{ok: false}, f, planWithRollback(), time.Minute)
	if out.Class != RolloutSkipped {
		t.Fatalf("got %q want %q", out.Class, RolloutSkipped)
	}
}

func TestVerifyRolloutInvalidWhenSupportedKindHasNoName(t *testing.T) {
	f := analyzer.Finding{ResourceKind: "Deployment", Namespace: "prod"}
	out := VerifyRollout(context.Background(), fakeRolloutChecker{ok: false}, f, planWithRollback(), time.Minute)
	if out.Class != RolloutInvalid {
		t.Fatalf("got %q want %q", out.Class, RolloutInvalid)
	}
	if !strings.Contains(out.Summary, "missing resource name") {
		t.Fatalf("expected missing name summary, got %#v", out)
	}
}

func TestVerifyRolloutUnknownOnCheckerError(t *testing.T) {
	out := VerifyRollout(context.Background(), fakeRolloutChecker{ok: false, err: errString("forbidden")}, deployFinding(), planWithRollback(), time.Minute)
	if out.Class != RolloutUnknown {
		t.Fatalf("got %q want %q", out.Class, RolloutUnknown)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
