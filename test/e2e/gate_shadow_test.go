//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

// TestShadowCleansUpOnFailure is the load-bearing case. A failed shadow run
// that leaks a pod plus an ingress-deny NetworkPolicy into a production
// namespace is the most damaging outcome this tool can produce.
func TestShadowCleansUpOnFailure(t *testing.T) {
	t.Parallel()
	ns := newNamespace(t)
	applyDeployWithMeta(t, ns, "shadow-target", nil, nil)
	waitForNotReady(t, ns, "shadow-target")

	before := specOf(t, ns, "deployment/shadow-target")
	c := typedClient(t)

	// This patch pins an image that will never pull, so the clone never
	// becomes ready and Run takes the failure path.
	patch := "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        image: ghcr.io/fixora/still-does-not-exist:e2e\n"

	finding := analyzer.Finding{
		Namespace:    ns,
		ResourceKind: "Deployment",
		ResourceName: "shadow-target",
		Status:       "ImagePullBackOff",
	}
	plan := fix.BuildPlan(finding)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	res, _ := shadow.Run(ctx, c, shadow.Request{
		Namespace: ns,
		Resource:  "deployment/shadow-target",
		Patch:     patch,
		Finding:   finding,
		Plan:      plan,
		Timeout:   60 * time.Second, // the 10m default would dominate the suite
		Retries:   0,
		Redact:    true,
	})

	if res.Verified {
		t.Fatal("a never-ready clone must not verify")
	}
	// The failure path must still clean up.
	if res.CloneName != "" {
		requireGone(t, ns, "pod", res.CloneName)
	}
	if res.NetworkPolicyName != "" {
		requireGone(t, ns, "networkpolicy", res.NetworkPolicyName)
	}
	requireUnchanged(t, ns, "deployment/shadow-target", before)
}

// TestShadowLeavesProductionUntouched asserts the sandbox never touches the
// real workload, whatever the verification outcome.
func TestShadowLeavesProductionUntouched(t *testing.T) {
	t.Parallel()
	ns := newNamespace(t)
	applyDeployWithMeta(t, ns, "shadow-prod", nil, nil)
	waitForNotReady(t, ns, "shadow-prod")

	before := specOf(t, ns, "deployment/shadow-prod")
	c := typedClient(t)

	finding := analyzer.Finding{
		Namespace:    ns,
		ResourceKind: "Deployment",
		ResourceName: "shadow-prod",
		Status:       "ImagePullBackOff",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	_, _ = shadow.Run(ctx, c, shadow.Request{
		Namespace: ns,
		Resource:  "deployment/shadow-prod",
		Patch:     "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        image: public.ecr.aws/docker/library/busybox:1.36\n",
		Finding:   finding,
		Plan:      fix.BuildPlan(finding),
		Timeout:   60 * time.Second,
		Redact:    true,
	})

	requireUnchanged(t, ns, "deployment/shadow-prod", before)
}
