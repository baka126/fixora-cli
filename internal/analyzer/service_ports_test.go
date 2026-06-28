package analyzer

import (
	"context"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func servicePortCtx(t *testing.T, services []map[string]any, pods kube.PodList) (*ScanContext, Analyzer) {
	t.Helper()
	reader := fakeReader{items: map[string][]map[string]any{"services": services}, pods: pods}
	opts := Options{Namespace: "prod"}
	return NewScanContext(context.Background(), reader, opts), New(reader, opts)
}

func podWithPorts(name string, labels map[string]string, ports []kube.ContainerPort) kube.Pod {
	return kube.Pod{
		Metadata: kube.ObjectMeta{Name: name, Namespace: "prod", Labels: labels},
		Spec:     kube.PodSpec{Containers: []kube.Container{{Name: "app", Ports: ports}}},
	}
}

func svc(name string, selector map[string]any, ports []any) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": name},
		"spec":     map[string]any{"selector": selector, "ports": ports},
	}
}

func hasStatus(findings []Finding, status string) *Finding {
	for i := range findings {
		if findings[i].Status == status {
			return &findings[i]
		}
	}
	return nil
}

func TestServicePortNumericMatchNoFinding(t *testing.T) {
	services := []map[string]any{svc("api", map[string]any{"app": "api"},
		[]any{map[string]any{"port": float64(80), "targetPort": float64(8080)}})}
	pods := kube.PodList{Items: []kube.Pod{podWithPorts("api-0", map[string]string{"app": "api"},
		[]kube.ContainerPort{{ContainerPort: 8080}})}}
	ctx, a := servicePortCtx(t, services, pods)
	findings, err := a.analyzeServicePortTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f := hasStatus(findings, "ServicePortMismatch"); f != nil {
		t.Fatalf("unexpected mismatch: %+v", f)
	}
}

func TestServicePortNumericMismatchMedium(t *testing.T) {
	services := []map[string]any{svc("api", map[string]any{"app": "api"},
		[]any{map[string]any{"port": float64(80), "targetPort": float64(8080)}})}
	pods := kube.PodList{Items: []kube.Pod{podWithPorts("api-0", map[string]string{"app": "api"},
		[]kube.ContainerPort{{ContainerPort: 9090}})}}
	ctx, a := servicePortCtx(t, services, pods)
	findings, err := a.analyzeServicePortTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f := hasStatus(findings, "ServicePortMismatch")
	if f == nil {
		t.Fatal("expected ServicePortMismatch")
	}
	if f.Severity != "medium" {
		t.Fatalf("severity = %q, want medium", f.Severity)
	}
}

func TestServicePortNamedMatchNoFinding(t *testing.T) {
	services := []map[string]any{svc("api", map[string]any{"app": "api"},
		[]any{map[string]any{"port": float64(80), "targetPort": "http"}})}
	pods := kube.PodList{Items: []kube.Pod{podWithPorts("api-0", map[string]string{"app": "api"},
		[]kube.ContainerPort{{Name: "http", ContainerPort: 8080}})}}
	ctx, a := servicePortCtx(t, services, pods)
	findings, _ := a.analyzeServicePortTargets(ctx)
	if f := hasStatus(findings, "ServicePortMismatch"); f != nil {
		t.Fatalf("unexpected mismatch: %+v", f)
	}
}

func TestServicePortNamedMismatchHigh(t *testing.T) {
	services := []map[string]any{svc("api", map[string]any{"app": "api"},
		[]any{map[string]any{"port": float64(80), "targetPort": "grpc"}})}
	pods := kube.PodList{Items: []kube.Pod{podWithPorts("api-0", map[string]string{"app": "api"},
		[]kube.ContainerPort{{Name: "http", ContainerPort: 8080}})}}
	ctx, a := servicePortCtx(t, services, pods)
	findings, _ := a.analyzeServicePortTargets(ctx)
	f := hasStatus(findings, "ServicePortMismatch")
	if f == nil {
		t.Fatal("expected ServicePortMismatch")
	}
	if f.Severity != "high" {
		t.Fatalf("severity = %q, want high", f.Severity)
	}
}

func TestServicePortNoDeclaredPortsSkips(t *testing.T) {
	services := []map[string]any{svc("api", map[string]any{"app": "api"},
		[]any{map[string]any{"port": float64(80), "targetPort": float64(8080)}})}
	pods := kube.PodList{Items: []kube.Pod{podWithPorts("api-0", map[string]string{"app": "api"}, nil)}}
	ctx, a := servicePortCtx(t, services, pods)
	findings, _ := a.analyzeServicePortTargets(ctx)
	if f := hasStatus(findings, "ServicePortMismatch"); f != nil {
		t.Fatalf("must skip when no container ports declared: %+v", f)
	}
}

func TestServicePortNoSelectedPodsSkips(t *testing.T) {
	services := []map[string]any{svc("api", map[string]any{"app": "api"},
		[]any{map[string]any{"port": float64(80), "targetPort": float64(8080)}})}
	pods := kube.PodList{Items: []kube.Pod{podWithPorts("other-0", map[string]string{"app": "other"},
		[]kube.ContainerPort{{ContainerPort: 1234}})}}
	ctx, a := servicePortCtx(t, services, pods)
	findings, _ := a.analyzeServicePortTargets(ctx)
	if f := hasStatus(findings, "ServicePortMismatch"); f != nil {
		t.Fatalf("must skip when no pods selected: %+v", f)
	}
}

func TestServicePortExternalNameSkips(t *testing.T) {
	services := []map[string]any{{
		"metadata": map[string]any{"namespace": "prod", "name": "ext"},
		"spec":     map[string]any{"type": "ExternalName", "selector": map[string]any{"app": "api"}},
	}}
	pods := kube.PodList{Items: []kube.Pod{podWithPorts("api-0", map[string]string{"app": "api"},
		[]kube.ContainerPort{{ContainerPort: 9090}})}}
	ctx, a := servicePortCtx(t, services, pods)
	findings, _ := a.analyzeServicePortTargets(ctx)
	if len(findings) != 0 {
		t.Fatalf("ExternalName must be skipped, got %+v", findings)
	}
}

func TestNetworkingStatusesDoNotCollideWithPlannerKeys(t *testing.T) {
	statuses := []string{"ServicePortMismatch", "IngressPortMismatch", "EndpointsBlockedByReadinessGate"}
	plannerKeys := []string{
		"ImagePull", "OOMKilled", "CrashLoopBackOff", "CreateContainerConfigError",
		"ExecFormatError", "PermissionDenied", "NoEndpoints", "ConnectionRefused",
		"HPA", "Evicted", "Webhook",
	}
	for _, s := range statuses {
		for _, key := range plannerKeys {
			if strings.Contains(s, key) || strings.Contains(key, s) {
				t.Fatalf("status %q collides with planner key %q", s, key)
			}
		}
	}
}
