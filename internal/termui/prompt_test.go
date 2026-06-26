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
