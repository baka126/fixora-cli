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

	if err := NewKubectl("").Apply(context.Background(), syntheticPatch(t, "kind: Deployment\nmetadata: {name: worker}\n")); err != nil {
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

	if err := NewKubectl("").DryRunApply(context.Background(), syntheticPatch(t, "kind: Deployment\nmetadata: {name: worker}\n")); err != nil {
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

func syntheticPatch(t *testing.T, contents string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "patch.yaml")
	if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}
func applyManager(args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, "--field-manager=") {
			return strings.TrimPrefix(arg, "--field-manager=")
		}
	}
	return ""
}
func TestApplyProposalManagerStableAcrossDryRunAndApply(t *testing.T) {
	argsFile := fakeKubectl(t)
	file := syntheticPatch(t, "kind: Deployment\nmetadata: {name: worker}\n")
	k := NewKubectl("")
	if err := k.DryRunApply(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	dry := applyManager(recordedArgs(t, argsFile))
	if err := k.Apply(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	actual := applyManager(recordedArgs(t, argsFile))
	if !strings.HasPrefix(dry, "fixora-") || len(dry) != 23 || dry != actual {
		t.Fatalf("dryrun manager=%q apply=%q", dry, actual)
	}
	secondPath := syntheticPatch(t, "kind: Deployment\nmetadata: {name: worker}\n")
	if err := k.Apply(context.Background(), secondPath); err != nil {
		t.Fatal(err)
	}
	if again := applyManager(recordedArgs(t, argsFile)); again != actual {
		t.Fatalf("same proposal with different filename changed manager %q -> %q", actual, again)
	}
}
func TestApplyDifferentProposalUsesDifferentManager(t *testing.T) {
	argsFile := fakeKubectl(t)
	k := NewKubectl("")
	first := syntheticPatch(t, "spec: {template: {spec: {containers: [{name: app, image: busybox:1.36}]}}}\n")
	second := syntheticPatch(t, "spec: {template: {spec: {containers: [{name: app, args: [httpd]}]}}}\n")
	if err := k.Apply(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	firstManager := applyManager(recordedArgs(t, argsFile))
	if err := k.Apply(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	secondManager := applyManager(recordedArgs(t, argsFile))
	if firstManager == "" || firstManager == secondManager {
		t.Fatalf("different proposals shared manager %q", firstManager)
	}
	if err := os.WriteFile(second, []byte("spec: {template: {spec: {containers: [{name: app, args: [httpd, -f]}]}}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := k.Apply(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if changed := applyManager(recordedArgs(t, argsFile)); changed == secondManager {
		t.Fatalf("updated proposal bytes at the same filename retained manager %q", changed)
	}
}
func TestApplyMissingProposalFailsBeforeKubectl(t *testing.T) {
	for _, dry := range []bool{false, true} {
		argsFile := fakeKubectl(t)
		file := filepath.Join(t.TempDir(), "missing.yaml")
		k := NewKubectl("")
		var err error
		if dry {
			err = k.DryRunApply(context.Background(), file)
		} else {
			err = k.Apply(context.Background(), file)
		}
		if err == nil {
			t.Fatal("missing proposal should fail")
		}
		if _, statErr := os.Stat(argsFile); !os.IsNotExist(statErr) {
			t.Fatalf("kubectl ran before the proposal could be read: %v", statErr)
		}
	}
}
