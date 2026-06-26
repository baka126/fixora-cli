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

func TestWalkthroughPromptsEOFDefaults(t *testing.T) {
	if got := PromptDelivery(strings.NewReader(""), &bytes.Buffer{}); got != DeliverCancel {
		t.Fatalf("EOF: PromptDelivery got %v, want DeliverCancel", got)
	}
	if got := PromptStep("x", strings.NewReader(""), &bytes.Buffer{}); got != StepQuit {
		t.Fatalf("EOF: PromptStep got %v, want StepQuit", got)
	}
	if p, e := ConfirmShadowOrEdit(strings.NewReader(""), &bytes.Buffer{}); p || e {
		t.Fatalf("EOF: ConfirmShadowOrEdit got proceed=%v edit=%v, want false,false", p, e)
	}
}
