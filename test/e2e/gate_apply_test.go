//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

// patchOut returns a per-test path for the generated patch file.
//
// Without --out the CLI defaults opts.outFile to the RELATIVE
// "fixora-patch.yaml" (internal/cli/root.go:1224,1257) in the package
// directory, and re-reads it by that same path to apply. Under t.Parallel()
// every test would write namespace-stamped patches to one shared file, so a
// test could apply another test's patch into the wrong namespace.
func patchOut(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "fixora-patch.yaml")
}

// readyImage is used by the positive/control cases. It must be an image that
// stays Ready: a successful apply is followed by gateRollout
// (internal/cli/root.go:1306) whose exit code becomes the command's, so an
// image that exits immediately (busybox with no command) times out the rollout
// and exits 1 — and the harness's synthetic "y" stdin would then accept the
// rollback prompt and undo the very mutation the test asserts. pause stays
// running and is preloaded on kind nodes, so it needs no registry pull.
const readyImage = "registry.k8s.io/pause:3.9"

// TestApplyGateBlocksIneligiblePlan is the negative case: with no --image
// supplied the plan keeps its TODO_ placeholder, ApplyEligible is false, and
// the command must refuse to mutate the cluster.
func TestApplyGateBlocksIneligiblePlan(t *testing.T) {
	t.Parallel()
	ns := newNamespace(t)
	applyFixture(t, ns, "broken-deploy.yaml")
	waitForImagePullFailure(t, ns, "broken-api")

	before := specOf(t, ns, "deployment/broken-api")

	// --quick suppresses shadow (there is no --no-shadow flag); --no-ai keeps
	// this test independent of any AI provider.
	stdout, stderr, _ := fixora(t, "fix", "deployment/broken-api",
		"-n", ns, "--quick", "--no-ai", "--delivery", "cluster", "--yes",
		"--out", patchOut(t))

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
	waitForImagePullFailure(t, ns, "broken-api")

	before := specOf(t, ns, "deployment/broken-api")

	_, stderr, code := fixora(t, "fix", "deployment/broken-api",
		"-n", ns, "--quick", "--no-ai", "--delivery", "cluster", "--yes",
		"--out", patchOut(t),
		"--container", "api", "--image", readyImage)

	if code != 0 {
		t.Fatalf("a concrete apply-eligible plan should apply; exit=%d stderr=%s", code, stderr)
	}
	after := specOf(t, ns, "deployment/broken-api")
	if after == before {
		t.Fatal("spec unchanged after an apply-eligible fix; the gate let nothing through")
	}
	if !strings.Contains(after, "pause:3.9") {
		t.Fatalf("expected the pinned image in the live spec, got:\n%s", after)
	}
}
