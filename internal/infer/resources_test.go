package infer

import (
	"context"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

const topOutput = `POD                             NAME         CPU(cores)   MEMORY(bytes)
oomkilled-demo-569b44bd6d-z5dmw   memory-hog   2m           118Mi
oomkilled-demo-569b44bd6d-z5dmw   sidecar      1m           12Mi`

// topOutputBusyCPU samples memory-hog well above the 10m CPU floor, so the
// inferrer must pass the observed millicores through unchanged.
const topOutputBusyCPU = `POD                             NAME         CPU(cores)   MEMORY(bytes)
oomkilled-demo-569b44bd6d-z5dmw   memory-hog   250m         118Mi`

func TestParseTopMemoryFindsContainer(t *testing.T) {
	got, ok := parseTopMemory(topOutput, "memory-hog")
	if !ok || got != 118 {
		t.Fatalf("want 118Mi, got %d ok=%v", got, ok)
	}
}

func TestParseTopMemoryMissingContainerDeclines(t *testing.T) {
	if _, ok := parseTopMemory(topOutput, "absent"); ok {
		t.Fatal("missing container row must decline")
	}
}

func TestParseTopMemoryZeroDeclines(t *testing.T) {
	// A container OOM-killed at startup may never report a sample. Inventing
	// a number here would produce a patch that cannot work.
	if _, ok := parseTopMemory("POD NAME CPU MEM\np app 0m 0Mi", "app"); ok {
		t.Fatal("zero observed usage must decline")
	}
}

func TestRoundUpMi(t *testing.T) {
	for _, tc := range []struct{ in, want int64 }{{1, 16}, {16, 16}, {17, 32}, {118, 128}} {
		if got := roundUpMi(tc.in); got != tc.want {
			t.Fatalf("roundUpMi(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestResourcesInferrerFromMetrics(t *testing.T) {
	f := analyzer.Finding{
		Status: "OOMKilled",
		Evidence: []analyzer.Evidence{
			{Label: "Container image memory-hog", Value: "busybox:1.36"},
			{Label: "Metrics pod containers", Value: topOutput},
		},
	}
	res, ok, err := resourcesInferrer{}.Infer(context.Background(), nil, f, fix.Plan{Strategy: "resources", Status: "OOMKilled"})
	if err != nil || !ok {
		t.Fatalf("want accept, got ok=%v err=%v", ok, err)
	}
	if res.Options.MemoryRequest != "128Mi" {
		t.Fatalf("request: want 128Mi, got %q", res.Options.MemoryRequest)
	}
	// 118 * 1.5 = 177 -> rounds up to 192Mi
	if res.Options.MemoryLimit != "192Mi" {
		t.Fatalf("limit: want 192Mi, got %q", res.Options.MemoryLimit)
	}
	if res.Options.Container != "memory-hog" {
		t.Fatalf("container: got %q", res.Options.Container)
	}
	// memory-hog is sampled at 2m, below the 10m floor.
	if res.Options.CPURequest != "10m" {
		t.Fatalf("cpu request: want 10m, got %q", res.Options.CPURequest)
	}
}

func TestResourcesInferrerFloorsSubFloorCPU(t *testing.T) {
	// A 2m sample is below the 10m floor. Unlike a zero memory sample it is not
	// a decline signal — a low CPU reading is ordinary — so it is floored.
	f := analyzer.Finding{
		Status: "OOMKilled",
		Evidence: []analyzer.Evidence{
			{Label: "Container image memory-hog", Value: "busybox:1.36"},
			{Label: "Metrics pod containers", Value: topOutput},
		},
	}
	res, ok, err := resourcesInferrer{}.Infer(context.Background(), nil, f, fix.Plan{Strategy: "resources", Status: "OOMKilled"})
	if err != nil || !ok {
		t.Fatalf("want accept, got ok=%v err=%v", ok, err)
	}
	if res.Options.CPURequest != "10m" {
		t.Fatalf("cpu request: want 10m, got %q", res.Options.CPURequest)
	}
}

func TestResourcesInferrerPassesThroughObservedCPU(t *testing.T) {
	f := analyzer.Finding{
		Status: "OOMKilled",
		Evidence: []analyzer.Evidence{
			{Label: "Container image memory-hog", Value: "busybox:1.36"},
			{Label: "Metrics pod containers", Value: topOutputBusyCPU},
		},
	}
	res, ok, err := resourcesInferrer{}.Infer(context.Background(), nil, f, fix.Plan{Strategy: "resources", Status: "OOMKilled"})
	if err != nil || !ok {
		t.Fatalf("want accept, got ok=%v err=%v", ok, err)
	}
	if res.Options.CPURequest != "250m" {
		t.Fatalf("cpu request: want 250m, got %q", res.Options.CPURequest)
	}
}

// TestResourcesInferrerConcretePlanIsApplyEligible feeds a real BuildPlan
// finding through infer.Concrete and asserts the concretized plan comes back
// ApplyEligible. Without a CPU request the resources template keeps
// TODO_OBSERVED_CPU_REQUEST, Concretize never runs its validation block, and
// the inferrer's result is silently discarded — this is the assertion that
// catches that.
func TestResourcesInferrerConcretePlanIsApplyEligible(t *testing.T) {
	f := analyzer.Finding{
		ResourceKind: "Deployment", ResourceName: "oomkilled-demo", Namespace: "default",
		Status: "OOMKilled",
		Evidence: []analyzer.Evidence{
			{Label: "Container image memory-hog", Value: "busybox:1.36"},
			{Label: "Metrics pod containers", Value: topOutput},
		},
	}
	plan := fix.BuildPlan(f)
	got, ok := Concrete(context.Background(), nil, f, plan)
	if !ok {
		t.Fatalf("want a concretized plan, got ok=false (blocked: %v)", got.BlockedReasons)
	}
	if !got.ApplyEligible {
		t.Fatalf("want ApplyEligible, got false (blocked: %v)", got.BlockedReasons)
	}
	if strings.Contains(got.PatchTemplate, "TODO_") {
		t.Fatalf("patch still has an unsubstituted placeholder:\n%s", got.PatchTemplate)
	}
}

func TestResourcesInferrerDeclinesWithoutMetrics(t *testing.T) {
	f := analyzer.Finding{Status: "OOMKilled", Evidence: []analyzer.Evidence{
		{Label: "Container image memory-hog", Value: "busybox:1.36"},
	}}
	if _, ok, _ := (resourcesInferrer{}).Infer(context.Background(), nil, f, fix.Plan{Strategy: "resources", Status: "OOMKilled"}); ok {
		t.Fatal("no metrics evidence must decline")
	}
}
