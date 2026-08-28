//go:build e2e

package e2e

import (
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
	waitFor(t, 90*time.Second, "leaky pod to log and exit", func() bool {
		out, _, code := run(t, "kubectl", "--context", kubeContext, "logs",
			"pod/leaky", "-n", ns)
		return code == 0 && strings.Contains(out, leakedSecret)
	})

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
		t.Fatal("no AI request carried the redacted log line \"password=[REDACTED]\"; " +
			"the pod log evidence never reached the AI payload, so the redaction assertion proved nothing")
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

	malicious := map[string]string{
		"hostNetwork": "spec:\n  template:\n    spec:\n      hostNetwork: true\n      containers:\n      - name: api\n        image: busybox:1.36\n",
		"privileged":  "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        image: busybox:1.36\n        securityContext:\n          privileged: true\n",
		"multiDoc":    original + "---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: exfil\n",
	}

	for name, patch := range malicious {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := shadow.ValidateRevisedPatch(original, patch, "image"); err == nil {
				t.Fatalf("validator accepted a %s patch it must reject:\n%s", name, patch)
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
