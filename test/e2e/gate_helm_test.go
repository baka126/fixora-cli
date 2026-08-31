//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestHelmGateRefusesDirectApply drives every marker that makes
// analyzer.gitOpsHints report source management. TargetAdvice is deliberately
// absent: it is derived from HelmRelease/ManagedBy and cannot be set directly.
func TestHelmGateRefusesDirectApply(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		labels      map[string]string
		annotations map[string]string
	}{
		{
			name:   "managed-by-label",
			labels: map[string]string{"app.kubernetes.io/managed-by": "Helm"},
		},
		{
			name:        "helm-release-annotation",
			annotations: map[string]string{"meta.helm.sh/release-name": "prod-api"},
		},
		{
			name:   "instance-label",
			labels: map[string]string{"app.kubernetes.io/instance": "prod-api"},
		},
		{
			name:   "helm-chart-label",
			labels: map[string]string{"helm.sh/chart": "api-1.2.3"},
		},
		{
			name:        "argocd-annotation",
			annotations: map[string]string{"argocd.argoproj.io/tracking-id": "api:apps/Deployment:prod/api"},
		},
		{
			name:        "flux-annotation",
			annotations: map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ns := newNamespace(t)
			applyDeployWithMeta(t, ns, "broken-api", tc.labels, tc.annotations)
			waitForImagePullFailure(t, ns, "broken-api")

			before := specOf(t, ns, "deployment/broken-api")

			// A concrete image is supplied so the plan IS apply-eligible.
			// That isolates the source-managed gate: if this applies, the
			// Helm block — not the eligibility gate — is what failed.
			stdout, stderr, code := fixora(t, "fix", "deployment/broken-api",
				"-n", ns, "--quick", "--no-ai", "--delivery", "cluster", "--yes",
				"--out", patchOut(t),
				"--container", "api", "--image", readyImage)

			if code == 0 {
				t.Fatalf("source-managed workload must not apply directly; stderr=%s", stderr)
			}
			requireUnchanged(t, ns, "deployment/broken-api", before)

			combined := strings.ToLower(stdout + stderr)
			if !strings.Contains(combined, "helm") && !strings.Contains(combined, "source") &&
				!strings.Contains(combined, "repo") && !strings.Contains(combined, "gitops") {
				t.Fatalf("refusal should tell the operator to use source delivery, got:\n%s", combined)
			}
		})
	}
}

// TestHelmGateAllowsUnmanagedWorkload is the control. A byte-identical
// deployment with no source markers must apply, proving the refusals above
// are attributable to the markers and not to incidental failure.
func TestHelmGateAllowsUnmanagedWorkload(t *testing.T) {
	t.Parallel()
	ns := newNamespace(t)
	applyDeployWithMeta(t, ns, "broken-api", nil, nil)
	waitForImagePullFailure(t, ns, "broken-api")

	before := specOf(t, ns, "deployment/broken-api")

	_, stderr, code := fixora(t, "fix", "deployment/broken-api",
		"-n", ns, "--quick", "--no-ai", "--delivery", "cluster", "--yes",
		"--out", patchOut(t),
		"--container", "api", "--image", readyImage)

	if code != 0 {
		t.Fatalf("unmarked workload should apply; exit=%d stderr=%s", code, stderr)
	}
	if specOf(t, ns, "deployment/broken-api") == before {
		t.Fatal("unmarked workload was not mutated; the control case proves nothing")
	}
}
