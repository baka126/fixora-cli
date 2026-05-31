package fix

import (
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

func TestConcreteImagePatchBecomesApplyEligible(t *testing.T) {
	plan := BuildPlan(analyzer.Finding{
		Namespace:    "prod",
		ResourceKind: "Deployment",
		ResourceName: "api",
		Status:       "ImagePullBackOff",
	})
	plan = Concretize(plan, ConcreteOptions{Container: "api", Image: "ghcr.io/acme/api:v1.2.3"})
	if !plan.CanApply || !plan.ApplyEligible {
		t.Fatalf("expected concrete image patch apply eligible, got %#v", plan)
	}
	if plan.Confidence < 90 {
		t.Fatalf("expected production confidence gate, got %d", plan.Confidence)
	}
}

func TestAdvisoryPlanDoesNotApply(t *testing.T) {
	plan := BuildPlan(analyzer.Finding{
		Namespace:    "prod",
		ResourceKind: "Deployment",
		ResourceName: "api",
		Status:       "CrashLoopBackOff",
	})
	if plan.ApplyEligible {
		t.Fatalf("expected advisory crashloop plan to be blocked: %#v", plan)
	}
	if len(plan.BlockedReasons) == 0 {
		t.Fatalf("expected blocked reason")
	}
}

func TestServiceSelectorFixStaysReviewOnly(t *testing.T) {
	plan := BuildPlan(analyzer.Finding{
		Namespace:    "prod",
		ResourceKind: "Service",
		ResourceName: "api",
		Status:       "NoEndpoints",
	})
	if plan.Strategy != "repair-selector" {
		t.Fatalf("expected repair-selector strategy, got %q", plan.Strategy)
	}
	if plan.ApplyEligible {
		t.Fatalf("selector repair should require proof and review: %#v", plan)
	}
}
