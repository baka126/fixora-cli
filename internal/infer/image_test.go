package infer

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

func imageFinding(candidate string, score int) analyzer.Finding {
	return analyzer.Finding{Evidence: []analyzer.Evidence{
		{Label: "Container image typo-container", Value: "nginx:1.25-typo"},
		{Label: "Ranked public image candidate (score " + strconv.Itoa(score) + ")", Value: candidate + " | official image"},
	}}
}

func TestImageInferrerAcceptsPinnedCandidate(t *testing.T) {
	f := imageFinding("docker.io/library/nginx@sha256:abc", 80)
	res, ok, err := imageInferrer{}.Infer(context.Background(), nil, f, fix.Plan{Strategy: "image"})
	if err != nil || !ok {
		t.Fatalf("want accept, got ok=%v err=%v", ok, err)
	}
	if res.Options.Image != "docker.io/library/nginx@sha256:abc" {
		t.Fatalf("unexpected image %q", res.Options.Image)
	}
	if res.Options.Container != "typo-container" {
		t.Fatalf("unexpected container %q", res.Options.Container)
	}
}

func TestImageInferrerRejectsFloatingTag(t *testing.T) {
	// A floating tag can drift under the verified digest, so only a pinned
	// reference may be adopted without human review.
	f := imageFinding("docker.io/library/nginx:1.25", 90)
	if _, ok, _ := (imageInferrer{}).Infer(context.Background(), nil, f, fix.Plan{Strategy: "image"}); ok {
		t.Fatal("floating tag must be declined")
	}
}

func TestImageInferrerRejectsLowScore(t *testing.T) {
	f := imageFinding("docker.io/library/nginx@sha256:abc", 64)
	if _, ok, _ := (imageInferrer{}).Infer(context.Background(), nil, f, fix.Plan{Strategy: "image"}); ok {
		t.Fatal("score below 65 must be declined")
	}
}

func TestImageInferrerRejectsMissingContainerName(t *testing.T) {
	f := analyzer.Finding{Evidence: []analyzer.Evidence{
		{Label: "Ranked public image candidate (score 90)", Value: "nginx@sha256:abc | official"},
	}}
	if _, ok, _ := (imageInferrer{}).Infer(context.Background(), nil, f, fix.Plan{Strategy: "image"}); ok {
		t.Fatal("missing container name must be declined")
	}
}

func TestImageInferrerHandlesBothStrategies(t *testing.T) {
	in := imageInferrer{}
	if !in.Handles(fix.Plan{Strategy: "image"}) || !in.Handles(fix.Plan{Strategy: "fix-architecture"}) {
		t.Fatal("must handle image and fix-architecture")
	}
	if in.Handles(fix.Plan{Strategy: "resources"}) {
		t.Fatal("must not handle resources")
	}
}

// TestImageInferrerConcretePlanIsApplyEligible builds a real ImagePullBackOff
// finding carrying a ranked pinned candidate, runs it through fix.BuildPlan then
// infer.Concrete, and asserts the concretized plan comes back ApplyEligible with
// no residual TODO_. The image inferrer shipped inert earlier in this plan; the
// other four inference paths have this net and it was the one missing it.
func TestImageInferrerConcretePlanIsApplyEligible(t *testing.T) {
	f := analyzer.Finding{
		ResourceKind: "Deployment", ResourceName: "imagepull-demo", Namespace: "default",
		Status: "ImagePullBackOff",
		Evidence: []analyzer.Evidence{
			{Label: "Container image typo-container", Value: "nginx:1.25-typo"},
			{Label: "Ranked public image candidate (score 80)", Value: "docker.io/library/nginx@sha256:abc | Docker Official Image"},
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

// Moved from internal/cli TestBestTrustedImageCandidatePrefersHigherScore when
// the CLI's applyTrustedImageCandidate path was folded into this inferrer.
func TestBestCandidatePrefersHigherScore(t *testing.T) {
	f := analyzer.Finding{Evidence: []analyzer.Evidence{
		{Label: "Ranked public image candidate (score 45)", Value: "example/low | public"},
		{Label: "Ranked public image candidate (score 65)", Value: "example/high | public"},
	}}
	ref, score := bestCandidate(f)
	if ref != "example/high" || score != 65 {
		t.Fatalf("unexpected candidate %q score=%d", ref, score)
	}
}
