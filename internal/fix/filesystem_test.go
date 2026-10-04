package fix

import (
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"strings"
	"testing"
)

func TestEphemeralCachePatchKeepsSecurityControls(t *testing.T) {
	p := BuildPlan(analyzer.Finding{ResourceKind: "Deployment", ResourceName: "worker", Namespace: "demo", Status: "PermissionDenied"})
	got := WithEphemeralCachePatch(p, EphemeralCache{Container: "worker", Directory: "/var/cache", Group: 1000})
	if !got.ApplyEligible || got.Strategy != "filesystem" {
		t.Fatalf("not concrete: %+v", got)
	}
	for _, field := range []string{"emptyDir: {}", "mountPath: /var/cache", "fsGroup: 1000"} {
		if !strings.Contains(got.PatchTemplate, field) {
			t.Fatalf("missing %s: %s", field, got.PatchTemplate)
		}
	}
	for _, field := range []string{"runAsUser", "runAsNonRoot", "readOnlyRootFilesystem", "command:"} {
		if strings.Contains(got.PatchTemplate, field) {
			t.Fatalf("changed existing control %s", field)
		}
	}
}

func TestEphemeralCachePatchRejectsUnsafeInputs(t *testing.T) {
	for _, c := range []EphemeralCache{{Container: "worker", Directory: "/", Group: 1000}, {Container: "worker", Directory: "/etc", Group: 1000}, {Container: "worker", Directory: "/var/cache/../etc", Group: 1000}, {Container: "worker", Directory: "/var/cache", Group: 0}} {
		p := BuildPlan(analyzer.Finding{ResourceKind: "Deployment", ResourceName: "worker", Namespace: "demo", Status: "PermissionDenied"})
		if got := WithEphemeralCachePatch(p, c); got.ApplyEligible {
			t.Fatalf("accepted unsafe input %+v", c)
		}
	}
}

func TestEphemeralCachePatchPreservesDeliveryBlocks(t *testing.T) {
	for _, scenario := range []string{"recovered", "gitops", "wrong-strategy", "pod-target"} {
		p := BuildPlan(analyzer.Finding{ResourceKind: "Deployment", ResourceName: "worker", Namespace: "demo", Status: "PermissionDenied"})
		switch scenario {
		case "recovered":
			p.Recovered = true
		case "gitops":
			p.BlockedReasons = append(p.BlockedReasons, "GitOps-managed workload requires source-level patching.")
		case "wrong-strategy":
			p.Strategy = "env"
		case "pod-target":
			p.Resource = "Pod/worker"
		}
		if got := WithEphemeralCachePatch(p, EphemeralCache{Container: "worker", Directory: "/tmp/cache", Group: 1000}); got.ApplyEligible {
			t.Fatalf("bypassed %s", scenario)
		}
	}
}
