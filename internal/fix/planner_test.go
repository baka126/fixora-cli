package fix

import (
	"strings"
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

func TestExecFormatPodPatchUsesPodShapeAndImageFix(t *testing.T) {
	plan := BuildPlan(analyzer.Finding{
		Namespace:    "default",
		ResourceKind: "Pod",
		ResourceName: "oom-test",
		Status:       "ExecFormatError",
	})
	if plan.Strategy != "fix-architecture" {
		t.Fatalf("expected fix-architecture, got %q", plan.Strategy)
	}
	for _, forbidden := range []string{"apiVersion: apps/v1", "template:", "nodeSelector", "TODO_TARGET_ARCHITECTURE"} {
		if strings.Contains(plan.PatchTemplate, forbidden) {
			t.Fatalf("architecture patch contains forbidden %q:\n%s", forbidden, plan.PatchTemplate)
		}
	}
	for _, want := range []string{"apiVersion: v1", "kind: Pod", "image: TODO_PINNED_MULTI_ARCH_IMAGE"} {
		if !strings.Contains(plan.PatchTemplate, want) {
			t.Fatalf("architecture patch missing %q:\n%s", want, plan.PatchTemplate)
		}
	}
}

func TestWorkloadPatchTemplatesUseControllerPodTemplateShape(t *testing.T) {
	plan := BuildPlan(analyzer.Finding{
		Namespace:    "prod",
		ResourceKind: "Deployment",
		ResourceName: "api",
		Status:       "ImagePullBackOff",
	})
	for _, want := range []string{"apiVersion: apps/v1", "kind: Deployment", "spec:\n  template:\n    spec:\n      containers:"} {
		if !strings.Contains(plan.PatchTemplate, want) {
			t.Fatalf("deployment patch missing %q:\n%s", want, plan.PatchTemplate)
		}
	}
}

// TestDaemonSetTier3StatusesAreReviewOnly: DaemonSetUnderScheduled and DaemonSetFleetHeterogeneous
// must fall through to BuildPlan's default branch and remain not apply-eligible.
func TestDaemonSetTier3StatusesAreReviewOnly(t *testing.T) {
	for _, status := range []string{"DaemonSetUnderScheduled", "DaemonSetFleetHeterogeneous"} {
		plan := BuildPlan(analyzer.Finding{
			Namespace:    "prod",
			ResourceKind: "DaemonSet",
			ResourceName: "agent",
			Status:       status,
		})
		if plan.ApplyEligible {
			t.Fatalf("BuildPlan(%q) must not be apply-eligible (review-only), got %#v", status, plan)
		}
		if plan.Strategy == "" {
			t.Fatalf("BuildPlan(%q) should have a strategy, got empty", status)
		}
	}
}
