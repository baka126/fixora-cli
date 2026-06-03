package integration

import (
	"context"
	"strconv"
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
		alertmanager(ctx, k),
		trivy(ctx, k),
		aws(ctx, k),
		kyverno(ctx, k),
		keda(ctx, k),
	}
}

func findServiceByLabels(ctx context.Context, k kube.Kubectl, target string) (string, string, bool, error) {
	selectors := []string{
		"app.kubernetes.io/name=" + target,
		"app=" + target,
		"k8s-app=" + target,
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	var lastErr error
	for _, sel := range selectors {
		if err := k.GetJSON(ctx, &list, "get", "services", "-A", "-l", sel, "-o", "json"); err == nil {
			for _, item := range list.Items {
				name, ns := meta(item)
				if strings.Contains(strings.ToLower(name), target) {
					return name, ns, true, nil
				}
			}
		} else {
			lastErr = err
		}
	}
	return "", "", false, lastErr
}

func alertmanager(ctx context.Context, k kube.Kubectl) Status {
	name, ns, found, err := findServiceByLabels(ctx, k, "alertmanager")
	if found {
		return Status{Name: "Alertmanager", Detected: true, Detail: ns + "/" + name, Analyzer: "AlertCorrelation", FreeLocal: true}
	}
	name, ns, found, nameErr := findServiceByName(ctx, k, "alertmanager")
	if found {
		return Status{Name: "Alertmanager", Detected: true, Detail: ns + "/" + name, Analyzer: "AlertCorrelation", FreeLocal: true}
	}
	if err != nil {
		return Status{Name: "Alertmanager", Detail: err.Error(), FreeLocal: true}
	}
	if nameErr != nil {
		return Status{Name: "Alertmanager", Detail: nameErr.Error(), FreeLocal: true}
	}
	return Status{Name: "Alertmanager", Detail: "no alertmanager service detected", FreeLocal: true}
}

func trivy(ctx context.Context, k kube.Kubectl) Status {
	for _, resource := range []string{"vulnerabilityreports.aquasecurity.github.io", "configauditreports.aquasecurity.github.io", "clustercompliancereports.aquasecurity.github.io"} {
		items, err := k.GetResourceItems(ctx, "", true, resource)
		if err == nil && len(items) > 0 {
			return Status{Name: "Trivy", Detected: true, Detail: countDetail(len(items), resource), Analyzer: "TrivyReports", FreeLocal: true}
		}
	}
	return Status{Name: "Trivy", Detail: "Trivy Operator report CRDs not readable", Analyzer: "TrivyReports", FreeLocal: true}
}

func prometheus(ctx context.Context, k kube.Kubectl) Status {
	name, ns, found, err := findServiceByLabels(ctx, k, "prometheus")
	if found {
		return Status{Name: "Prometheus", Detected: true, Detail: ns + "/" + name, Analyzer: "PrometheusConfig", FreeLocal: true}
	}
	name, ns, found, nameErr := findServiceByName(ctx, k, "prometheus")
	if found {
		return Status{Name: "Prometheus", Detected: true, Detail: ns + "/" + name, Analyzer: "PrometheusConfig", FreeLocal: true}
	}
	if err != nil {
		return Status{Name: "Prometheus", Detail: err.Error(), FreeLocal: true}
	}
	if nameErr != nil {
		return Status{Name: "Prometheus", Detail: nameErr.Error(), FreeLocal: true}
	}
	return Status{Name: "Prometheus", Detail: "no prometheus service detected", FreeLocal: true}
}

func findServiceByName(ctx context.Context, k kube.Kubectl, target string) (string, string, bool, error) {
	items, err := k.GetResourceItems(ctx, "", true, "services")
	if err != nil {
		return "", "", false, err
	}
	for _, item := range items {
		name, ns := meta(item)
		if strings.Contains(strings.ToLower(name), target) {
			return name, ns, true, nil
		}
	}
	return "", "", false, nil
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
	return strings.TrimSpace(strings.Join([]string{strconv.Itoa(count), label}, " "))
}
