//go:build e2e_delivery

package e2e

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestScenarioDelivery runs the real fix pipeline — diagnose, AI patch,
// shadow-verify, apply — against every curated failure scenario and asserts
// the workload recovers. It mutates the cluster. It needs a real AI provider
// (FIXORA_AI_* env) and runs only via the e2e-delivery workflow.
func TestScenarioDelivery(t *testing.T) {
	if os.Getenv("FIXORA_AI_API_KEY") == "" {
		t.Skip("FIXORA_AI_API_KEY unset; the delivery suite needs a real provider")
	}

	type tc struct {
		fixture   string
		deploy    string
		podReason string // "" => wait on phase
		phase     string
		container string // passed to `fix` so it knows which container; the AI supplies the fix
	}

	cases := []tc{
		{"imagepull.yaml", "imagepull-demo", "ImagePullBackOff", "", "typo-container"},
		{"crashloop.yaml", "crashloop-demo", "CrashLoopBackOff", "", "broken-app"},
		{"missing-config.yaml", "missing-config-demo", "CreateContainerConfigError", "", "config-consumer"},
		{"pending.yaml", "pending-demo", "", "Pending", "greedy-container"},
		{"security.yaml", "security-demo", "", "", "restricted-app"},
		{"oomkilled.yaml", "oomkilled-demo", "", "", "memory-hog"},
		{"probe.yaml", "probe-demo", "", "", "web-app"},
		{"dependency.yaml", "dependency-demo", "CrashLoopBackOff", "", "db-client"},
	}

	for _, c := range cases {
		c := c
		t.Run(strings.TrimSuffix(c.fixture, ".yaml"), func(t *testing.T) {
			t.Parallel()
			ns := newNamespace(t)
			applyFixture(t, ns, "scenarios/"+c.fixture)

			switch {
			case c.deploy == "security-demo":
				waitForPodReason(t, ns, "security-demo", "CrashLoopBackOff")
			case c.deploy == "oomkilled-demo":
				waitForPodReason(t, ns, "oomkilled-demo", "OOMKilled")
			case c.deploy == "probe-demo":
				// Pod runs but never becomes Ready. Wait for that.
				waitFor(t, 90*time.Second, "probe-demo to be running-not-ready", func() bool {
					out, _, code := run(t, "kubectl", "--context", kubeContext, "get", "pods",
						"-n", ns, "-l", "app=probe-demo",
						"-o", "jsonpath={.items[*].status.containerStatuses[*].ready}")
					return code == 0 && strings.Contains(out, "false")
				})
			case c.podReason != "":
				waitForPodReason(t, ns, c.deploy, c.podReason)
			case c.phase != "":
				waitForPhase(t, ns, c.deploy, c.phase)
			}

			_, stderr, code := fixora(t, "fix", "deployment/"+c.deploy,
				"-n", ns, "--container", c.container,
				"--delivery", "cluster", "--yes",
				"--out", patchOut(t),
				"--shadow-timeout", "90s")

			if code != 0 {
				t.Fatalf("fix could not deliver a working patch for %s (exit %d):\n%s", c.deploy, code, stderr)
			}

			waitForRollout(t, ns, c.deploy, 4*time.Minute)
		})
	}
}
