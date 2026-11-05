package shadow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"k8s.io/client-go/kubernetes/fake"
)

func TestUntrustedShadowRequestCannotExecuteStartupMutation(t *testing.T) {
	c := &kube.TypedClient{Clientset: fake.NewClientset()}
	p := fix.Plan{Resource: "Deployment/api", Namespace: "prod", Strategy: "restore-command", ApplyEligible: true, CanApply: true, PatchTemplate: startupPatch}
	data, _ := json.Marshal(p)
	var replay fix.Plan
	_ = json.Unmarshal(data, &replay)
	_, err := Run(context.Background(), c, Request{Plan: replay, Resource: p.Resource, Namespace: p.Namespace, Patch: startupPatch, ProbeImage: testIsolationProbeImage})
	if err == nil || !strings.Contains(err.Error(), "proof") {
		t.Fatalf("want missing proof refusal, got %v", err)
	}
	if len(c.Clientset.(*fake.Clientset).Actions()) != 0 {
		t.Fatal("untrusted request touched the cluster")
	}
}

func TestPolicyFieldsCannotBeRelabeledToBypassProof(t *testing.T) {
	for _, patch := range []string{startupPatch, `apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: prod}
spec: {template: {spec: {nodeSelector: {pool: approved}}}}
`} {
		c := &kube.TypedClient{Clientset: fake.NewClientset()}
		_, err := Run(context.Background(), c, Request{Resource: "Deployment/api", Namespace: "prod", Patch: patch, Plan: fix.Plan{Strategy: "image"}, ProbeImage: testIsolationProbeImage})
		if err == nil || !strings.Contains(err.Error(), "proof") {
			t.Fatalf("relabelled candidate accepted: %v", err)
		}
		if len(c.Clientset.(*fake.Clientset).Actions()) != 0 {
			t.Fatal("unproved request reached cluster")
		}
	}
}
