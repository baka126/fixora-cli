//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/shadow"
)

const leakedSecret = "hunter2supersecret"

// TestRedactionHoldsAtEgress asserts the secret never left the process. This
// is stronger than checking the rendered output: the stub records the request
// body fixora actually sent, so we assert on what crossed the network boundary.
func TestRedactionHoldsAtEgress(t *testing.T) {
	t.Parallel()
	ns := newNamespace(t)
	applyFixture(t, ns, "leaky-pod.yaml")

	// Wait for CrashLoopBackOff specifically. That is the state fixora
	// classifies as a failure (podProblem), and it guarantees a "previous"
	// container run whose stdout holds the credential line — which is what
	// --include-logs collects and redacts. Just seeing the log via kubectl is
	// not enough: the analyzer must also see the pod as broken.
	waitFor(t, 150*time.Second, "leaky pod to reach CrashLoopBackOff", func() bool {
		out, _, code := run(t, "kubectl", "--context", kubeContext, "get", "pod/leaky",
			"-n", ns, "-o", "jsonpath={.status.containerStatuses[*].state.waiting.reason}")
		return code == 0 && strings.Contains(out, "CrashLoopBackOff")
	})
	// Sanity: the crashed run's stdout really does carry the secret.
	logs, _, _ := run(t, "kubectl", "--context", kubeContext, "logs", "pod/leaky",
		"-n", ns, "--previous")
	if !strings.Contains(logs, leakedSecret) {
		t.Fatalf("previous container logs missing the seeded secret; got: %q", logs)
	}

	stub := newAIStub(t, "")
	stdout, stderr, _ := fixoraEnv(t, stub.Env(),
		"why", "pod/leaky", "-n", ns, "--ai", "--redact", "--include-logs", "-o", "json")

	sent := stub.Received()
	if len(sent) == 0 {
		t.Fatal("the AI stub received no request; the test proved nothing about egress")
	}
	for i, body := range sent {
		if strings.Contains(body, leakedSecret) {
			t.Fatalf("secret left the process in AI request %d:\n%s", i, body)
		}
	}
	if strings.Contains(stdout+stderr, leakedSecret) {
		t.Fatal("secret appeared in command output despite --redact")
	}

	// Guard against a vacuous pass. The assertions above hold trivially if the
	// pod log text never reached the AI payload at all (evidence bounding
	// dropped it, Logs() errored, a plumbing regression): a request still goes
	// out carrying finding metadata, so len(sent) > 0 and "secret absent" is
	// true while nothing about log redaction was exercised. The redact rule
	// keeps the "password=" prefix and rewrites only the value
	// (internal/redact/redact.go:22-23), so a redacted log line appears as
	// "password=[REDACTED]". Requiring that in a recorded body proves both
	// halves of the contract: the log text really did cross the egress
	// boundary, and it was redacted on the way.
	redacted := false
	for _, body := range sent {
		if strings.Contains(body, "password=[REDACTED]") {
			redacted = true
			break
		}
	}
	if !redacted {
		// Dump what fixora actually sent so the next CI run shows whether the
		// log was collected at all: --include-logs means the redacted log line
		// belongs in the AI payload, so its absence is either "logs never
		// collected" (finding carried only metadata) or "collected but dropped
		// before the payload". Both are product bugs, not test bugs.
		var dump strings.Builder
		for i, body := range sent {
			if len(body) > 4000 {
				body = body[:4000] + "…(truncated)"
			}
			fmt.Fprintf(&dump, "\n--- AI request %d ---\n%s\n", i, body)
		}
		t.Fatalf("no AI request carried the redacted log line \"password=[REDACTED]\"; "+
			"the pod log evidence never reached the AI payload, so the redaction "+
			"assertion proved nothing.\nfixora stderr:\n%s%s", stderr, dump.String())
	}
}

// TestMaliciousAIPatchRejected drives the shadow allowlist directly. Each
// patch mutates something the validator must never accept.
//
// planType must be one of the four allowed revision strategies ("image",
// "fix-architecture", "resources", "env"); see internal/shadow/validation.go:140.
// Any other value fails the allowlist before the patch is ever inspected, so
// the malicious cases would pass for the wrong reason.
func TestMaliciousAIPatchRejected(t *testing.T) {
	t.Parallel()

	const original = "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        image: busybox:1.36\n"

	// Each case asserts the SPECIFIC rejection reason, not merely that an error
	// came back. Under the "image" strategy, validateProjectedDiff also rejects
	// spec.hostNetwork and containers[].securityContext as out-of-scope changes,
	// independently of the dedicated safety rules — so an err != nil assertion
	// would stay green even if the real rule were deleted. The wanted substrings
	// come from internal/shadow/validation.go:189, :209 and :124.
	malicious := map[string]struct{ patch, want string }{
		"hostNetwork": {
			patch: "spec:\n  template:\n    spec:\n      hostNetwork: true\n      containers:\n      - name: api\n        image: busybox:1.36\n",
			want:  "spec.hostNetwork is not allowed",
		},
		"privileged": {
			patch: "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        image: busybox:1.36\n        securityContext:\n          privileged: true\n",
			want:  "securityContext.privileged is not allowed",
		},
		"multiDoc": {
			patch: original + "---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: exfil\n",
			want:  "multi-document YAML is not allowed",
		},
	}

	for name, tc := range malicious {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := shadow.ValidateRevisedPatch(original, tc.patch, "image")
			if err == nil {
				t.Fatalf("validator accepted a %s patch it must reject:\n%s", name, tc.patch)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s rejected for the wrong reason — want a reason containing %q, got: %v",
					name, tc.want, err)
			}
		})
	}
}

// TestBenignAIPatchAccepted is the control: the validator must not be
// rejecting unconditionally. Both patches are resources-shaped so the
// "resources" strategy is coherent (see validation.go:140).
func TestBenignAIPatchAccepted(t *testing.T) {
	t.Parallel()

	const original = "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        resources:\n          limits:\n            memory: 128Mi\n"
	const benign = "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        resources:\n          limits:\n            memory: 256Mi\n"

	if err := shadow.ValidateRevisedPatch(original, benign, "resources"); err != nil {
		t.Fatalf("validator rejected a benign resources patch: %v", err)
	}
}
