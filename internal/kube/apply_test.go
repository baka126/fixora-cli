package kube

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeKubectl puts a recording stub named `kubectl` at the front of PATH and
// returns the path of the file it writes its arguments to. This exercises the
// real Run/Apply code path — including argument construction — without needing
// a cluster.
func fakeKubectl(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	argsFile := filepath.Join(binDir, "args.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	return argsFile
}

func recordedArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("stub kubectl recorded no arguments: %v", err)
	}
	return strings.Fields(strings.TrimSpace(string(data)))
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestApplyUsesServerSideApply pins the fix for the defect the e2e suite caught
// on its first run.
//
// Fixora generates a PARTIAL manifest (only the fields it intends to change —
// see fix.workloadPatchTemplate, which omits spec.selector and
// spec.template.metadata.labels). Client-side `kubectl apply` three-way-merges
// that against last-applied-configuration and computes DELETIONS for every
// field present in last-applied but absent from the file. Against any workload
// created with `kubectl apply` — the common case — the API server then rejects
// the result with "spec.selector: Invalid value: null: field is immutable".
//
// Server-side apply merges only the fields present and never deletes the rest,
// which is what a partial patch means. Dropping either flag reintroduces the bug.
func TestApplyUsesServerSideApply(t *testing.T) {
	argsFile := fakeKubectl(t)

	if err := NewKubectl("").Apply(context.Background(), "/tmp/fixora-patch.yaml"); err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}

	args := recordedArgs(t, argsFile)
	if !hasArg(args, "--server-side") {
		t.Fatalf("Apply must use server-side apply; a client-side apply deletes fields "+
			"absent from fixora's partial patch. args=%v", args)
	}
	if !hasArg(args, "--force-conflicts") {
		t.Fatalf("Apply must pass --force-conflicts; fields on a kubectl-created workload "+
			"are owned by the kubectl-client-side-apply manager and would otherwise conflict. args=%v", args)
	}
}

// TestDryRunApplyUsesServerSideApply pins the same fix on the dry-run path.
// The dry-run is the preflight gate for both `fix --delivery cluster` and every
// step of the coordinate saga, so if it keeps client-side semantics it rejects
// valid patches and aborts the whole coordinated set.
func TestDryRunApplyUsesServerSideApply(t *testing.T) {
	argsFile := fakeKubectl(t)

	if err := NewKubectl("").DryRunApply(context.Background(), "/tmp/fixora-patch.yaml"); err != nil {
		t.Fatalf("DryRunApply returned an error: %v", err)
	}

	args := recordedArgs(t, argsFile)
	if !hasArg(args, "--server-side") {
		t.Fatalf("DryRunApply must use server-side apply so the preflight matches the "+
			"real apply; otherwise it rejects patches that would actually succeed. args=%v", args)
	}
	if !hasArg(args, "--force-conflicts") {
		t.Fatalf("DryRunApply must pass --force-conflicts to match Apply, or the preflight "+
			"disagrees with the mutation it is gating. args=%v", args)
	}
	if !hasArg(args, "--dry-run=server") {
		t.Fatalf("DryRunApply must stay a server dry-run. args=%v", args)
	}
}
