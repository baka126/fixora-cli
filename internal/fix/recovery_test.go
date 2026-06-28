package fix

import (
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

func recoveredImageFinding() analyzer.Finding {
	return analyzer.Finding{
		Namespace:    "prod",
		ResourceKind: "Deployment",
		ResourceName: "api",
		Status:       "ImagePullBackOff",
		Recovered:    true,
	}
}

func TestRecoveredFindingNotApplyEligibleEvenWhenConcrete(t *testing.T) {
	plan := BuildPlan(recoveredImageFinding())
	if !plan.Recovered {
		t.Fatal("expected plan.Recovered=true")
	}
	if len(plan.BlockedReasons) == 0 {
		t.Fatal("expected a blocked reason for a recovered finding")
	}
	// Concretizing with a valid image clears BlockedReasons + sets CanApply, but
	// the recovered gate must still hold.
	plan = Concretize(plan, ConcreteOptions{Container: "api", Image: "ghcr.io/acme/api:v1.2.3"})
	if plan.ApplyEligible {
		t.Fatalf("recovered plan must not be apply-eligible after Concretize: %#v", plan)
	}
	// Fix #1: after Concretize, the recovered diagnostic reason must be present.
	if len(plan.BlockedReasons) == 0 {
		t.Fatal("expected BlockedReasons to be non-empty after Concretize (recovered gate diagnostic)")
	}
	found := false
	for _, r := range plan.BlockedReasons {
		if strings.Contains(r, "recovered") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a recovered-related BlockedReason after Concretize; got: %v", plan.BlockedReasons)
	}
}

func TestRecoveredFindingBlockedThroughValidatedAIPatch(t *testing.T) {
	plan := BuildPlan(recoveredImageFinding())
	// WithValidatedAIPatch sets Safe/CanApply/confidence, clears BlockedReasons —
	// but the recovered gate must survive it.
	validPatch := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    spec:
      containers:
      - name: api
        image: ghcr.io/acme/api:v1.2.3
`
	plan = WithValidatedAIPatch(plan, validPatch, 95)
	if plan.ApplyEligible {
		t.Fatalf("recovered plan must not be apply-eligible even after WithValidatedAIPatch: %#v", plan)
	}
}

func TestRecoveredFindingForceRiskyOptIn(t *testing.T) {
	plan := BuildPlan(recoveredImageFinding())
	plan = Concretize(plan, ConcreteOptions{Container: "api", Image: "ghcr.io/acme/api:v1.2.3", ForceRisky: true})
	if !plan.ApplyEligible {
		t.Fatalf("force-risky must allow applying a recovered plan: %#v", plan)
	}
}

func TestNonRecoveredImageStillApplyEligible(t *testing.T) {
	// Regression: the guard must only affect recovered findings.
	plan := BuildPlan(analyzer.Finding{
		Namespace:    "prod",
		ResourceKind: "Deployment",
		ResourceName: "api",
		Status:       "ImagePullBackOff",
	})
	plan = Concretize(plan, ConcreteOptions{Container: "api", Image: "ghcr.io/acme/api:v1.2.3"})
	if !plan.ApplyEligible {
		t.Fatalf("non-recovered concrete image patch should stay apply-eligible: %#v", plan)
	}
}
