//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"os/exec"
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

// fixora runs the binary under test against the kind cluster.
func fixora(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	full := append([]string{"--context", kubeContext}, args...)
	return run(t, binPath, full...)
}

// kubectl runs kubectl against the kind cluster and fails the test on error.
func kubectl(t *testing.T, args ...string) string {
	t.Helper()
	full := append([]string{"--context", kubeContext}, args...)
	stdout, stderr, code := run(t, "kubectl", full...)
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
	ns := strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	if len(ns) > 60 {
		ns = ns[:60]
	}
	ns = strings.Trim(ns, "-")
	kubectl(t, "create", "namespace", ns)
	t.Cleanup(func() {
		_, _, _ = run(t, "kubectl", "--context", kubeContext,
			"delete", "namespace", ns, "--ignore-not-found=true", "--wait=false")
	})
	return ns
}
