package infer

import (
	"context"
	"fmt"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

// minTrustScore is the ranking floor below which a discovered image is not
// adopted without human review.
const minTrustScore = 65

func init() { register(imageInferrer{}) }

type imageInferrer struct{}

func (imageInferrer) Name() string { return "image" }

func (imageInferrer) Handles(plan fix.Plan) bool {
	switch strings.ToLower(plan.Strategy) {
	case "image", "fix-architecture":
		return true
	}
	return false
}

func (imageInferrer) Infer(_ context.Context, _ kubeReader, f analyzer.Finding, _ fix.Plan) (Result, bool, error) {
	candidate, score := bestCandidate(f)
	if candidate == "" || score < minTrustScore {
		return Result{}, false, nil
	}
	// A floating tag can drift under the digest the discovery path verified,
	// so only a pinned reference is adopted.
	if !strings.Contains(candidate, "@sha256:") {
		return Result{}, false, nil
	}
	container := ContainerName(f)
	if container == "" {
		return Result{}, false, nil
	}
	return Result{
		Options:   fix.ConcreteOptions{Container: container, Image: candidate},
		Guardrail: "inferred-image-from-catalog",
		Warning:   fmt.Sprintf("Fixora selected ranked public image candidate %s (trust score %d); review the diff and shadow result before delivery.", candidate, score),
	}, true, nil
}

// bestCandidate returns the highest-scoring "Ranked public image candidate"
// evidence attached by inspectCurrentImagePlatforms.
func bestCandidate(f analyzer.Finding) (string, int) {
	best, bestScore := "", 0
	for _, evidence := range f.Evidence {
		var score int
		if _, err := fmt.Sscanf(evidence.Label, "Ranked public image candidate (score %d)", &score); err != nil {
			continue
		}
		reference := strings.TrimSpace(strings.SplitN(evidence.Value, "|", 2)[0])
		if reference != "" && score > bestScore {
			best, bestScore = reference, score
		}
	}
	return best, bestScore
}
