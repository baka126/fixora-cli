//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"strconv"
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
// container's current waiting/terminated state or its last-terminated state.
// lastState matters for reasons like OOMKilled that only sit in the current
// terminated state for the instant between the kill and CrashLoopBackOff.
func waitForPodReason(t *testing.T, ns, deploy, reason string) {
	t.Helper()
	waitFor(t, 150*time.Second, deploy+" pods to report "+reason, func() bool {
		out, _, code := run(t, "kubectl", "--context", kubeContext, "get", "pods",
			"-n", ns, "-l", "app="+deploy,
			"-o", "jsonpath={.items[*].status.containerStatuses[*].state.waiting.reason} {.items[*].status.containerStatuses[*].state.terminated.reason} {.items[*].status.containerStatuses[*].lastState.terminated.reason}")
		return code == 0 && strings.Contains(out, reason)
	}, func() { dumpScenario(t, ns, deploy) })
}

// waitForPhase polls a deployment's first pod until its phase matches.
func waitForPhase(t *testing.T, ns, deploy, phase string) {
	t.Helper()
	waitFor(t, 90*time.Second, deploy+" pod to reach phase "+phase, func() bool {
		out, _, code := run(t, "kubectl", "--context", kubeContext, "get", "pods",
			"-n", ns, "-l", "app="+deploy, "-o", "jsonpath={.items[*].status.phase}")
		return code == 0 && strings.Contains(out, phase)
	}, func() { dumpScenario(t, ns, deploy) })
}

// waitForRollout polls until every replica of deploy has been continuously
// available for a stability streak (~32s). A bare availableReplicas==replicas
// check is not enough: scenarios like crashloop and dependency have no
// readiness probe, so their pod is briefly Ready on every restart cycle and a
// single poll can catch that window while the workload is still broken. The
// streak resets to 0 on any miss and only clears the bar after 16 consecutive
// hits, longer than any fixture's transient Ready window.
func waitForRollout(t *testing.T, ns, deploy string, timeout time.Duration) {
	t.Helper()
	streak := 0
	waitFor(t, timeout, deploy+" to stay fully available for ~32s", func() bool {
		want, _, c1 := run(t, "kubectl", "--context", kubeContext, "get", "deployment/"+deploy,
			"-n", ns, "-o", "jsonpath={.spec.replicas}")
		got, _, c2 := run(t, "kubectl", "--context", kubeContext, "get", "deployment/"+deploy,
			"-n", ns, "-o", "jsonpath={.status.availableReplicas}")
		ok := c1 == 0 && c2 == 0 && strings.TrimSpace(want) != "" && strings.TrimSpace(want) == strings.TrimSpace(got)
		if ok {
			streak++
		} else {
			streak = 0
		}
		return streak >= 16
	}, func() { dumpScenario(t, ns, deploy) })
}

// waitForUnschedulable polls until a pod labelled app=<deploy> reports the
// PodScheduled condition with reason Unschedulable. Stronger than waiting on
// phase Pending, which any freshly-created pod satisfies while it is still
// scheduling or pulling.
func waitForUnschedulable(t *testing.T, ns, deploy string) {
	t.Helper()
	waitFor(t, 90*time.Second, deploy+" to be Unschedulable", func() bool {
		out, _, code := run(t, "kubectl", "--context", kubeContext, "get", "pods",
			"-n", ns, "-l", "app="+deploy,
			"-o", `jsonpath={.items[*].status.conditions[?(@.type=="PodScheduled")].reason}`)
		return code == 0 && strings.Contains(out, "Unschedulable")
	}, func() { dumpScenario(t, ns, deploy) })
}

// waitForRestarts polls until a pod labelled app=<deploy> has restarted at
// least min times — used to ensure a CrashLoopBackOff backoff interval is long
// enough that a following read won't race a container re-run.
func waitForRestarts(t *testing.T, ns, deploy string, min int) {
	t.Helper()
	waitFor(t, 150*time.Second, fmt.Sprintf("%s to restart >= %d times", deploy, min), func() bool {
		out, _, code := run(t, "kubectl", "--context", kubeContext, "get", "pods",
			"-n", ns, "-l", "app="+deploy,
			"-o", "jsonpath={.items[*].status.containerStatuses[*].restartCount}")
		if code != 0 {
			return false
		}
		for _, f := range strings.Fields(out) {
			n, err := strconv.Atoi(f)
			if err == nil && n >= min {
				return true
			}
		}
		return false
	}, func() { dumpScenario(t, ns, deploy) })
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
	}, func() { dumpScenario(t, ns, deploy) })
}
