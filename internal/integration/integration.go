package integration

import (
	"context"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

type Status struct {
	Name      string `json:"name"`
	Detected  bool   `json:"detected"`
	Detail    string `json:"detail"`
	Analyzer  string `json:"analyzer,omitempty"`
	FreeLocal bool   `json:"freeLocal"`
}

func List(ctx context.Context, k kube.Kubectl) []Status {
	return []Status{
		prometheus(ctx, k),
		aws(ctx, k),
		kyverno(ctx, k),
		keda(ctx, k),
	}
}

func prometheus(ctx context.Context, k kube.Kubectl) Status {
	items, err := k.GetResourceItems(ctx, "", true, "services")
	if err != nil {
		return Status{Name: "Prometheus", Detail: err.Error(), FreeLocal: true}
	}
	for _, item := range items {
		name, ns := meta(item)
		if strings.Contains(strings.ToLower(name), "prometheus") {
			return Status{Name: "Prometheus", Detected: true, Detail: ns + "/" + name, Analyzer: "PrometheusConfig", FreeLocal: true}
		}
	}
	return Status{Name: "Prometheus", Detail: "no prometheus service detected", FreeLocal: true}
}

func aws(ctx context.Context, k kube.Kubectl) Status {
	nodes, err := k.GetNodes(ctx)
	if err != nil {
		return Status{Name: "AWS/EKS", Detail: err.Error(), FreeLocal: true}
	}
	for _, node := range nodes {
		if strings.HasPrefix(node.Spec.ProviderID, "aws://") {
			return Status{Name: "AWS/EKS", Detected: true, Detail: "node providerID uses aws://", Analyzer: "NodeCost", FreeLocal: true}
		}
	}
	return Status{Name: "AWS/EKS", Detail: "no aws:// node providerID detected", FreeLocal: true}
}

func kyverno(ctx context.Context, k kube.Kubectl) Status {
	items, err := k.GetResourceItems(ctx, "", true, "policyreports.wgpolicyk8s.io")
	if err != nil {
		return Status{Name: "Kyverno", Detail: "PolicyReport CRD not readable", Analyzer: "KyvernoPolicyReport", FreeLocal: true}
	}
	return Status{Name: "Kyverno", Detected: len(items) > 0, Detail: countDetail(len(items), "policy reports"), Analyzer: "KyvernoPolicyReport", FreeLocal: true}
}

func keda(ctx context.Context, k kube.Kubectl) Status {
	items, err := k.GetResourceItems(ctx, "", true, "scaledobjects.keda.sh")
	if err != nil {
		return Status{Name: "KEDA", Detail: "ScaledObject CRD not readable", Analyzer: "KedaScaledObject", FreeLocal: true}
	}
	return Status{Name: "KEDA", Detected: len(items) > 0, Detail: countDetail(len(items), "scaled objects"), Analyzer: "KedaScaledObject", FreeLocal: true}
}

func meta(item map[string]any) (string, string) {
	m, _ := item["metadata"].(map[string]any)
	name, _ := m["name"].(string)
	ns, _ := m["namespace"].(string)
	return name, ns
}

func countDetail(count int, label string) string {
	if count == 1 {
		return "1 " + strings.TrimSuffix(label, "s")
	}
	return strings.TrimSpace(strings.Join([]string{itoa(count), label}, " "))
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	n := i
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
