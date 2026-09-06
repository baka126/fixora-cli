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

// TestTier3StatusesDefaultToBuildPlan verifies that the new tier-3 review-only
// statuses (JobRetrying, CronJobOverlap) fall through to BuildPlan's default
// branch and do not accidentally match an existing apply-eligible switch case.
func TestPendingWithExcessiveRequestsUsesResourcesStrategy(t *testing.T) {
	plan := BuildPlan(analyzer.Finding{
		ResourceKind: "Deployment", ResourceName: "pending-demo", Namespace: "ns",
		Status: "Pending",
		Evidence: []analyzer.Evidence{
			{Label: "Requests exceed node allocatable", Value: "requested memory=102400Mi cpu=100000m; largest node allocatable memory=7936Mi cpu=8000m"},
		},
	})
	if plan.Strategy != "resources" {
		t.Fatalf("want resources strategy, got %q", plan.Strategy)
	}
	if !strings.Contains(plan.PatchTemplate, "TODO_OBSERVED_REQUEST") {
		t.Fatalf("want a resources template, got:\n%s", plan.PatchTemplate)
	}
}

func TestPendingWithoutAllocatableEvidenceStaysScheduling(t *testing.T) {
	// Taints, affinity and PVC binding are also Pending causes and none of
	// them are fixed by lowering requests, so they must stay review-only.
	plan := BuildPlan(analyzer.Finding{
		ResourceKind: "Deployment", ResourceName: "pending-demo", Namespace: "ns",
		Status:          "Pending",
		Recommendations: []analyzer.Recommendation{{PatchType: "scheduling"}},
	})
	if plan.Strategy == "resources" {
		t.Fatal("scheduling failures without allocatable evidence must not claim the resources strategy")
	}
	if plan.ApplyEligible {
		t.Fatal("must stay review-only")
	}
}

func TestTier3StatusesDefaultToBuildPlan(t *testing.T) {
	for _, status := range []string{"JobRetrying", "CronJobOverlap"} {
		plan := BuildPlan(analyzer.Finding{Status: status, Namespace: "prod", ResourceKind: "Job", ResourceName: "batch"})
		found := false
		for _, r := range plan.BlockedReasons {
			if r == "No deterministic patch strategy matched this status." {
				found = true
			}
		}
		if !found {
			t.Fatalf("status %q: expected default-branch block reason, got BlockedReasons=%v", status, plan.BlockedReasons)
		}
		if plan.ApplyEligible {
			t.Fatalf("status %q must not be apply eligible from BuildPlan alone; got %#v", status, plan)
		}
	}
}

func TestProbeStrategyProducesProbeTemplate(t *testing.T) {
	plan := BuildPlan(analyzer.Finding{
		ResourceKind: "Deployment", ResourceName: "probe-demo", Namespace: "ns",
		Status: "ProbeFailure",
	})
	if plan.Strategy != "probe" {
		t.Fatalf("want probe, got %q", plan.Strategy)
	}
	if !strings.Contains(plan.PatchTemplate, "TODO_PROBE_PORT") {
		t.Fatalf("want a probe template, got:\n%s", plan.PatchTemplate)
	}
}

func TestProbePlanBecomesApplyEligibleWhenConcretized(t *testing.T) {
	plan := BuildPlan(analyzer.Finding{
		ResourceKind: "Deployment", ResourceName: "probe-demo", Namespace: "ns",
		Status: "ProbeFailure",
	})
	got := Concretize(plan, ConcreteOptions{Container: "web-app", ProbePort: "8080"})
	if !got.ApplyEligible {
		t.Fatalf("concretized probe plan must be apply-eligible; blocked: %v", got.BlockedReasons)
	}
}
