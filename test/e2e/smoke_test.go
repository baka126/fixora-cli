//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestSmokeBinaryRunsAgainstCluster proves the whole harness pipeline works:
// the binary was built, the kind cluster exists, and KUBECONFIG points at it.
func TestSmokeBinaryRunsAgainstCluster(t *testing.T) {
	stdout, stderr, code := fixora(t, "version")
	if code != 0 {
		t.Fatalf("version exited %d: stderr=%s", code, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("version printed nothing on stdout")
	}

	nodes := kubectl(t, "get", "nodes", "-o", "name")
	if !strings.Contains(nodes, "node/") {
		t.Fatalf("expected at least one kind node, got %q", nodes)
	}
}
