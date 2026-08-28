//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// binPath is the kubectl-fixora binary under test; kubeContext is the kind
// context. Both are set by TestMain.
var (
	binPath     string
	kubeContext string
	kubeconfig  string
)

// run executes a command and returns stdout, stderr and the exit code. A
// non-zero exit is a value, not an error: exit codes are part of the contract
// under test.
func run(t *testing.T, name string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	// Auto-approve any interactive confirmation. --yes does NOT bypass the
	// guided-fix apply prompt (root.go calls termui.ConfirmApply reading
	// os.Stdin unconditionally); an exec'd process with no stdin gets EOF,
	// the prompt is declined, and nothing applies. Without this a negative
	// gate test could pass for the WRONG reason — prompt declined rather
	// than the safety gate firing. Mirrors the repo's synthetic-stdin
	// convention for prompt confirmation flows.
	cmd.Stdin = strings.NewReader("y\ny\ny\ny\ny\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %s %v: %v", name, args, err)
	}
	return stdout.String(), stderr.String(), code
}

// fixora runs the binary under test against the kind cluster. args are passed
// through untouched: fixora dispatches on args[0], so a prepended global flag
// would break command routing. The exported KUBECONFIG already points at the
// kind cluster (kind writes current-context: kind-<name>), so the context is
// correct without an explicit --context.
func fixora(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	return run(t, binPath, args...)
}

// kubectl runs kubectl against the kind cluster and fails the test on error.
// Context comes from the exported KUBECONFIG, same as fixora.
func kubectl(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, code := run(t, "kubectl", args...)
	if code != 0 {
		t.Fatalf("kubectl %v exited %d: %s", args, code, stderr)
	}
	return stdout
}

// waitFor polls cond until it returns true or the deadline passes. It replaces
// unconditional sleeps, which are the main flake source in cluster tests.
func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, desc)
}

// newNamespace creates a namespace named for the test and deletes it on
// cleanup. Per-test namespaces are what make t.Parallel() safe here.
func newNamespace(t *testing.T) string {
	t.Helper()

	// Go subtest names carry '.', '(', ')', ':', ',' and capitals, none of
	// which are legal in an RFC 1123 label. Map anything outside [a-z0-9-] to
	// '-', collapse runs, trim, then append a short unique suffix so two names
	// that sanitize (or truncate) to the same string do not collide.
	var b strings.Builder
	for _, r := range strings.ToLower(t.Name()) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	base := b.String()
	for strings.Contains(base, "--") {
		base = strings.ReplaceAll(base, "--", "-")
	}
	base = strings.Trim(base, "-")

	suffix := fmt.Sprintf("%05d", time.Now().UnixNano()%100000)
	if limit := 63 - len(suffix) - 1; len(base) > limit {
		base = strings.Trim(base[:limit], "-")
	}
	ns := base + "-" + suffix

	kubectl(t, "create", "namespace", ns)
	t.Cleanup(func() {
		_, _, _ = run(t, "kubectl", "--context", kubeContext,
			"delete", "namespace", ns, "--ignore-not-found=true", "--wait=false")
	})
	return ns
}

// applyFixture applies a YAML file from test/e2e/fixtures into ns.
func applyFixture(t *testing.T, ns, file string) {
	t.Helper()
	kubectl(t, "apply", "-n", ns, "-f", filepath.Join("fixtures", file))
}

// specOf returns the live .spec of a resource as canonical JSON. Comparing
// this catches any mutation, including ones that do not bump generation.
func specOf(t *testing.T, ns, ref string) string {
	t.Helper()
	return strings.TrimSpace(kubectl(t, "get", ref, "-n", ns, "-o", "jsonpath={.spec}"))
}

// requireUnchanged fails if the live spec differs from the captured baseline.
func requireUnchanged(t *testing.T, ns, ref, before string) {
	t.Helper()
	if after := specOf(t, ns, ref); after != before {
		t.Fatalf("%s was mutated but the gate should have blocked it\nbefore: %s\nafter:  %s", ref, before, after)
	}
}

// waitForNotReady blocks until a deployment reports zero available replicas,
// which is the state fixora's analyzers key off.
func waitForNotReady(t *testing.T, ns, deploy string) {
	t.Helper()
	waitFor(t, 90*time.Second, deploy+" to report unavailable replicas", func() bool {
		out, _, code := run(t, "kubectl", "--context", kubeContext, "get",
			"deployment/"+deploy, "-n", ns, "-o", "jsonpath={.status.availableReplicas}")
		return code == 0 && strings.TrimSpace(out) == ""
	})
}

// applyDeployWithMeta applies the broken deployment with caller-supplied
// labels and annotations, so a test can set exactly one GitOps marker. The
// markers are stamped on both the Deployment metadata and the pod template:
// for a failing workload fixora derives the finding (and its GitOps hints)
// from the owned pod, so template metadata is what the source-managed gate
// actually reads.
func applyDeployWithMeta(t *testing.T, ns, name string, labels, annotations map[string]string) {
	t.Helper()

	podLabels := map[string]string{"app": name}
	for k, v := range labels {
		podLabels[k] = v
	}

	meta := map[string]any{"name": name}
	if len(labels) > 0 {
		meta["labels"] = labels
	}
	if len(annotations) > 0 {
		meta["annotations"] = annotations
	}

	templateMeta := map[string]any{"labels": podLabels}
	if len(annotations) > 0 {
		templateMeta["annotations"] = annotations
	}

	doc := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   meta,
		"spec": map[string]any{
			"replicas": 1,
			"selector": map[string]any{"matchLabels": map[string]string{"app": name}},
			"template": map[string]any{
				"metadata": templateMeta,
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name":  "api",
						"image": "ghcr.io/fixora/does-not-exist:e2e",
					}},
				},
			},
		},
	}

	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal deployment: %v", err)
	}

	cmd := exec.Command("kubectl", "--context", kubeContext, "apply", "-n", ns, "-f", "-")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	cmd.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("apply deployment %s: %v: %s", name, err, stderr.String())
	}
}
