package termui

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestConfirmEditPatch(t *testing.T) {
	var out bytes.Buffer
	if !ConfirmEditPatch("fixora-patch.yaml", strings.NewReader("yes\n"), &out) {
		t.Fatal("expected yes to allow editing")
	}
	if !strings.Contains(out.String(), "fixora-patch.yaml") {
		t.Fatalf("prompt should show patch path, got %q", out.String())
	}
	out.Reset()
	if ConfirmEditPatch("fixora-patch.yaml", strings.NewReader("\n"), &out) {
		t.Fatal("default response should skip editing")
	}
}

func TestSequentialPromptsShareBufferedInput(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("n\ny\n"))
	var out bytes.Buffer
	if ConfirmEditPatch("fixora-patch.yaml", input, &out) {
		t.Fatal("expected first response to skip editor")
	}
	if !ConfirmShadowDeploy("", input, &out) {
		t.Fatal("expected second response to approve shadow deployment")
	}
}

func TestConfirmRollback(t *testing.T) {
	if !ConfirmRollback("kubectl rollout undo deployment/api -n prod", strings.NewReader("y\n"), &bytes.Buffer{}) {
		t.Fatal("y must confirm")
	}
	if ConfirmRollback("cmd", strings.NewReader("\n"), &bytes.Buffer{}) {
		t.Fatal("default must be No")
	}
	if ConfirmRollback("cmd", strings.NewReader(""), &bytes.Buffer{}) {
		t.Fatal("EOF must be No")
	}
	var out bytes.Buffer
	ConfirmRollback("kubectl rollout undo deployment/api -n prod", strings.NewReader("n\n"), &out)
	if !strings.Contains(out.String(), "kubectl rollout undo deployment/api") {
		t.Fatalf("prompt must show the rollback command, got %q", out.String())
	}
}
