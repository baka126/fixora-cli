//go:build e2e

package e2e

import "testing"

// TestPlanViewParses is a pure unit check that planView captures the fix.Plan
// JSON keys the scenario suite asserts on. It does not touch a cluster.
func TestPlanViewParses(t *testing.T) {
	const sample = `{"resource":"Deployment/x","status":"ImagePullBackOff","strategy":"image","applyEligible":true,"patchTemplate":"spec:\n  template: {}\n"}`
	pv := parsePlanView(t, []byte(sample))
	if pv.Strategy != "image" {
		t.Fatalf("strategy: got %q", pv.Strategy)
	}
	if !pv.ApplyEligible {
		t.Fatal("applyEligible should be true")
	}
	if pv.Status != "ImagePullBackOff" {
		t.Fatalf("status: got %q", pv.Status)
	}
	if pv.PatchTemplate == "" {
		t.Fatal("patchTemplate should be captured")
	}
}
