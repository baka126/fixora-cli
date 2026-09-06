package infer

import (
	"context"
	"strconv"
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
