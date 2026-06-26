package termui

import (
	"bytes"
	"strings"
	"testing"
)

func TestPromptStep(t *testing.T) {
	cases := map[string]StepChoice{"\n": StepContinue, "w\n": StepShowProof, "q\n": StepQuit}
	for in, want := range cases {
		var out bytes.Buffer
		got := PromptStep("continue?", strings.NewReader(in), &out)
		if got != want {
			t.Fatalf("input %q: got %v want %v", in, got, want)
		}
	}
}

func TestPromptDelivery(t *testing.T) {
	cases := map[string]DeliveryChoice{
		"1\n": DeliverCluster, "2\n": DeliverPR, "3\n": DeliverPatch,
		"\n": DeliverPatch, "x\n": DeliverCancel,
	}
	for in, want := range cases {
		var out bytes.Buffer
		got := PromptDelivery(strings.NewReader(in), &out)
		if got != want {
			t.Fatalf("input %q: got %v want %v", in, got, want)
		}
	}
}

func TestConfirmShadowOrEdit(t *testing.T) {
	var out bytes.Buffer
	if p, e := ConfirmShadowOrEdit(strings.NewReader("e\n"), &out); !p || !e {
		t.Fatalf("e: got proceed=%v edit=%v", p, e)
	}
	if p, e := ConfirmShadowOrEdit(strings.NewReader("\n"), &out); !p || e {
		t.Fatalf("enter: got proceed=%v edit=%v", p, e)
	}
	if p, _ := ConfirmShadowOrEdit(strings.NewReader("n\n"), &out); p {
		t.Fatalf("n: expected proceed=false")
	}
}
