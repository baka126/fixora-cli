package analyzer

import (
	"context"
	"fmt"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func TestDeploymentReplicaMismatchWaitsForCurrentStatus(t *testing.T) {
	cases := []struct {
		name                 string
		generation, observed int
		conditions           []any
		want                 int
	}{
		{"stale", 2, 1, nil, 0},
		{"healthy rollout", 2, 2, []any{map[string]any{"type": "Progressing", "status": "True"}, map[string]any{"type": "Available", "status": "True"}}, 0},
		{"stalled", 2, 2, []any{map[string]any{"type": "Progressing", "status": "False"}}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "api", "generation": tc.generation}, "spec": map[string]any{"replicas": 3}, "status": map[string]any{"observedGeneration": tc.observed, "replicas": 2, "readyReplicas": 2, "conditions": tc.conditions}}
			a := New(fakeReader{items: map[string][]map[string]any{"deployments": {obj}}}, Options{Namespace: "prod"})
			got, err := a.analyzeDeployments(NewScanContext(context.Background(), a.k, a.opts))
			if err != nil || len(got) != tc.want {
				t.Fatalf("findings=%v err=%v", got, err)
			}
		})
	}
}

func TestPodEventsRequireExactObjectIdentity(t *testing.T) {
	pod := kube.Pod{Metadata: kube.ObjectMeta{Name: "api", Namespace: "prod", UID: "new"}}
	events := []kube.Event{
		{InvolvedObject: kube.ObjectReference{Kind: "Pod", Namespace: "prod", Name: "api", UID: "new"}},
		{InvolvedObject: kube.ObjectReference{Kind: "Pod", Namespace: "other", Name: "api", UID: "new"}},
		{InvolvedObject: kube.ObjectReference{Kind: "Deployment", Namespace: "prod", Name: "api", UID: "new"}},
		{InvolvedObject: kube.ObjectReference{Kind: "Pod", Namespace: "prod", Name: "api", UID: "old"}},
		{Metadata: kube.ObjectMeta{Namespace: "prod"}, Message: "api failed"},
	}
	got := eventsForPod(events, pod)
	if len(got) != 1 {
		t.Fatalf("want one exact event, got %#v", got)
	}
}

func TestNetworkPolicyExpressionSelector(t *testing.T) {
	policy := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "allow-app"}, "spec": map[string]any{"podSelector": map[string]any{"matchExpressions": []any{map[string]any{"key": "app", "operator": "In", "values": []any{"api"}}}}}}
	pod := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "api-1", "labels": map[string]any{"app": "api"}}}
	a := New(fakeReader{items: map[string][]map[string]any{"networkpolicies": {policy}, "pods": {pod}}}, Options{Namespace: "prod"})
	got, err := a.analyzeNetworkPolicies(NewScanContext(context.Background(), a.k, a.opts))
	if err != nil || len(got) != 0 {
		t.Fatalf("findings=%v err=%v", got, err)
	}
}

func TestNetworkPolicyExpressionDoesNotSelectOtherNamespace(t *testing.T) {
	policy := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "allow-app"}, "spec": map[string]any{"podSelector": map[string]any{"matchExpressions": []any{map[string]any{"key": "app", "operator": "In", "values": []any{"api"}}}}}}
	pod := map[string]any{"metadata": map[string]any{"namespace": "staging", "name": "api-1", "labels": map[string]any{"app": "api"}}}
	a := New(fakeReader{items: map[string][]map[string]any{"networkpolicies": {policy}, "pods": {pod}}}, Options{Namespace: "prod"})
	got, err := a.analyzeNetworkPolicies(NewScanContext(context.Background(), a.k, a.opts))
	if err != nil || len(got) != 1 || got[0].Status != "NoSelectedPods" {
		t.Fatalf("findings=%v err=%v", got, err)
	}
}

func TestResourceClaimFindsMissingDeviceClass(t *testing.T) {
	claim := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "gpu"}, "spec": map[string]any{"devices": map[string]any{"requests": []any{map[string]any{"name": "gpu", "deviceClassName": "nvidia.com"}}}}}
	reader := fakeReader{items: map[string][]map[string]any{"resourceclaims.resource.k8s.io": {claim}, "deviceclasses.resource.k8s.io": {}}}
	a := New(reader, Options{Namespace: "prod", Filters: []string{"resourceclaim"}})
	got, skipped := a.runPrecisionAnalyzers(NewScanContext(context.Background(), reader, a.opts))
	if len(skipped) != 0 || len(got) != 1 || got[0].Status != "DeviceClassNotFound" {
		t.Fatalf("findings=%#v skipped=%#v", got, skipped)
	}
	reader.items["deviceclasses.resource.k8s.io"] = []map[string]any{{"metadata": map[string]any{"name": "nvidia.com"}}}
	got, skipped = a.runPrecisionAnalyzers(NewScanContext(context.Background(), reader, a.opts))
	if len(skipped) != 0 || len(got) != 0 {
		t.Fatalf("present class: findings=%#v skipped=%#v", got, skipped)
	}
}

func TestResourceClaimSkipsWhenDeviceClassesUnavailable(t *testing.T) {
	claim := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "gpu"}, "spec": map[string]any{"devices": map[string]any{"requests": []any{map[string]any{"deviceClassName": "nvidia.com"}}}}}
	reader := fakeReader{items: map[string][]map[string]any{"resourceclaims.resource.k8s.io": {claim}}, itemErrs: map[string]error{"deviceclasses.resource.k8s.io": fmt.Errorf("forbidden")}}
	a := New(reader, Options{Namespace: "prod", Filters: []string{"resourceclaim"}})
	got, skipped := a.runPrecisionAnalyzers(NewScanContext(context.Background(), reader, a.opts))
	if len(got) != 0 || len(skipped) != 1 {
		t.Fatalf("findings=%#v skipped=%#v", got, skipped)
	}
}

func TestPodFindingIncludesIdentityMatchedEventWithoutNameInMessage(t *testing.T) {
	pod := kube.Pod{Metadata: kube.ObjectMeta{Name: "api", Namespace: "prod", UID: "new"}, Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{Name: "api", State: map[string]kube.StatusState{"waiting": {Reason: "CrashLoopBackOff"}}}}}}
	event := kube.Event{Metadata: kube.ObjectMeta{Namespace: "prod"}, InvolvedObject: kube.ObjectReference{Kind: "Pod", Namespace: "prod", Name: "api", UID: "new"}, Reason: "BackOff", Message: "Back-off restarting failed container"}
	a := New(fakeReader{}, Options{Namespace: "prod"})
	finding, ok := a.findingForPod(context.Background(), NewScanContext(context.Background(), a.k, a.opts), pod, []kube.Event{event})
	if !ok {
		t.Fatal("expected finding")
	}
	for _, e := range finding.Evidence {
		if e.Label == "Event BackOff" {
			return
		}
	}
	t.Fatalf("missing event evidence: %#v", finding.Evidence)
}

func TestNetworkPolicyExpressionOperators(t *testing.T) {
	for _, tc := range []struct {
		name, operator string
		values         []any
		want           bool
	}{
		{"in", "In", []any{"api"}, true}, {"not in", "NotIn", []any{"worker"}, true}, {"exists", "Exists", nil, true}, {"does not exist", "DoesNotExist", nil, false}, {"malformed", "Unknown", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "p"}, "spec": map[string]any{"podSelector": map[string]any{"matchExpressions": []any{map[string]any{"key": "app", "operator": tc.operator, "values": tc.values}}}}}
			pod := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "api", "labels": map[string]any{"app": "api"}}}
			a := New(fakeReader{items: map[string][]map[string]any{"networkpolicies": {policy}, "pods": {pod}}}, Options{Namespace: "prod"})
			got, err := a.analyzeNetworkPolicies(NewScanContext(context.Background(), a.k, a.opts))
			if err != nil {
				t.Fatal(err)
			}
			if tc.want && len(got) != 0 {
				t.Fatalf("unexpected findings %#v", got)
			}
			if !tc.want && tc.operator != "Unknown" && (len(got) != 1 || got[0].Status != "NoSelectedPods") {
				t.Fatalf("expected unmatched selector, got %#v", got)
			}
			if tc.operator == "Unknown" && len(got) != 0 {
				t.Fatalf("malformed selector should not create a false finding: %#v", got)
			}
		})
	}
}

func TestResourceClaimDeduplicatesClassRequests(t *testing.T) {
	claim := map[string]any{"metadata": map[string]any{"namespace": "prod", "name": "gpu"}, "spec": map[string]any{"devices": map[string]any{"requests": []any{map[string]any{"deviceClassName": "gpu.example"}, map[string]any{"deviceClassName": "gpu.example"}}}}}
	reader := fakeReader{items: map[string][]map[string]any{"resourceclaims.resource.k8s.io": {claim}, "deviceclasses.resource.k8s.io": {}}}
	a := New(reader, Options{Namespace: "prod"})
	got, err := a.analyzeResourceClaims(NewScanContext(context.Background(), reader, a.opts))
	if err != nil || len(got) != 1 {
		t.Fatalf("findings=%#v err=%v", got, err)
	}
}
