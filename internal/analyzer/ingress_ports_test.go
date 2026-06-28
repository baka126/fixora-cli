package analyzer

import (
	"context"
	"testing"
)

func ingressPortCtx(t *testing.T, ingresses, services []map[string]any) (*ScanContext, Analyzer) {
	t.Helper()
	reader := fakeReader{items: map[string][]map[string]any{"ingresses": ingresses, "services": services}}
	opts := Options{Namespace: "prod"}
	return NewScanContext(context.Background(), reader, opts), New(reader, opts)
}

func ingressTo(svcName string, port any) map[string]any {
	portMap := map[string]any{}
	switch v := port.(type) {
	case string:
		portMap["name"] = v
	default:
		portMap["number"] = v
	}
	return map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "web"},
		"spec": map[string]any{
			"ingressClassName": "nginx",
			"rules": []any{map[string]any{"http": map[string]any{"paths": []any{
				map[string]any{"backend": map[string]any{"service": map[string]any{
					"name": svcName,
					"port": portMap,
				}}},
			}}}},
		},
	}
}

func svcWithPorts(name string, ports []any) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": name},
		"spec":     map[string]any{"ports": ports},
	}
}

func TestIngressBackendNumberPortMatchNoFinding(t *testing.T) {
	ingresses := []map[string]any{ingressTo("api", float64(80))}
	services := []map[string]any{svcWithPorts("api", []any{map[string]any{"port": float64(80)}})}
	ctx, a := ingressPortCtx(t, ingresses, services)
	findings, err := a.analyzeIngressBackends(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f := hasStatus(findings, "IngressPortMismatch"); f != nil {
		t.Fatalf("unexpected mismatch: %+v", f)
	}
}

func TestIngressBackendNumberPortMismatch(t *testing.T) {
	ingresses := []map[string]any{ingressTo("api", float64(8080))}
	services := []map[string]any{svcWithPorts("api", []any{map[string]any{"port": float64(80)}})}
	ctx, a := ingressPortCtx(t, ingresses, services)
	findings, _ := a.analyzeIngressBackends(ctx)
	if f := hasStatus(findings, "IngressPortMismatch"); f == nil {
		t.Fatal("expected IngressPortMismatch")
	}
}

func TestIngressBackendNamedPortMismatch(t *testing.T) {
	ingresses := []map[string]any{ingressTo("api", "grpc")}
	services := []map[string]any{svcWithPorts("api", []any{map[string]any{"port": float64(80), "name": "http"}})}
	ctx, a := ingressPortCtx(t, ingresses, services)
	findings, _ := a.analyzeIngressBackends(ctx)
	if f := hasStatus(findings, "IngressPortMismatch"); f == nil {
		t.Fatal("expected IngressPortMismatch for unexposed named port")
	}
}

func TestIngressBackendMissingServiceNoPortMismatch(t *testing.T) {
	ingresses := []map[string]any{ingressTo("absent", float64(80))}
	services := []map[string]any{} // service does not exist
	ctx, a := ingressPortCtx(t, ingresses, services)
	findings, _ := a.analyzeIngressBackends(ctx)
	if f := hasStatus(findings, "IngressPortMismatch"); f != nil {
		t.Fatalf("absent service must not yield port mismatch: %+v", f)
	}
	if f := hasStatus(findings, "MissingBackendService"); f == nil {
		t.Fatal("existing MissingBackendService must still be emitted")
	}
}
