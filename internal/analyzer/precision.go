package analyzer

import (
	"fmt"
	"sort"
	"strings"
)

func (a Analyzer) runPrecisionAnalyzers(ctx *ScanContext) ([]Finding, []SkippedCheck) {
	type precisionAnalyzer struct {
		name    string
		aliases []string
		run     func(*ScanContext) ([]Finding, error)
	}
	analyzers := []precisionAnalyzer{
		{name: "service-endpoints", aliases: []string{"service", "services", "networking"}, run: a.analyzeServiceEndpoints},
		{name: "service-ports", aliases: []string{"service", "services", "networking"}, run: a.analyzeServicePortTargets},
		{name: "ingress-backends", aliases: []string{"ingress", "ingresses", "networking"}, run: a.analyzeIngressBackends},
		{name: "hpa-targets", aliases: []string{"hpa", "horizontalpodautoscaler", "autoscaling"}, run: a.analyzeHPATargets},
		{name: "pdb-disruptions", aliases: []string{"pdb", "poddisruptionbudget", "policy"}, run: a.analyzePDBs},
		{name: "webhook-backends", aliases: []string{"webhook", "mutatingwebhook", "validatingwebhook", "policy"}, run: a.analyzeWebhooks},
		{name: "gateway-api", aliases: []string{"gateway", "gatewayclass", "httproute", "networking"}, run: a.analyzeGatewayAPI},
		{name: "network-policy", aliases: []string{"networkpolicy", "networkpolicies", "netpol", "networking"}, run: a.analyzeNetworkPolicies},
		{name: "rbac-risk", aliases: []string{"rbac", "role", "clusterrole", "rolebinding", "clusterrolebinding", "security"}, run: a.analyzeRBAC},
		{name: "pod-security", aliases: []string{"pod-security", "podsecurity", "security"}, run: a.analyzePodSecurity},
		{name: "node-conditions", aliases: []string{"node", "nodes", "scheduling"}, run: a.analyzeNodes},
		{name: "pvc", aliases: []string{"pvc", "persistentvolumeclaim", "persistentvolumeclaims", "storage"}, run: a.analyzePVCs},
		{name: "storage", aliases: []string{"storage", "pv", "persistentvolume", "storageclass"}, run: a.analyzeStorage},
		{name: "configmap", aliases: []string{"configmap", "configmaps", "configuration"}, run: a.analyzeConfigMaps},
		{name: "olm-operators", aliases: []string{"olm", "operator", "operators", "catalogsource", "subscription", "installplan", "clusterserviceversion", "operatorgroup", "clustercatalog", "clusterextension"}, run: a.analyzeOLM},
		{name: "deployment-replicas", aliases: []string{"deployment", "deployments", "workload"}, run: a.analyzeDeployments},
		{name: "daemonset", aliases: []string{"daemonset", "daemonsets", "workload"}, run: a.analyzeDaemonSets},
		{name: "statefulset", aliases: []string{"statefulset", "statefulsets", "workload"}, run: a.analyzeStatefulSets},
		{name: "job", aliases: []string{"job", "jobs", "workload"}, run: a.analyzeJobs},
		{name: "cronjob", aliases: []string{"cronjob", "cronjobs", "workload"}, run: a.analyzeCronJobs},
		{name: "replicaset", aliases: []string{"replicaset", "replicasets", "workload"}, run: a.analyzeReplicaSets},
		{name: "secret", aliases: []string{"secret", "secrets", "configuration"}, run: a.analyzeSecrets},
	}
	findings := []Finding{}
	skipped := []SkippedCheck{}
	selected := filterSet(a.opts.Filters)
	for _, analyzer := range analyzers {
		if len(selected) > 0 && !matchesAny(selected, analyzer.aliases...) {
			continue
		}
		list, err := analyzer.run(ctx)
		if err != nil {
			skipped = append(skipped, SkippedCheck{Name: analyzer.name, Reason: err.Error()})
			continue
		}
		findings = append(findings, list...)
	}
	return findings, skipped
}

func matchesAny(selected map[string]bool, aliases ...string) bool {
	for _, alias := range aliases {
		if selected[strings.ToLower(alias)] {
			return true
		}
	}
	return false
}

func objectNamespaceName(obj map[string]any) (string, string) {
	meta := nestedMap(obj, "metadata")
	namespace := strValue(meta["namespace"])
	name := strValue(meta["name"])
	return namespace, name
}

func objectKey(obj map[string]any) string {
	namespace, name := objectNamespaceName(obj)
	return keyFor(namespace, name)
}

func keyFor(namespace, name string) string {
	if namespace == "" {
		return "cluster/" + name
	}
	return namespace + "/" + name
}

func nestedMap(obj map[string]any, key string) map[string]any {
	if obj == nil {
		return map[string]any{}
	}
	value, _ := obj[key].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func nestedSlice(obj map[string]any, key string) []any {
	if obj == nil {
		return nil
	}
	value, _ := obj[key].([]any)
	return value
}

func strValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func boolValue(value any) bool {
	v, _ := value.(bool)
	return v
}

func compactStringMap(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for key, value := range values {
		parts = append(parts, key+"="+value)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func gitOpsForObject(obj map[string]any) GitOpsHints {
	labels, annotations := objectLabelsAnnotations(obj)
	return gitOpsHints(labels, annotations)
}

func endpointAddressCounts(subsets []any) (ready, notReady int) {
	for _, subset := range subsets {
		subsetMap, _ := subset.(map[string]any)
		ready += len(nestedSlice(subsetMap, "addresses"))
		notReady += len(nestedSlice(subsetMap, "notReadyAddresses"))
	}
	return ready, notReady
}

func ingressBackendServices(spec map[string]any) []string {
	seen := map[string]bool{}
	add := func(value string) {
		if value != "" {
			seen[value] = true
		}
	}
	add(backendServiceName(nestedMap(spec, "defaultBackend")))
	for _, rule := range nestedSlice(spec, "rules") {
		ruleMap, _ := rule.(map[string]any)
		http := nestedMap(ruleMap, "http")
		for _, path := range nestedSlice(http, "paths") {
			pathMap, _ := path.(map[string]any)
			add(backendServiceName(nestedMap(pathMap, "backend")))
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func ingressTLSSecretNames(spec map[string]any) []string {
	seen := map[string]bool{}
	for _, tls := range nestedSlice(spec, "tls") {
		tlsMap, _ := tls.(map[string]any)
		if name := strValue(tlsMap["secretName"]); name != "" {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func backendServiceName(backend map[string]any) string {
	service := nestedMap(backend, "service")
	if name := strValue(service["name"]); name != "" {
		return name
	}
	return strValue(backend["serviceName"])
}

func hpaResourceMetricNames(spec map[string]any) []string {
	seen := map[string]bool{}
	for _, metric := range nestedSlice(spec, "metrics") {
		metricMap, _ := metric.(map[string]any)
		if strValue(metricMap["type"]) != "Resource" {
			continue
		}
		resource := nestedMap(metricMap, "resource")
		if name := strValue(resource["name"]); name != "" {
			seen[name] = true
		}
	}
	if len(seen) == 0 && spec["targetCPUUtilizationPercentage"] != nil {
		seen["cpu"] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func containersMissingResourceRequest(obj map[string]any, resource string) []string {
	containers := podTemplateContainers(obj)
	missing := []string{}
	for _, container := range containers {
		containerMap, _ := container.(map[string]any)
		name := strValue(containerMap["name"])
		requests := nestedMap(nestedMap(containerMap, "resources"), "requests")
		if strValue(requests[resource]) == "" {
			missing = append(missing, firstNonEmpty(name, "<unnamed>"))
		}
	}
	sort.Strings(missing)
	return missing
}

func podTemplateContainers(obj map[string]any) []any {
	kind := strings.ToLower(strValue(obj["kind"]))
	if kind == "pod" {
		return nestedSlice(nestedMap(obj, "spec"), "containers")
	}
	template := nestedMap(nestedMap(obj, "spec"), "template")
	return nestedSlice(nestedMap(template, "spec"), "containers")
}

type backendRef struct {
	Kind      string
	Namespace string
	Name      string
	Port      int
}

func httpRouteBackendRefs(route map[string]any) []backendRef {
	spec := nestedMap(route, "spec")
	out := []backendRef{}
	for _, rule := range nestedSlice(spec, "rules") {
		ruleMap, _ := rule.(map[string]any)
		for _, ref := range nestedSlice(ruleMap, "backendRefs") {
			refMap, _ := ref.(map[string]any)
			out = append(out, backendRef{Kind: firstNonEmpty(strValue(refMap["kind"]), "Service"), Namespace: strValue(refMap["namespace"]), Name: strValue(refMap["name"]), Port: intValue(refMap["port"])})
		}
	}
	return out
}

func httpRouteParentRefs(route map[string]any) []backendRef {
	spec := nestedMap(route, "spec")
	out := []backendRef{}
	for _, ref := range nestedSlice(spec, "parentRefs") {
		refMap, _ := ref.(map[string]any)
		out = append(out, backendRef{Kind: firstNonEmpty(strValue(refMap["kind"]), "Gateway"), Namespace: strValue(refMap["namespace"]), Name: strValue(refMap["name"])})
	}
	return out
}

type objectState struct {
	Exists    bool
	Forbidden bool
	Message   string
}

func (a Analyzer) objectNameState(ctx *ScanContext, namespace, resource, name string) objectState {
	if name == "" {
		return objectState{Message: "empty name"}
	}
	args := []string{"get", resource, name}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	args = append(args, "-o", "name")
	_, err := ctx.Reader.Run(ctx.Context, args...)
	if err == nil {
		return objectState{Exists: true, Message: "readable"}
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "forbidden"), strings.Contains(msg, "unauthorized"):
		return objectState{Forbidden: true, Message: "resource exists or may exist but is not readable: " + err.Error()}
	case strings.Contains(msg, "notfound"), strings.Contains(msg, "not found"):
		return objectState{Message: "not found"}
	case strings.Contains(msg, "the server doesn't have a resource type"), strings.Contains(msg, "no matches for kind"):
		return objectState{Message: "unknown resource type: " + err.Error()}
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "context deadline"):
		return objectState{Message: "timeout: " + err.Error()}
	default:
		return objectState{Message: "unknown API error: " + err.Error()}
	}
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		var out int
		_, _ = fmt.Sscanf(v, "%d", &out)
		return out
	default:
		return 0
	}
}

func sliceHasWildcard(values []any) bool {
	for _, value := range values {
		if strValue(value) == "*" {
			return true
		}
	}
	return false
}

func clusterFinding(kind, name, status, severity, category, summary string, evidence []Evidence) Finding {
	return Finding{
		ID:           "cluster/" + kind + "/" + name + "/" + status,
		ResourceKind: kind,
		ResourceName: name,
		Status:       status,
		Severity:     severity,
		Category:     category,
		Summary:      summary,
		Evidence:     evidence,
		Recommendations: []Recommendation{{
			Title:         "Review controller dependency",
			Description:   "Fix the referenced dependency or relax the policy only after confirming production blast radius.",
			PatchType:     strings.ToLower(kind),
			SafeByDefault: false,
		}},
	}
}
