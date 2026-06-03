package analyzer

import (
	"fmt"
	"sort"
	"strings"
)

var registry = []Definition{
	{Name: "Pod", Kind: "Pod", Resource: "pods", Scope: "namespaced", Description: "Pod phase, container states, restarts, events, and optional logs", Enabled: true},
	{Name: "ReplicaSet", Kind: "ReplicaSet", Resource: "replicasets", Scope: "namespaced", Description: "ReplicaSet desired/ready/available state", Enabled: true},
	{Name: "Deployment", Kind: "Deployment", Resource: "deployments", Scope: "namespaced", Description: "Deployment availability and rollout conditions", Enabled: true},
	{Name: "StatefulSet", Kind: "StatefulSet", Resource: "statefulsets", Scope: "namespaced", Description: "StatefulSet replica readiness and storage-related conditions", Enabled: true},
	{Name: "DaemonSet", Kind: "DaemonSet", Resource: "daemonsets", Scope: "namespaced", Description: "DaemonSet scheduled/ready/misscheduled state", Enabled: true},
	{Name: "Job", Kind: "Job", Resource: "jobs", Scope: "namespaced", Description: "Job failed/succeeded/active status", Enabled: true},
	{Name: "CronJob", Kind: "CronJob", Resource: "cronjobs", Scope: "namespaced", Description: "CronJob suspension and last schedule state", Enabled: true},
	{Name: "Service", Kind: "Service", Resource: "services", Scope: "namespaced", Description: "Service selector and endpoint availability hints", Enabled: true},
	{Name: "Ingress", Kind: "Ingress", Resource: "ingresses", Scope: "namespaced", Description: "Ingress load balancer and routing status", Enabled: true},
	{Name: "Gateway", Kind: "Gateway", Resource: "gateways.gateway.networking.k8s.io", Scope: "namespaced", Description: "Gateway API listener and condition health", Enabled: true},
	{Name: "HTTPRoute", Kind: "HTTPRoute", Resource: "httproutes.gateway.networking.k8s.io", Scope: "namespaced", Description: "Gateway API route acceptance and backend refs", Enabled: true},
	{Name: "HPA", Kind: "HorizontalPodAutoscaler", Resource: "hpa", Scope: "namespaced", Description: "Autoscaler conditions and target health", Enabled: true},
	{Name: "PDB", Kind: "PodDisruptionBudget", Resource: "pdb", Scope: "namespaced", Description: "Disruption budget availability", Enabled: true},
	{Name: "PVC", Kind: "PersistentVolumeClaim", Resource: "pvc", Scope: "namespaced", Description: "PVC pending/lost/bound status", Enabled: true},
	{Name: "ConfigMap", Kind: "ConfigMap", Resource: "configmaps", Scope: "namespaced", Description: "ConfigMap presence and GitOps/Helm ownership hints", Enabled: false},
	{Name: "NetworkPolicy", Kind: "NetworkPolicy", Resource: "networkpolicies", Scope: "namespaced", Description: "Network policy inventory for connectivity RCA context", Enabled: false},
	{Name: "Node", Kind: "Node", Resource: "nodes", Scope: "cluster", Description: "Node readiness, pressure, taints, allocatable capacity, and pricing hints", Enabled: true},
	{Name: "StorageClass", Kind: "StorageClass", Resource: "storageclasses", Scope: "cluster", Description: "StorageClass inventory for PVC RCA context", Enabled: false},
	{Name: "MutatingWebhook", Kind: "MutatingWebhookConfiguration", Resource: "mutatingwebhookconfigurations", Scope: "cluster", Description: "Admission webhook health and timeout risk", Enabled: true},
	{Name: "ValidatingWebhook", Kind: "ValidatingWebhookConfiguration", Resource: "validatingwebhookconfigurations", Scope: "cluster", Description: "Validation webhook health and timeout risk", Enabled: true},
	{Name: "KyvernoPolicyReport", Kind: "PolicyReport", Resource: "policyreports.wgpolicyk8s.io", Scope: "namespaced", Description: "Kyverno policy report failures", Enabled: true},
	{Name: "KedaScaledObject", Kind: "ScaledObject", Resource: "scaledobjects.keda.sh", Scope: "namespaced", Description: "KEDA ScaledObject readiness and scaler errors", Enabled: true},
	{Name: "TrivyVulnerabilityReport", Kind: "VulnerabilityReport", Resource: "vulnerabilityreports.aquasecurity.github.io", Scope: "namespaced", Description: "Trivy Operator vulnerability findings", Enabled: true},
	{Name: "TrivyConfigAuditReport", Kind: "ConfigAuditReport", Resource: "configauditreports.aquasecurity.github.io", Scope: "namespaced", Description: "Trivy Operator config audit findings", Enabled: true},
	{Name: "OLMClusterServiceVersion", Kind: "ClusterServiceVersion", Resource: "clusterserviceversions.operators.coreos.com", Scope: "namespaced", Description: "OLM operator install and upgrade health", Enabled: true},
	{Name: "OLMSubscription", Kind: "Subscription", Resource: "subscriptions.operators.coreos.com", Scope: "namespaced", Description: "OLM subscription health and catalog resolution", Enabled: true},
	{Name: "OLMInstallPlan", Kind: "InstallPlan", Resource: "installplans.operators.coreos.com", Scope: "namespaced", Description: "OLM install plan phase and approval status", Enabled: true},
	{Name: "OLMCatalogSource", Kind: "CatalogSource", Resource: "catalogsources.operators.coreos.com", Scope: "namespaced", Description: "OLM catalog source health", Enabled: true},
}

func ListAnalyzers(filters []string) []Definition {
	selected := filterSet(filters)
	out := make([]Definition, 0, len(registry))
	for _, def := range registry {
		if len(selected) > 0 {
			def.Enabled = selected[strings.ToLower(def.Name)] || selected[strings.ToLower(def.Kind)] || selected[strings.ToLower(def.Resource)]
		}
		out = append(out, def)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func (a Analyzer) runRegistry(ctx *ScanContext) ([]Finding, []SkippedCheck) {
	findings := []Finding{}
	skipped := []SkippedCheck{}
	selected := filterSet(a.opts.Filters)
	for _, def := range registry {
		if def.Name == "Pod" {
			continue
		}
		if len(selected) == 0 && !def.Enabled {
			continue
		}
		if len(selected) > 0 && !selected[strings.ToLower(def.Name)] && !selected[strings.ToLower(def.Kind)] && !selected[strings.ToLower(def.Resource)] {
			continue
		}
		if def.Scope == "cluster" {
			list, err := a.analyzeResourceList(ctx, def, "", true)
			if err != nil {
				skipped = append(skipped, SkippedCheck{Name: def.Resource, Reason: err.Error()})
				continue
			}
			findings = append(findings, list...)
			continue
		}
		list, err := a.analyzeResourceList(ctx, def, a.opts.Namespace, a.opts.AllNS)
		if err != nil {
			skipped = append(skipped, SkippedCheck{Name: def.Resource, Reason: err.Error()})
			continue
		}
		findings = append(findings, list...)
	}
	return findings, skipped
}

func (a Analyzer) analyzeResourceList(ctx *ScanContext, def Definition, namespace string, allNS bool) ([]Finding, error) {
	items, err := ctx.GetResourceItems(namespace, allNS, def.Resource)
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for _, item := range items {
		f, ok := a.findingForRegisteredObject(item, def)
		if ok {
			out = append(out, f)
		}
	}
	return out, nil
}

func (a Analyzer) findingForRegisteredObject(obj map[string]any, def Definition) (Finding, bool) {
	meta, _ := obj["metadata"].(map[string]any)
	name := fmt.Sprint(meta["name"])
	namespace := fmt.Sprint(meta["namespace"])
	if namespace == "<nil>" {
		namespace = ""
	}
	statusMap, _ := obj["status"].(map[string]any)
	if !objectLooksUnhealthy(def, statusMap) {
		return Finding{}, false
	}
	labels, annotations := objectLabelsAnnotations(obj)
	status := compactMap(statusMap)
	if status == "" {
		status = "Unhealthy"
	}
	return Finding{
		ID:           firstNonEmpty(namespace, "cluster") + "/" + def.Kind + "/" + name,
		Namespace:    namespace,
		ResourceKind: def.Kind,
		ResourceName: name,
		Status:       status,
		Severity:     severityForRegistered(def, status),
		Category:     categoryForRegistered(def),
		Summary:      summaryForRegistered(def, status),
		GitOps:       gitOpsHints(labels, annotations),
		Evidence:     []Evidence{{Label: def.Name + " status", Value: status}},
		Recommendations: []Recommendation{{
			Title:         "Inspect " + def.Kind + " evidence",
			Description:   recommendationForRegistered(def),
			PatchType:     strings.ToLower(def.Name),
			SafeByDefault: false,
		}},
	}, true
}

func objectLooksUnhealthy(def Definition, status map[string]any) bool {
	if len(status) == 0 {
		switch def.Name {
		case "MutatingWebhook", "ValidatingWebhook", "NetworkPolicy", "ConfigMap", "StorageClass":
			return false
		default:
			return false
		}
	}
	flat := strings.ToLower(compactMap(status))
	badMarkers := []string{"false", "failed", "failure", "unavailable", "degraded", "pending", "lost", "misscheduled", "backoff", "denied", "timeout", "error"}
	switch def.Name {
	case "PVC":
		return strings.Contains(flat, "phase=pending") || strings.Contains(flat, "phase=lost")
	case "Node":
		return strings.Contains(flat, "ready") && strings.Contains(flat, "false") || strings.Contains(flat, "pressure") && strings.Contains(flat, "true")
	default:
		for _, marker := range badMarkers {
			if strings.Contains(flat, marker) {
				return true
			}
		}
	}
	return false
}

func severityForRegistered(def Definition, status string) string {
	flat := strings.ToLower(status)
	switch {
	case strings.Contains(flat, "failed"), strings.Contains(flat, "unavailable"), strings.Contains(flat, "notready"):
		return "high"
	case strings.Contains(flat, "pending"), strings.Contains(flat, "false"):
		return "medium"
	default:
		return "low"
	}
}

func categoryForRegistered(def Definition) string {
	switch def.Name {
	case "PVC", "StorageClass":
		return "storage"
	case "Service", "Ingress", "Gateway", "HTTPRoute", "NetworkPolicy":
		return "networking"
	case "HPA", "PDB":
		return "policy"
	case "Node":
		return "node"
	case "KyvernoPolicyReport", "MutatingWebhook", "ValidatingWebhook":
		return "policy"
	case "KedaScaledObject":
		return "autoscaling"
	case "TrivyVulnerabilityReport", "TrivyConfigAuditReport":
		return "security"
	case "OLMClusterServiceVersion", "OLMSubscription", "OLMInstallPlan", "OLMCatalogSource":
		return "operator"
	default:
		return "workload"
	}
}

func summaryForRegistered(def Definition, status string) string {
	return fmt.Sprintf("%s analyzer found a potentially unhealthy %s state: %s", def.Name, def.Kind, trim(status, 180))
}

func recommendationForRegistered(def Definition) string {
	switch def.Name {
	case "Service":
		return "Check selectors, endpoints, readiness probes, targetPort, and backend pod health before changing routing."
	case "Ingress", "Gateway", "HTTPRoute":
		return "Check route attachment, backend refs, service endpoints, TLS secrets, and controller events."
	case "PVC":
		return "Check StorageClass, access mode, zone constraints, provisioner events, and volume quota."
	case "HPA":
		return "Check metrics availability, target references, min/max replicas, and resource requests."
	case "Node":
		return "Check node conditions, taints, allocatable resources, and affected pods."
	case "KyvernoPolicyReport":
		return "Review the failing policy result and update manifests to satisfy policy before applying."
	case "KedaScaledObject":
		return "Check trigger authentication, scaler target, metrics source, and KEDA operator events."
	case "TrivyVulnerabilityReport", "TrivyConfigAuditReport":
		return "Review affected image, package, severity, and available fixed versions before promoting or rolling back workloads."
	case "OLMClusterServiceVersion", "OLMSubscription", "OLMInstallPlan", "OLMCatalogSource":
		return "Check OLM conditions, catalog availability, install plan approval, and operator pod events."
	default:
		return "Inspect related events, owner chain, logs, and GitOps source before patching."
	}
}

func filterSet(filters []string) map[string]bool {
	out := map[string]bool{}
	for _, item := range filters {
		for _, part := range strings.Split(item, ",") {
			part = strings.ToLower(strings.TrimSpace(part))
			if part != "" {
				out[part] = true
			}
		}
	}
	return out
}

func trim(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
