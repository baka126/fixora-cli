package kube

import (
	"errors"
	"testing"
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
