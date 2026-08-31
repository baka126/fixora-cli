//go:build e2e

package e2e

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type planView struct {
	Strategy      string `json:"strategy"`
	ApplyEligible bool   `json:"applyEligible"`
	Status        string `json:"status"`
	PatchTemplate string `json:"patchTemplate"`
}

// parsePlanView unmarshals a fix.Plan JSON document into the fields the
// scenario suites assert on. Split out so it can be unit-tested without a
// cluster.
func parsePlanView(t *testing.T, data []byte) planView {
	t.Helper()
	var pv planView
	if err := json.Unmarshal(data, &pv); err != nil {
		t.Fatalf("plan JSON did not parse: %v\n%s", err, data)
	}
	return pv
}

// planJSON runs `fixora why <ref> -n <ns> --no-ai -o json` (plus any extra
// flags) and returns the parsed plan. `why -o json` prints the concretized
// fix.Plan and exits; `fix` never does.
func planJSON(t *testing.T, ns, ref string, extraArgs ...string) planView {
	t.Helper()
	args := append([]string{"why", ref, "-n", ns, "--no-ai", "-o", "json"}, extraArgs...)
	stdout, stderr, code := fixora(t, args...)
	if code != 0 {
		t.Fatalf("why %s exited %d: %s", ref, code, stderr)
	}
	return parsePlanView(t, []byte(stdout))
}

// waitForPodReason polls until a pod labelled app=<deploy> reports reason in a
// container waiting- or terminated-state reason.
func waitForPodReason(t *testing.T, ns, deploy, reason string) {
	t.Helper()
	waitFor(t, 150*time.Second, deploy+" pods to report "+reason, func() bool {
		out, _, code := run(t, "kubectl", "--context", kubeContext, "get", "pods",
			"-n", ns, "-l", "app="+deploy,
			"-o", "jsonpath={.items[*].status.containerStatuses[*].state.waiting.reason} {.items[*].status.containerStatuses[*].state.terminated.reason}")
		return code == 0 && strings.Contains(out, reason)
	})
}

// waitForPhase polls a deployment's first pod until its phase matches.
func waitForPhase(t *testing.T, ns, deploy, phase string) {
	t.Helper()
	waitFor(t, 90*time.Second, deploy+" pod to reach phase "+phase, func() bool {
		out, _, code := run(t, "kubectl", "--context", kubeContext, "get", "pods",
			"-n", ns, "-l", "app="+deploy, "-o", "jsonpath={.items[*].status.phase}")
		return code == 0 && strings.Contains(out, phase)
	})
}

// waitForRollout polls until every replica of deploy is available.
func waitForRollout(t *testing.T, ns, deploy string, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, deploy+" to become fully available", func() bool {
		want, _, c1 := run(t, "kubectl", "--context", kubeContext, "get", "deployment/"+deploy,
			"-n", ns, "-o", "jsonpath={.spec.replicas}")
		got, _, c2 := run(t, "kubectl", "--context", kubeContext, "get", "deployment/"+deploy,
			"-n", ns, "-o", "jsonpath={.status.availableReplicas}")
		return c1 == 0 && c2 == 0 && strings.TrimSpace(want) != "" && strings.TrimSpace(want) == strings.TrimSpace(got)
	})
}

// waitForLog polls the logs of pods labelled app=<deploy> until they contain
// substr (case-insensitive). Used by scenarios whose classification depends on
// a specific log line the analyzer keys off.
func waitForLog(t *testing.T, ns, deploy, substr string) {
	t.Helper()
	waitFor(t, 120*time.Second, deploy+" logs to contain "+substr, func() bool {
		out, _, code := run(t, "kubectl", "--context", kubeContext, "logs",
			"-n", ns, "-l", "app="+deploy, "--tail=-1")
		return code == 0 && strings.Contains(strings.ToLower(out), strings.ToLower(substr))
	})
}
