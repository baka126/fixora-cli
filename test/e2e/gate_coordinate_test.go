//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestCoordinateRollbackRestoresPriorSpec exercises the coordinate saga's
// central promise — that a failed step rolls the already-applied prefix back.
//
// Until now that promise was only covered by unit tests using fakes, so the
// real Capture/Restore round-trip against an API server was never verified.
// It is the path that matters most: a coordinated set that mutates production
// and then cannot undo itself is worse than one that never ran.
//
// Note the deliberate absence of --yes. coordinateConfirmers disables
// auto-rollback under --yes (internal/cli/coordinate.go:141) so a
// non-interactive run never silently reverts production, which means the
// rollback path is reachable only interactively. The harness feeds synthetic
// "y" on stdin, answering both the apply and the rollback prompt.
func TestCoordinateRollbackRestoresPriorSpec(t *testing.T) {
	t.Parallel()
	ns := newNamespace(t)

	applyDeployWithMeta(t, ns, "coord-a", nil, nil)
	applyDeployWithMeta(t, ns, "coord-b", nil, nil)
	waitForImagePullFailure(t, ns, "coord-a")
	waitForImagePullFailure(t, ns, "coord-b")

	// A concrete image, so the plan is apply-eligible and the set clears
	// preflight and actually mutates — but one that will never pull, so health
	// verification fails and the saga must roll back what it applied.
	stdout, stderr, code := fixora(t, "coordinate",
		"deployment/coord-a", "deployment/coord-b",
		"-n", ns, "--no-ai",
		"--rollout-timeout", "30s",
		"--container", "api", "--image", "registry.k8s.io/pause:no-such-tag-e2e")

	if code == 0 {
		t.Fatalf("a coordinated set whose health check failed must not exit 0; stdout=%s stderr=%s", stdout, stderr)
	}

	after := specOf(t, ns, "deployment/coord-a")
	if strings.Contains(after, "no-such-tag-e2e") {
		t.Fatalf("the failed mutation is still live — rollback did not restore coord-a:\n%s", after)
	}
	if !strings.Contains(after, "does-not-exist:e2e") {
		t.Fatalf("rollback did not restore coord-a's original image; the prior spec is lost:\n%s", after)
	}
	if !strings.Contains(stdout, "rolled-back") {
		t.Fatalf("report should mark the applied prefix rolled-back; stdout=%s", stdout)
	}
}

// TestCoordinatePreflightAbortMutatesNothing pins the fail-closed half: if any
// step is ineligible, the whole set must abort before touching the cluster.
// Without this, a passing rollback test alone could not distinguish "rolled
// back correctly" from "never applied anything in the first place".
func TestCoordinatePreflightAbortMutatesNothing(t *testing.T) {
	t.Parallel()
	ns := newNamespace(t)

	applyDeployWithMeta(t, ns, "pre-a", nil, nil)
	// pre-b carries a Helm marker, so it is source-managed and preflight must
	// refuse the entire set on its behalf.
	applyDeployWithMeta(t, ns, "pre-b", map[string]string{"app.kubernetes.io/managed-by": "Helm"}, nil)
	waitForImagePullFailure(t, ns, "pre-a")
	waitForImagePullFailure(t, ns, "pre-b")

	beforeA := specOf(t, ns, "deployment/pre-a")
	beforeB := specOf(t, ns, "deployment/pre-b")

	stdout, stderr, code := fixora(t, "coordinate",
		"deployment/pre-a", "deployment/pre-b",
		"-n", ns, "--no-ai",
		"--container", "api", "--image", "registry.k8s.io/pause:3.9")

	if code != 2 {
		t.Fatalf("preflight abort should exit 2, got %d; stdout=%s stderr=%s", code, stdout, stderr)
	}
	requireUnchanged(t, ns, "deployment/pre-a", beforeA)
	requireUnchanged(t, ns, "deployment/pre-b", beforeB)
}
