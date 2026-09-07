//go:build e2e_delivery

package e2e

import (
	"os"
	"strings"
	"testing"
	"time"
)

// outcome is what a scenario is expected to achieve. Three fixtures exist to
// prove fixora refuses a fix it cannot make safely — asserting the refusal is
// what catches a safety regression, which a uniform rollout assertion cannot.
type outcome int

const (
	delivered  outcome = iota // patch applied, workload recovers
	adviceOnly                // fix declines to mutate; assert the refusal
)

// refusalMarker is printed by runGuidedFix when the plan is not apply-eligible
// (internal/cli/root.go). Its presence means no mutation was attempted.
const refusalMarker = "No production mutation was attempted."

func assertOutcome(t *testing.T, ns, deploy string, want outcome, stdout string) {
	t.Helper()
	refused := strings.Contains(stdout, refusalMarker)
	switch want {
	case delivered:
		if refused {
			// Fail immediately. Waiting out waitForRollout on a workload
			// nothing patched costs four minutes and proves nothing.
			t.Fatalf("%s: expected delivery but fix refused to mutate", deploy)
		}
		waitForRollout(t, ns, deploy, 4*time.Minute)
	case adviceOnly:
		if !refused {
			t.Fatalf("%s: expected fix to refuse, but it attempted a mutation", deploy)
		}
	}
}

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
		want      outcome
	}

	cases := []tc{
		{"imagepull.yaml", "imagepull-demo", "ImagePullBackOff", "", "typo-container", delivered},
		{"crashloop.yaml", "crashloop-demo", "CrashLoopBackOff", "", "broken-app", adviceOnly},
		{"missing-config.yaml", "missing-config-demo", "CreateContainerConfigError", "", "config-consumer", delivered},
		{"pending.yaml", "pending-demo", "", "Pending", "greedy-container", delivered},
		{"security.yaml", "security-demo", "", "", "restricted-app", adviceOnly},
		{"oomkilled.yaml", "oomkilled-demo", "", "", "memory-hog", delivered},
		{"probe.yaml", "probe-demo", "", "", "web-app", delivered},
		{"dependency.yaml", "dependency-demo", "CrashLoopBackOff", "", "db-client", adviceOnly},
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
				}, func() { dumpScenario(t, ns, "probe-demo") })
			case c.podReason != "":
				waitForPodReason(t, ns, c.deploy, c.podReason)
			case c.phase != "":
				waitForPhase(t, ns, c.deploy, c.phase)
			}

			stdout, stderr, code := fixWithModelFallback(t, ns, c.deploy, c.container, patchOut(t))

			// Always surface the fix output: a zero exit does not mean the
			// workload recovered (advice-only outcomes, silent AI failures),
			// and this is the only window into the diagnose→patch→apply path.
			t.Logf("fix %s (exit %d)\n--- stdout ---\n%s\n--- stderr ---\n%s", c.deploy, code, stdout, stderr)

			if code != 0 {
				t.Fatalf("fix exited %d for %s:\n%s", code, c.deploy, stderr)
			}
			assertOutcome(t, ns, c.deploy, c.want, stdout)
		})
	}
}

// fixWithModelFallback runs the real `fix` pipeline for one scenario. Gemini's
// free-tier request quota is per-project *and per-model*, so when the primary
// model is rate-limited (HTTP 429 / RESOURCE_EXHAUSTED) a lower-traffic model
// still has budget. If FIXORA_E2E_FALLBACK_MODEL is set and the first attempt
// was throttled, it retries once with FIXORA_AI_MODEL overridden to that model.
// With the variable unset it is a plain single `fix` call.
func fixWithModelFallback(t *testing.T, ns, deploy, container, out string) (string, string, int) {
	t.Helper()
	args := []string{
		"fix", "deployment/" + deploy,
		"-n", ns, "--container", container,
		"--delivery", "cluster", "--yes", "--verbose",
		"--out", out,
		"--shadow-timeout", "90s",
	}
	stdout, stderr, code := fixora(t, args...)

	fallback := strings.TrimSpace(os.Getenv("FIXORA_E2E_FALLBACK_MODEL"))
	if fallback == "" || !aiRateLimited(stderr) {
		return stdout, stderr, code
	}
	t.Logf("AI rate-limited for %s; retrying on fallback model %q", deploy, fallback)
	time.Sleep(5 * time.Second)
	return fixoraEnv(t, []string{"FIXORA_AI_MODEL=" + fallback}, args...)
}

// aiRateLimited reports whether fix's stderr shows the AI provider refused the
// call for quota or rate-limit reasons rather than a genuine analysis outcome.
func aiRateLimited(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "http 429") ||
		strings.Contains(s, "resource_exhausted") ||
		strings.Contains(s, "quota") ||
		strings.Contains(s, "rate limit")
}
