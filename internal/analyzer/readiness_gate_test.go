package analyzer

import (
	"context"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func readinessCtx(t *testing.T, service map[string]any, pods kube.PodList) (*ScanContext, Analyzer) {
	t.Helper()
	// No endpointslices/endpoints provided -> ready==0 -> NoEndpoints path.
	reader := fakeReader{items: map[string][]map[string]any{"services": {service}}, pods: pods}
	opts := Options{Namespace: "prod"}
	return NewScanContext(context.Background(), reader, opts), New(reader, opts)
}

func gateBlockedPod() kube.Pod {
	return kube.Pod{
		Metadata: kube.ObjectMeta{Name: "api-0", Namespace: "prod", Labels: map[string]string{"app": "api"}},
		Spec: kube.PodSpec{
			Containers:     []kube.Container{{Name: "app"}},
			ReadinessGates: []kube.ReadinessGate{{ConditionType: "example.com/ready"}},
		},
		Status: kube.PodStatus{
			ContainerStatuses: []kube.ContainerStatus{{Name: "app", Ready: true}},
			Conditions:        []kube.Condition{{Type: "example.com/ready", Status: "False"}},
		},
	}
}

func selectorService() map[string]any {
	return map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "api"},
		"spec":     map[string]any{"selector": map[string]any{"app": "api"}},
	}
}

func TestReadinessGateBlocksEndpointsSuppressesNoEndpoints(t *testing.T) {
	ctx, a := readinessCtx(t, selectorService(), kube.PodList{Items: []kube.Pod{gateBlockedPod()}})
	findings, err := a.analyzeServiceEndpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hasStatus(findings, "NoEndpoints") != nil {
		t.Fatal("NoEndpoints must be suppressed when a readiness gate is the cause")
	}
	f := hasStatus(findings, "EndpointsBlockedByReadinessGate")
	if f == nil {
		t.Fatal("expected EndpointsBlockedByReadinessGate")
	}
}

func TestNoReadinessGateStillReportsNoEndpoints(t *testing.T) {
	pod := gateBlockedPod()
	pod.Spec.ReadinessGates = nil
	ctx, a := readinessCtx(t, selectorService(), kube.PodList{Items: []kube.Pod{pod}})
	findings, _ := a.analyzeServiceEndpoints(ctx)
	if hasStatus(findings, "NoEndpoints") == nil {
		t.Fatal("without a readiness gate, NoEndpoints must still be reported")
	}
	if hasStatus(findings, "EndpointsBlockedByReadinessGate") != nil {
		t.Fatal("must not report a gate finding when no gate exists")
	}
}

func TestReadinessGateSatisfiedNotFlagged(t *testing.T) {
	pod := gateBlockedPod()
	pod.Status.Conditions = []kube.Condition{{Type: "example.com/ready", Status: "True"}}
	ctx, a := readinessCtx(t, selectorService(), kube.PodList{Items: []kube.Pod{pod}})
	findings, _ := a.analyzeServiceEndpoints(ctx)
	if hasStatus(findings, "EndpointsBlockedByReadinessGate") != nil {
		t.Fatal("a satisfied gate must not be flagged")
	}
}

func TestReadinessGateContainersNotReadyNotFlagged(t *testing.T) {
	pod := gateBlockedPod()
	pod.Status.ContainerStatuses = []kube.ContainerStatus{{Name: "app", Ready: false}}
	ctx, a := readinessCtx(t, selectorService(), kube.PodList{Items: []kube.Pod{pod}})
	findings, _ := a.analyzeServiceEndpoints(ctx)
	if hasStatus(findings, "EndpointsBlockedByReadinessGate") != nil {
		t.Fatal("gate finding requires containers otherwise ready")
	}
}
