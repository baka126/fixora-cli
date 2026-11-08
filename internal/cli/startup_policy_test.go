package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

func TestStartupSavedPlanRequiresFreshProof(t *testing.T) {
	path, r, f, p := startupFixture(t, validStartupBaseline)
	p, err := withStartupBaseline(context.Background(), r, path, f, p)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	var saved fix.Plan
	_ = json.Unmarshal(raw, &saved)
	if saved.PolicyProof != nil || saved.PolicyAuthority != nil {
		t.Fatal("saved plan retained trusted state")
	}
	var out, errs bytes.Buffer
	if code := deliverVerifiedFix(context.Background(), &out, &errs, options{yes: true}, kube.Kubectl{}, f, saved, shadow.Result{Verified: true}, shadow.DeliveryCluster, true); code == 0 {
		t.Fatal("saved plan bypassed proof with yes/verified")
	}
}
func TestStartupDeliveryControllerMatrix(t *testing.T) {
	_, _, _, p := startupFixture(t, validStartupBaseline)
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "CronJob", "Job", "ReplicaSet", "Pod"} {
		t.Run(kind, func(t *testing.T) {
			p.Resource = kind + "/worker"
			p.Strategy = "restore-command"
			p.CanApply = true
			p.ApplyEligible = true
			p.Guardrails = nil
			p = fix.WithTargetPolicy(p)
			want := kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet" || kind == "CronJob"
			if p.ApplyEligible != want {
				t.Fatalf("eligible=%v want=%v", p.ApplyEligible, want)
			}
		})
	}
}
