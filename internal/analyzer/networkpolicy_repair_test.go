package analyzer

import (
	"context"
	"fmt"
	"testing"
)

func TestNetworkPolicyInvalidSelectorIsDiagnosed(t *testing.T) {
	policy := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "bad", "uid": "p1"}, "spec": map[string]any{"podSelector": map[string]any{"matchExpressions": []any{map[string]any{"key": "app", "operator": "Invalid"}}}}}
	reader := fakeReader{items: map[string][]map[string]any{"networkpolicies": {policy}, "pods": {}}}
	a := New(reader, Options{Namespace: "prod"})
	got, err := a.analyzeNetworkPolicies(NewScanContext(context.Background(), reader, a.opts))
	if err != nil || len(got) != 1 || got[0].Status != "InvalidPodSelector" {
		t.Fatalf("findings=%#v err=%v", got, err)
	}
}

func TestNetworkPolicyOverlapIsReportedAsAdditive(t *testing.T) {
	selector := map[string]any{"matchLabels": map[string]any{"app": "api"}}
	policies := []map[string]any{
		{"metadata": map[string]any{"namespace": "prod", "name": "one", "uid": "p1"}, "spec": map[string]any{"podSelector": selector}},
		{"metadata": map[string]any{"namespace": "prod", "name": "two", "uid": "p2"}, "spec": map[string]any{"podSelector": selector}},
	}
	pod := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "api-1", "uid": "pod1", "labels": map[string]any{"app": "api"}}}
	reader := fakeReader{items: map[string][]map[string]any{"networkpolicies": policies, "pods": {pod}}}
	a := New(reader, Options{Namespace: "prod"})
	got, err := a.analyzeNetworkPolicies(NewScanContext(context.Background(), reader, a.opts))
	if err != nil || countStatus(got, "OverlappingPolicies") != 2 {
		t.Fatalf("findings=%#v err=%v", got, err)
	}
}

func TestNetworkPolicyUnreadablePodsRetainExplicitDiagnosis(t *testing.T) {
	policy := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "p1"}, "spec": map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": "api"}}}}
	reader := fakeReader{items: map[string][]map[string]any{"networkpolicies": {policy}}, itemErrs: map[string]error{"pods": fmt.Errorf("forbidden")}}
	a := New(reader, Options{Namespace: "prod"})
	got, err := a.analyzeNetworkPolicies(NewScanContext(context.Background(), reader, a.opts))
	if err != nil || len(got) != 1 || got[0].Status != "PodListUnavailable" {
		t.Fatalf("findings=%#v err=%v", got, err)
	}
}

func TestNetworkPolicySourceOwnershipIsExposed(t *testing.T) {
	policy := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "p1", "annotations": map[string]any{"argocd.argoproj.io/tracking-id": "app:networking.k8s.io/NetworkPolicy:prod/p1"}}, "spec": map[string]any{"podSelector": map[string]any{}}}
	reader := fakeReader{items: map[string][]map[string]any{"networkpolicies": {policy}, "pods": {}}}
	a := New(reader, Options{Namespace: "prod"})
	got, err := a.analyzeNetworkPolicies(NewScanContext(context.Background(), reader, a.opts))
	if err != nil || len(got) != 1 || got[0].GitOps.ArgoHint == "" {
		t.Fatalf("findings=%#v err=%v", got, err)
	}
}

func countStatus(findings []Finding, status string) int {
	count := 0
	for _, finding := range findings {
		if finding.Status == status {
			count++
		}
	}
	return count
}
