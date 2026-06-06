package analyzer

import "strings"

func DefaultIncidentFilters(quick bool) []string {
	if quick {
		return []string{"pod"}
	}
	return []string{"pod", "deployment", "statefulset", "daemonset", "replicaset", "job", "cronjob", "service", "ingress", "hpa", "pdb", "pvc", "node"}
}

func SmartFiltersFor(resource, status string) []string {
	kind, _ := splitResourceKindName(resource)
	text := strings.ToLower(kind + " " + status + " " + resource)
	add := func(values ...string) []string {
		out := []string{"pod"}
		seen := map[string]bool{"pod": true}
		for _, value := range values {
			value = strings.ToLower(strings.TrimSpace(value))
			if value != "" && !seen[value] {
				seen[value] = true
				out = append(out, value)
			}
		}
		return out
	}

	switch {
	case containsAnyText(text, "service", "endpoint", "endpoints", "noendpoints", "connectionrefused", "dns"):
		return add("service", "networking")
	case containsAnyText(text, "ingress", "httproute", "gateway", "route"):
		return add("service", "ingress", "gateway", "networking")
	case containsAnyText(text, "hpa", "horizontalpodautoscaler", "autoscal"):
		return add("hpa", "deployment", "statefulset", "daemonset")
	case containsAnyText(text, "pdb", "poddisruptionbudget", "evicted", "disruption"):
		return add("pdb", "node", "deployment", "statefulset", "daemonset")
	case containsAnyText(text, "pvc", "persistentvolume", "storage", "volume", "mount"):
		return add("pvc", "storage", "storageclass", "node")
	case containsAnyText(text, "webhook", "admission"):
		return add("webhook", "service", "networking")
	case containsAnyText(text, "rbac", "role", "forbidden", "unauthorized"):
		return add("rbac", "security")
	case containsAnyText(text, "security", "policy", "kyverno", "trivy", "vulnerability"):
		return add("security", "policy", "policyreport", "vulnerabilityreport", "configauditreport")
	case containsAnyText(text, "node", "pressure", "taint", "unschedulable", "pending", "scheduling"):
		return add("node", "pdb")
	case containsAnyText(text, "job", "cronjob"):
		return add("job", "cronjob")
	case containsAnyText(text, "statefulset", "daemonset", "replicaset", "deployment", "deploy"):
		return add(kind, "service", "hpa", "pdb")
	default:
		return DefaultIncidentFilters(true)
	}
}

func splitResourceKindName(resource string) (string, string) {
	resource = strings.TrimSpace(resource)
	if resource == "" {
		return "", ""
	}
	parts := strings.SplitN(resource, "/", 2)
	if len(parts) != 2 {
		return strings.ToLower(resource), ""
	}
	return strings.ToLower(parts[0]), strings.ToLower(parts[1])
}

func containsAnyText(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
