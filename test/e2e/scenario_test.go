//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestScenarioDiagnosis asserts fixora classifies each real failure archetype
// correctly against a live cluster: the finding status and the plan strategy
// match the archetype. It never mutates — `why -o json` only reads and plans.
//
// This is the coverage the fake-reader unit tests cannot give: it proves the
// analyzer sees real kubelet/API output the way those tests assume.
func TestScenarioDiagnosis(t *testing.T) {
	type tc struct {
		fixture      string   // file under fixtures/scenarios/
		deploy       string   // Deployment + app label
		podReason    string   // container state reason to wait for ("" => use phase)
		phase        string   // pod phase to wait for when podReason == ""
		wantStatus   string   // substring required in the plan/scan status
		wantStrategy string   // exact plan.Strategy
		concrete     []string // extra `why` flags that make the plan apply-eligible
	}

	cases := []tc{
		{
			fixture: "imagepull.yaml", deploy: "imagepull-demo",
			podReason:  "ImagePullBackOff",
			wantStatus: "ImagePull", wantStrategy: "image",
			concrete: []string{"--container", "typo-container", "--image", "public.ecr.aws/docker/library/nginx:1.27"},
		},
		{
			fixture: "crashloop.yaml", deploy: "crashloop-demo",
			podReason:  "CrashLoopBackOff",
			wantStatus: "CrashLoopBackOff", wantStrategy: "runtime",
		},
		{
			fixture: "missing-config.yaml", deploy: "missing-config-demo",
			podReason:  "CreateContainerConfigError",
			wantStatus: "CreateContainerConfigError", wantStrategy: "env",
		},
		{
			fixture: "pending.yaml", deploy: "pending-demo",
			phase:      "Pending",
			wantStatus: "", wantStrategy: "", // asserted specially below
		},
		{
			fixture: "security.yaml", deploy: "security-demo",
			podReason:  "", // the container runs then crashes; wait on the log line instead
			phase:      "", // handled specially: wait for the permission-denied log
			wantStatus: "PermissionDenied", wantStrategy: "security",
		},
	}

	for _, c := range cases {
		c := c
		t.Run(strings.TrimSuffix(c.fixture, ".yaml"), func(t *testing.T) {
			t.Parallel()
			ns := newNamespace(t)
			t.Cleanup(func() {
				if t.Failed() {
					dumpScenario(t, ns, c.deploy)
				}
			})
			applyFixture(t, ns, "scenarios/"+c.fixture)

			switch {
			case c.deploy == "security-demo":
				// readOnlyRootFilesystem makes `touch` fail; the container echoes
				// "permission denied" and exits 1, so the pod reaches
				// CrashLoopBackOff. podProblem then builds a finding, --include-logs
				// collects the log, and classifyLogSignal upgrades the status to
				// PermissionDenied. Wait for the CrashLoopBackOff reason.
				waitForPodReason(t, ns, "security-demo", "CrashLoopBackOff")
			case c.deploy == "crashloop-demo":
				// The container crashes on start. Wait for CrashLoopBackOff and
				// a couple of restarts so the backoff window is comfortably
				// wide before `why` scans — otherwise a scan can land in the
				// brief moment the container is re-running and see a healthy pod.
				waitForPodReason(t, ns, "crashloop-demo", "CrashLoopBackOff")
				waitForRestarts(t, ns, "crashloop-demo", 2)
			case c.deploy == "pending-demo":
				// Phase Pending matches any pod still scheduling or pulling. Wait
				// for the real signal: the PodScheduled=Unschedulable condition.
				waitForUnschedulable(t, ns, "pending-demo")
			case c.podReason != "":
				waitForPodReason(t, ns, c.deploy, c.podReason)
			case c.phase != "":
				waitForPhase(t, ns, c.deploy, c.phase)
			}

			var plan planView
			if c.wantStatus != "" {
				plan = planJSONUntil(t, ns, "deployment/"+c.deploy, c.wantStatus, c.concrete...)
			} else {
				plan = planJSON(t, ns, "deployment/"+c.deploy, c.concrete...)
			}

			if c.deploy == "pending-demo" {
				// pending has no single canonical status string across k8s
				// versions; assert fixora produced *a* scheduling plan.
				if plan.Status == "" {
					t.Fatalf("expected a non-empty status for an unschedulable pod, got plan %+v", plan)
				}
				if plan.Strategy == "collect-evidence" || plan.Strategy == "" {
					t.Fatalf("expected an actionable strategy for a pending pod, got %q", plan.Strategy)
				}
				return
			}

			if !strings.Contains(plan.Status, c.wantStatus) {
				t.Fatalf("status: want substring %q, got %q (plan %+v)", c.wantStatus, plan.Status, plan)
			}
			if plan.Strategy != c.wantStrategy {
				t.Fatalf("strategy: want %q, got %q", c.wantStrategy, plan.Strategy)
			}

			if len(c.concrete) > 0 {
				if !plan.ApplyEligible {
					t.Fatalf("a concretized plan should be apply-eligible; got %+v", plan)
				}
				if strings.Contains(plan.PatchTemplate, "TODO_") {
					t.Fatalf("concretized patch still has a TODO_ placeholder:\n%s", plan.PatchTemplate)
				}
			}
		})
	}
}
