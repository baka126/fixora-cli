package servicefix

import (
	"context"
	"encoding/json"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"k8s.io/apimachinery/pkg/util/intstr"
	"testing"
)

func serviceFinding() analyzer.Finding {
	return analyzer.Finding{ResourceKind: "Service", ResourceName: "gateway", Namespace: "sample", ResourceUID: "svc-1", Status: "NoEndpoints"}
}
func TestServiceCandidateExactFields(t *testing.T) {
	a, r := serviceFixture(t)
	r.service.Spec.Selector["obsolete"] = "remove"
	p, err := Prepare(context.Background(), r, a, serviceFinding(), "")
	if err != nil || p.Proof == nil || p.Status != "candidate-prepared" {
		t.Fatal(p, err)
	}
	c := p.Proof.candidate
	if len(c.Spec.Selector) != 1 || c.Spec.Selector["workload"] != "arbitrary" || c.Spec.Ports[0].TargetPort.StrVal != "http" || c.Spec.ClusterIP != r.service.Spec.ClusterIP {
		t.Fatal(c)
	}
	b, _ := json.Marshal(p)
	var saved Plan
	json.Unmarshal(b, &saved)
	if saved.Proof != nil {
		t.Fatal("proof serialized")
	}
	if err = p.Proof.CheckFresh(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.service.ResourceVersion = "2"
	if p.Proof.CheckFresh(context.Background(), r) == nil {
		t.Fatal("stale proof")
	}
}
func TestServiceProofNoReplayOrPolicyBypass(t *testing.T) {
	for _, bad := range []string{"recovered", "AI", "identity", "fields"} {
		t.Run(bad, func(t *testing.T) {
			a, r := serviceFixture(t)
			f := serviceFinding()
			proposal := ""
			switch bad {
			case "recovered":
				f.Recovered = true
			case "AI":
				f.AI = &analyzer.AIResult{Confidence: 50}
				proposal = "apiVersion: v1\nkind: Service\nmetadata: {name: gateway, namespace: sample}\nspec: {selector: {workload: arbitrary}}\n"
			case "identity":
				f.ResourceUID = "other"
			case "fields":
				proposal = "apiVersion: v1\nkind: Service\nmetadata: {name: gateway, namespace: sample}\nspec: {clusterIP: 10.0.9.9}\n"
			}
			p, err := Prepare(context.Background(), r, a, f, proposal)
			if err == nil || p.Proof != nil {
				t.Fatal("unsafe proof", p)
			}
		})
	}
}

func TestServiceCandidateNoOpAndSourceOwnership(t *testing.T) {
	a, r := serviceFixture(t)
	r.service.Spec.Selector = map[string]string{"workload": "arbitrary"}
	r.service.Spec.Ports[0].TargetPort = intstr.FromString("http")
	p, err := Prepare(context.Background(), r, a, serviceFinding(), "")
	if err != nil || p.Proof != nil || p.Status != "no-change" {
		t.Fatal(p, err)
	}
	a, r = serviceFixture(t)
	r.service.Annotations = map[string]string{"meta.helm.sh/release-name": "owned"}
	p, err = Prepare(context.Background(), r, a, serviceFinding(), "")
	if err != nil || p.Status != "source-review-required" || !p.Proof.sourceOnly {
		t.Fatal(p, err)
	}
}
