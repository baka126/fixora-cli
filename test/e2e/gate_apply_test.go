//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestApplyGateBlocksIneligiblePlan is the negative case: with no --image
// supplied the plan keeps its TODO_ placeholder, ApplyEligible is false, and
// the command must refuse to mutate the cluster.
func TestApplyGateBlocksIneligiblePlan(t *testing.T) {
	t.Parallel()
	ns := newNamespace(t)
	applyFixture(t, ns, "broken-deploy.yaml")
	waitForNotReady(t, ns, "broken-api")

	before := specOf(t, ns, "deployment/broken-api")

	// --quick suppresses shadow (there is no --no-shadow flag); --no-ai keeps
	// this test independent of any AI provider.
	stdout, stderr, _ := fixora(t, "fix", "deployment/broken-api",
		"-n", ns, "--quick", "--no-ai", "--delivery", "cluster", "--yes")

	// An ineligible plan is a successful "declined to act" outcome: the guided
	// walkthrough prints this and exits 0. The exit code is not the contract —
	// the live spec staying byte-identical is.
	if !strings.Contains(stdout, "No production mutation was attempted.") {
		t.Fatalf("expected the walkthrough to decline explicitly; stdout=%s stderr=%s", stdout, stderr)
	}
	requireUnchanged(t, ns, "deployment/broken-api", before)
}

// TestApplyGateAllowsEligiblePlan is the positive case. Without it, a gate
// that refuses everything would pass the negative test while being broken.
func TestApplyGateAllowsEligiblePlan(t *testing.T) {
	t.Parallel()
	ns := newNamespace(t)
	applyFixture(t, ns, "broken-deploy.yaml")
	waitForNotReady(t, ns, "broken-api")

	before := specOf(t, ns, "deployment/broken-api")

	_, stderr, code := fixora(t, "fix", "deployment/broken-api",
		"-n", ns, "--quick", "--no-ai", "--delivery", "cluster", "--yes",
		"--container", "api", "--image", "public.ecr.aws/docker/library/busybox:1.36")

	if code != 0 {
		t.Fatalf("a concrete apply-eligible plan should apply; exit=%d stderr=%s", code, stderr)
	}
	after := specOf(t, ns, "deployment/broken-api")
	if after == before {
		t.Fatal("spec unchanged after an apply-eligible fix; the gate let nothing through")
	}
	if !strings.Contains(after, "busybox:1.36") {
		t.Fatalf("expected the pinned image in the live spec, got:\n%s", after)
	}
}
