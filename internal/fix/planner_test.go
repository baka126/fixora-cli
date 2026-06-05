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

func TestUnknownStrategyStaysReviewOnlyAfterConcreteValues(t *testing.T) {
	plan := BuildPlan(analyzer.Finding{
		Namespace:    "prod",
		ResourceKind: "Deployment",
		ResourceName: "api",
		Status:       "ImagePullBackOff",
	})
	plan = Concretize(plan, ConcreteOptions{Container: "api", Image: "ghcr.io/acme/api:v1.2.3", Strategy: "custom-risky"})
	if plan.CanApply || plan.ApplyEligible {
		t.Fatalf("unknown strategy must not become apply eligible: %#v", plan)
	}
}

func TestWebhookStrategyStaysReviewOnly(t *testing.T) {
	plan := BuildPlan(analyzer.Finding{
		Namespace:    "prod",
		ResourceKind: "ValidatingWebhookConfiguration",
		ResourceName: "policy",
		Status:       "WebhookTimeout",
	})
	plan.PatchTemplate = "failurePolicy: Ignore\n"
	plan = Concretize(plan, ConcreteOptions{})
	if plan.CanApply || plan.ApplyEligible {
		t.Fatalf("webhook strategy must remain review-only: %#v", plan)
	}
}

func TestResourcePatchRequiresResourceFields(t *testing.T) {
	plan := Plan{Strategy: "resources", PatchTemplate: "kind: Deployment\nmetadata:\n  name: api\n"}
	plan = Concretize(plan, ConcreteOptions{})
	if plan.CanApply || plan.ApplyEligible {
		t.Fatalf("resource strategy without resource evidence must be blocked: %#v", plan)
	}
}
