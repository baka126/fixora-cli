package ops

import (
	"context"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
)

func TestPreflightRejectsNewHelmOwnership(t *testing.T) {
	obj, _ := kube.WorkloadPatch("Deployment", "app", "ns", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "old:v1"}}})
	meta := obj["metadata"].(map[string]any)
	meta["uid"] = "u"
	meta["resourceVersion"] = "2"
	target := repairpolicy.Target{Kind: "Deployment", Name: "app", Namespace: "ns", UID: "u"}
	proof, err := repairpolicy.BindCommon(repairpolicy.Snapshot{Target: target, Object: obj}, "candidate", "image", func(string, string, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	meta["labels"] = map[string]any{"app.kubernetes.io/managed-by": "Helm"}
	meta["annotations"] = map[string]any{"meta.helm.sh/release-name": "new-release"}
	if err = proof.Check(repairpolicy.Snapshot{Target: target, Object: obj}, "candidate"); err != nil {
		t.Fatal(err)
	}
	_, err = PreflightControllerSnapshot(context.Background(), controllerReader{object: obj}, analyzer.Finding{ResourceKind: "Deployment", ResourceName: "app", Namespace: "ns"}, fix.Plan{Resource: "Deployment/app", Namespace: "ns", Strategy: "image", CanApply: true, ApplyEligible: true, TargetUID: "u"})
	if err == nil {
		t.Fatal("fresh preflight accepted new Helm ownership and resourceVersion")
	}
}
