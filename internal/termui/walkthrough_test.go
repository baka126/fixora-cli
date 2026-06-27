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
		got := PromptDelivery(strings.NewReader(in), &out, false)
		if got != want {
			t.Fatalf("input %q: got %v want %v", in, got, want)
		}
	}
}

func TestPromptDeliveryReviewOnly(t *testing.T) {
	// Review-only must not offer or imply direct cluster apply: "1" is PR,
	// "2"/Enter is patch-file, and the copy must not claim "safe to ship".
	cases := map[string]DeliveryChoice{
		"1\n": DeliverPR, "2\n": DeliverPatch, "\n": DeliverPatch, "x\n": DeliverCancel,
	}
	for in, want := range cases {
		var out bytes.Buffer
		got := PromptDelivery(strings.NewReader(in), &out, true)
		if got != want {
			t.Fatalf("review-only input %q: got %v want %v", in, got, want)
		}
		if strings.Contains(out.String(), "directly to the cluster") || strings.Contains(out.String(), "safe to ship") {
			t.Fatalf("review-only prompt must not offer cluster apply, got %q", out.String())
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

func TestConfirmProceedOrEdit(t *testing.T) {
	var out bytes.Buffer
	if p, e := ConfirmProceedOrEdit(strings.NewReader("e\n"), &out); !p || !e {
		t.Fatalf("e: got proceed=%v edit=%v", p, e)
	}
	if p, e := ConfirmProceedOrEdit(strings.NewReader("\n"), &out); !p || e {
		t.Fatalf("enter: got proceed=%v edit=%v", p, e)
	}
	if p, _ := ConfirmProceedOrEdit(strings.NewReader("n\n"), &out); p {
		t.Fatalf("n: expected proceed=false")
	}
	if p, e := ConfirmProceedOrEdit(strings.NewReader(""), &out); p || e {
		t.Fatalf("EOF: got proceed=%v edit=%v, want false,false", p, e)
	}
	if !strings.Contains(out.String(), "Proceed with this fix?") {
		t.Fatalf("expected generic proceed prompt, got %q", out.String())
	}
	if strings.Contains(out.String(), "shadow") {
		t.Fatalf("proceed prompt must not mention shadow, got %q", out.String())
	}
}

func TestWalkthroughPromptsEOFDefaults(t *testing.T) {
	if got := PromptDelivery(strings.NewReader(""), &bytes.Buffer{}, false); got != DeliverCancel {
		t.Fatalf("EOF: PromptDelivery got %v, want DeliverCancel", got)
	}
	if got := PromptStep("x", strings.NewReader(""), &bytes.Buffer{}); got != StepQuit {
		t.Fatalf("EOF: PromptStep got %v, want StepQuit", got)
	}
	if p, e := ConfirmShadowOrEdit(strings.NewReader(""), &bytes.Buffer{}); p || e {
		t.Fatalf("EOF: ConfirmShadowOrEdit got proceed=%v edit=%v, want false,false", p, e)
	}
}
