package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type askContextPolicy string

const (
	askContextIdentityStatusOnly askContextPolicy = "identityStatusOnly"
	askContextWorkload           askContextPolicy = "workload"
	askContextNetworking         askContextPolicy = "networking"
	askContextStorage            askContextPolicy = "storage"
	askContextScheduling         askContextPolicy = "scheduling"
	askContextScaling            askContextPolicy = "scaling"
	askContextDisruption         askContextPolicy = "disruption"
	askContextPolicySecurity     askContextPolicy = "policySecurity"
	askContextOperator           askContextPolicy = "operator"
	askContextConfiguration      askContextPolicy = "configuration"
	askContextSecret             askContextPolicy = "secret"
)

// Every analyzer registry Kind maps to one reviewed context policy. Adding a
// kind without a policy is caught by TestAskContextPoliciesCoverAnalyzerRegistry.
var askContextPolicies = map[string]askContextPolicy{
	"Pod": askContextWorkload, "ReplicaSet": askContextWorkload, "Deployment": askContextWorkload,
	"StatefulSet": askContextWorkload, "DaemonSet": askContextWorkload, "Job": askContextWorkload, "CronJob": askContextWorkload,
	"Service": askContextNetworking, "Ingress": askContextNetworking, "GatewayClass": askContextNetworking,
	"Gateway": askContextNetworking, "HTTPRoute": askContextNetworking, "NetworkPolicy": askContextNetworking,
	"HorizontalPodAutoscaler": askContextScaling, "PodDisruptionBudget": askContextDisruption,
	"PersistentVolumeClaim": askContextStorage, "StorageClass": askContextStorage,
	"Node": askContextScheduling, "ResourceClaim": askContextScheduling,
	"ConfigMap": askContextConfiguration, "Secret": askContextSecret,
	"MutatingWebhookConfiguration": askContextPolicySecurity, "ValidatingWebhookConfiguration": askContextPolicySecurity,
	"PolicyReport": askContextPolicySecurity, "ScaledObject": askContextScaling,
	"VulnerabilityReport": askContextPolicySecurity, "ConfigAuditReport": askContextPolicySecurity,
	"ClusterServiceVersion": askContextOperator, "Subscription": askContextOperator, "InstallPlan": askContextOperator,
	"CatalogSource": askContextOperator, "OperatorGroup": askContextOperator, "ClusterCatalog": askContextOperator,
	"ClusterExtension": askContextOperator,
}

func askContextPolicyForKind(kind string) (askContextPolicy, bool) {
	policy, ok := askContextPolicies[kind]
	return policy, ok
}

// askObjectContextEvidence returns only reviewed, bounded facts. It never
// serializes the source object or reads a field not selected by a kind policy.
func askObjectContextEvidence(kind string, object map[string]any, checkSecretKeys bool) []analyzer.Evidence {
	policy, registered := askContextPolicyForKind(kind)
	if !registered {
		return nil
	}
	var evidence []analyzer.Evidence
	add := func(label string, value any) {
		if len(evidence) >= 12 || value == nil {
			return
		}
		var text string
		switch typed := value.(type) {
		case string:
			text = typed
		case map[string]any, []any:
			// Structured values must be reduced by a kind-specific summarizer
			// before they reach evidence. This prevents malformed CRD fields from
			// smuggling nested arbitrary data through a selected path.
			return
		default:
			var valid bool
			text, valid = askScalarText(typed)
			if !valid {
				return
			}
		}
		text = strings.TrimSpace(text)
		if text == "" || len(text) > 512 {
			return
		}
		evidence = append(evidence, analyzer.Evidence{Label: label, Value: text})
	}

	spec, _ := object["spec"].(map[string]any)
	status, _ := object["status"].(map[string]any)
	askAppendCommonStatus(add, status)
	if policy == askContextWorkload {
		if kind == "Pod" {
			askAppendPodStatusContext(add, status)
		}
		if value, ok := spec["replicas"]; ok {
			add("Configured replicas", value)
		}
		for _, path := range [][]string{{"template", "spec"}, {"jobTemplate", "spec", "template", "spec"}} {
			podSpec := askNestedMap(spec, path...)
			if len(podSpec) == 0 {
				continue
			}
			for _, key := range []string{"containers", "initContainers"} {
				containers, _ := podSpec[key].([]any)
				for _, raw := range containers {
					container, _ := raw.(map[string]any)
					askAppendContainerContext(add, container)
				}
			}
			break
		}
		if containers, ok := askNestedMap(object, "spec")["containers"].([]any); ok {
			for _, raw := range containers {
				container, _ := raw.(map[string]any)
				askAppendContainerContext(add, container)
			}
		}
	}
	switch kind {
	case "Service":
		add("Service type", spec["type"])
		add("Service selector", askStringMapSummary(spec["selector"]))
		add("Service ports", askServicePortsSummary(spec["ports"]))
	case "ConfigMap":
		add("ConfigMap keys", askObjectKeys(object, "data", "binaryData"))
	case "Secret":
		if checkSecretKeys {
			add("Secret type", object["type"])
			add("Secret keys", askObjectKeys(object, "data", "stringData"))
		}
	case "PersistentVolumeClaim":
		add("PVC storage class", spec["storageClassName"])
		add("PVC access modes", askStringSliceSummary(spec["accessModes"]))
		add("PVC requested storage", askNestedMap(spec, "resources", "requests")["storage"])
		add("PVC capacity", askNestedMap(status, "capacity")["storage"])
	case "NetworkPolicy":
		add("NetworkPolicy pod selector", askSelectorSummary(spec["podSelector"]))
		add("NetworkPolicy types", askStringSliceSummary(spec["policyTypes"]))
		add("NetworkPolicy ingress rule count", askSliceLength(spec["ingress"]))
		add("NetworkPolicy egress rule count", askSliceLength(spec["egress"]))
	case "Ingress":
		add("Ingress rules", strings.Join(askIngressSummary(spec["rules"]), ","))
		add("Ingress backends", strings.Join(askIngressBackendSummary(spec["rules"]), ","))
	case "GatewayClass":
		add("GatewayClass controller", spec["controllerName"])
	case "Gateway":
		add("Gateway listeners", strings.Join(askGatewayListenerSummary(spec["listeners"]), ","))
	case "HTTPRoute":
		add("HTTPRoute parents", strings.Join(askRouteReferenceSummary(spec["parentRefs"], true), ","))
		add("HTTPRoute backends", strings.Join(askRouteBackendSummary(spec["rules"]), ","))
	case "StatefulSet":
		add("StatefulSet replicas", askFieldSummary(status, "current", "currentReplicas", "updated", "updatedReplicas", "ready", "readyReplicas"))
	case "DaemonSet":
		add("DaemonSet scheduling", askFieldSummary(status, "desired", "desiredNumberScheduled", "ready", "numberReady"))
	case "Job":
		add("Job progress", askFieldSummary(status, "active", "active", "succeeded", "succeeded", "failed", "failed"))
	case "CronJob":
		add("CronJob schedule", spec["schedule"])
		add("CronJob suspended", spec["suspend"])
		add("CronJob last schedule", status["lastScheduleTime"])
	case "StorageClass":
		add("StorageClass provisioner", object["provisioner"])
		add("StorageClass reclaim policy", object["reclaimPolicy"])
		add("StorageClass binding mode", object["volumeBindingMode"])
		add("StorageClass allows expansion", object["allowVolumeExpansion"])
	case "Node":
		add("Node unschedulable", spec["unschedulable"])
		add("Node capacity", askResourceSummary(status["capacity"]))
		add("Node allocatable", askResourceSummary(status["allocatable"]))
	case "ResourceClaim":
		add("ResourceClaim resource class", spec["resourceClassName"])
		add("ResourceClaim allocation mode", spec["allocationMode"])
		allocation := status["allocation"]
		add("ResourceClaim allocated", allocation != nil)
		add("ResourceClaim device count", askNestedSliceLength(allocation, "devices", "results"))
	case "HorizontalPodAutoscaler":
		add("HPA target", askReferenceSummary(spec["scaleTargetRef"], "kind", "name"))
		add("HPA scale bounds", askFieldSummary(spec, "min", "minReplicas", "max", "maxReplicas"))
		add("HPA current state", askFieldSummary(status, "current", "currentReplicas", "desired", "desiredReplicas"))
	case "ScaledObject":
		add("KEDA scale target", askReferenceSummary(spec["scaleTargetRef"], "kind", "name"))
		add("KEDA scale bounds", askFieldSummary(spec, "min", "minReplicaCount", "max", "maxReplicaCount"))
		add("KEDA cooldown settings", askFieldSummary(spec, "pollingSeconds", "pollingInterval", "cooldownSeconds", "cooldownPeriod"))
	case "PodDisruptionBudget":
		add("PDB availability policy", askFieldSummary(spec, "minAvailable", "minAvailable", "maxUnavailable", "maxUnavailable"))
		add("PDB disruption state", askFieldSummary(status, "allowed", "disruptionsAllowed", "healthy", "currentHealthy", "desired", "desiredHealthy"))
	case "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration":
		add("Webhook names", strings.Join(askWebhookNames(object["webhooks"]), ","))
		add("Webhook policy summary", askWebhookPolicySummary(object["webhooks"]))
	case "PolicyReport":
		add("Policy report summary", askNamedCountSummary(object["summary"], "pass", "fail", "warn", "error", "skip"))
		add("Policy report result count", askSliceLength(object["results"]))
	case "VulnerabilityReport":
		report, _ := object["report"].(map[string]any)
		add("Vulnerability summary", askNamedCountSummary(report["summary"], "criticalCount", "highCount", "mediumCount", "lowCount", "unknownCount"))
		add("Vulnerability artifact", askReferenceSummary(report["artifact"], "repository", "tag"))
	case "ConfigAuditReport":
		report, _ := object["report"].(map[string]any)
		add("Audit summary", askNamedCountSummary(report["summary"], "criticalCount", "highCount", "mediumCount", "lowCount"))
	case "ClusterServiceVersion":
		add("Operator version", spec["version"])
		add("Operator phase", status["phase"])
		add("Operator reason", status["reason"])
	case "Subscription":
		add("Subscription channel", spec["channel"])
		add("Subscription source", askReferenceSummary(spec, "sourceNamespace", "source"))
		add("Subscription state", status["state"])
		add("Subscription current CSV", status["currentCSV"])
		add("Subscription installed CSV", status["installedCSV"])
	case "InstallPlan":
		add("InstallPlan approval", spec["approval"])
		add("InstallPlan approved", spec["approved"])
		add("InstallPlan phase", status["phase"])
	case "CatalogSource":
		add("Catalog source type", spec["sourceType"])
		add("Catalog image", spec["image"])
		add("Catalog connection state", askNestedValue(status, "connectionState", "lastObservedState"))
	case "OperatorGroup":
		add("OperatorGroup target namespace count", askSliceLength(spec["targetNamespaces"]))
	case "ClusterCatalog":
		source, _ := spec["source"].(map[string]any)
		add("ClusterCatalog source type", source["type"])
		add("ClusterCatalog source image", source["image"])
	case "ClusterExtension":
		add("ClusterExtension state", status["state"])
		add("ClusterExtension source", askReferenceSummary(spec["source"], "sourceType", "name"))
		add("ClusterExtension namespace", spec["namespace"])
	}
	return evidence
}

func askAppendPodStatusContext(add func(string, any), status map[string]any) {
	for _, field := range []string{"containerStatuses", "initContainerStatuses"} {
		statuses, _ := status[field].([]any)
		for _, raw := range statuses {
			containerStatus, _ := raw.(map[string]any)
			name, _ := containerStatus["name"].(string)
			if name == "" {
				continue
			}
			ready, _ := containerStatus["ready"].(bool)
			restarts := containerStatus["restartCount"]
			state, _ := containerStatus["state"].(map[string]any)
			stateName, stateValue := "", map[string]any(nil)
			for _, candidate := range []string{"waiting", "terminated", "running"} {
				if value, exists := state[candidate].(map[string]any); exists {
					stateName, stateValue = candidate, value
					break
				}
			}
			stateSummary := stateName
			if reason, ok := stateValue["reason"].(string); ok && reason != "" {
				stateSummary += ":" + reason
			}
			if exitCode, valid := askScalarText(stateValue["exitCode"]); valid {
				stateSummary += ":exit=" + exitCode
			}
			restartText, valid := askScalarText(restarts)
			if !valid {
				restartText = "unknown"
			}
			add("Container status", fmt.Sprintf("%s ready=%t restarts=%s state=%s", name, ready, restartText, stateSummary))
		}
	}
}

func askReferenceSummary(value any, keys ...string) string {
	object, _ := value.(map[string]any)
	var parts []string
	for _, key := range keys {
		if item, ok := object[key].(string); ok && item != "" {
			parts = append(parts, key+"="+item)
		}
	}
	return strings.Join(parts, " ")
}

func askNamedCountSummary(value any, keys ...string) string {
	object, _ := value.(map[string]any)
	var parts []string
	for _, key := range keys {
		if count, ok := object[key]; ok {
			if text, valid := askScalarText(count); valid {
				parts = append(parts, strings.TrimSuffix(key, "Count")+"="+text)
			}
		}
	}
	return strings.Join(parts, " ")
}

func askScalarText(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return fmt.Sprint(typed), true
	default:
		return "", false
	}
}

func askNestedValue(value any, path ...string) any {
	current := value
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[key]
	}
	return current
}

func askWebhookNames(value any) []string {
	webhooks, _ := value.([]any)
	var names []string
	for _, raw := range webhooks {
		webhook, _ := raw.(map[string]any)
		if name, ok := webhook["name"].(string); ok && name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func askWebhookPolicySummary(value any) string {
	webhooks, _ := value.([]any)
	var counts = map[string]int{}
	for _, raw := range webhooks {
		webhook, _ := raw.(map[string]any)
		if policy, ok := webhook["failurePolicy"].(string); ok && policy != "" {
			counts[policy]++
		}
	}
	var policies []string
	for policy := range counts {
		policies = append(policies, policy)
	}
	sort.Strings(policies)
	var summaries []string
	for _, policy := range policies {
		summaries = append(summaries, fmt.Sprintf("%s=%d", policy, counts[policy]))
	}
	return strings.Join(summaries, ",")
}

func askFieldSummary(object map[string]any, labelAndKeys ...string) string {
	var parts []string
	for index := 0; index+1 < len(labelAndKeys); index += 2 {
		key := labelAndKeys[index+1]
		if value, ok := object[key]; ok {
			if text, valid := askScalarText(value); valid {
				parts = append(parts, labelAndKeys[index]+"="+text)
			}
		}
	}
	return strings.Join(parts, " ")
}

func askResourceSummary(value any) string {
	resources, _ := value.(map[string]any)
	var parts []string
	for _, key := range []string{"cpu", "memory", "pods", "ephemeral-storage"} {
		if item, ok := resources[key]; ok {
			if text, valid := askScalarText(item); valid {
				parts = append(parts, key+"="+text)
			}
		}
	}
	return strings.Join(parts, " ")
}

func askNestedSliceLength(value any, path ...string) int {
	current := value
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return 0
		}
		current = object[key]
	}
	items, _ := current.([]any)
	return len(items)
}

func askIngressBackendSummary(value any) []string {
	rules, _ := value.([]any)
	var backends []string
	for _, rawRule := range rules {
		rule, _ := rawRule.(map[string]any)
		http, _ := rule["http"].(map[string]any)
		paths, _ := http["paths"].([]any)
		for _, rawPath := range paths {
			path, _ := rawPath.(map[string]any)
			backend, _ := path["backend"].(map[string]any)
			service, _ := backend["service"].(map[string]any)
			name, _ := service["name"].(string)
			port, _ := service["port"].(map[string]any)
			if name == "" {
				continue
			}
			portValue := port["number"]
			if portValue == nil {
				portValue = port["name"]
			}
			if text, valid := askScalarText(portValue); valid {
				backends = append(backends, fmt.Sprintf("%s:%s", name, text))
			} else {
				backends = append(backends, name)
			}
		}
	}
	sort.Strings(backends)
	return backends
}

func askGatewayListenerSummary(value any) []string {
	listeners, _ := value.([]any)
	var summaries []string
	for _, raw := range listeners {
		listener, _ := raw.(map[string]any)
		name, _ := listener["name"].(string)
		protocol, _ := listener["protocol"].(string)
		port, portOK := askScalarText(listener["port"])
		if name == "" || protocol == "" || !portOK {
			continue
		}
		summary := fmt.Sprintf("%s=%s:%s", name, protocol, port)
		if hostname, ok := listener["hostname"].(string); ok && hostname != "" {
			summary += ":" + hostname
		}
		summaries = append(summaries, summary)
	}
	sort.Strings(summaries)
	return summaries
}

func askRouteReferenceSummary(value any, includeNamespace bool) []string {
	refs, _ := value.([]any)
	var summaries []string
	for _, raw := range refs {
		ref, _ := raw.(map[string]any)
		name, _ := ref["name"].(string)
		if name == "" {
			continue
		}
		namespace, _ := ref["namespace"].(string)
		if includeNamespace && namespace != "" {
			name = namespace + "/" + name
		}
		summaries = append(summaries, name)
	}
	sort.Strings(summaries)
	return summaries
}

func askRouteBackendSummary(value any) []string {
	rules, _ := value.([]any)
	var summaries []string
	for _, rawRule := range rules {
		rule, _ := rawRule.(map[string]any)
		refs, _ := rule["backendRefs"].([]any)
		for _, rawRef := range refs {
			ref, _ := rawRef.(map[string]any)
			name, _ := ref["name"].(string)
			if name == "" {
				continue
			}
			if port, valid := askScalarText(ref["port"]); valid {
				summaries = append(summaries, fmt.Sprintf("%s:%s", name, port))
			} else if ref["port"] != nil {
				continue
			} else {
				summaries = append(summaries, name)
			}
		}
	}
	sort.Strings(summaries)
	return summaries
}

func askAppendCommonStatus(add func(string, any), status map[string]any) {
	add("Phase", status["phase"])
	add("Ready replicas", status["readyReplicas"])
	add("Available replicas", status["availableReplicas"])
	conditions, _ := status["conditions"].([]any)
	var summaries []string
	for _, raw := range conditions {
		condition, _ := raw.(map[string]any)
		typeName, _ := condition["type"].(string)
		conditionStatus, _ := condition["status"].(string)
		if typeName == "" || conditionStatus == "" {
			continue
		}
		summary := typeName + "=" + conditionStatus
		if reason, _ := condition["reason"].(string); reason != "" {
			summary += ":" + reason
		}
		summaries = append(summaries, summary)
	}
	sort.Strings(summaries)
	if len(summaries) > 8 {
		summaries = summaries[:8]
	}
	if len(summaries) > 0 {
		add("Condition states", strings.Join(summaries, ","))
	}
}

func askObjectKeys(object map[string]any, fields ...string) string {
	seen := map[string]bool{}
	var keys []string
	for _, field := range fields {
		values, _ := object[field].(map[string]any)
		for key := range values {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func askStringMapSummary(value any) string {
	values, _ := value.(map[string]any)
	safe := make(map[string]string, len(values))
	for key, raw := range values {
		if text, ok := raw.(string); ok {
			safe[key] = text
		}
	}
	if len(safe) == 0 {
		return ""
	}
	encoded, err := json.Marshal(safe)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func askSelectorSummary(value any) string {
	selector, _ := value.(map[string]any)
	safe := map[string]any{}
	if labels, ok := selector["matchLabels"].(map[string]any); ok {
		safeLabels := map[string]string{}
		for key, raw := range labels {
			if text, ok := raw.(string); ok {
				safeLabels[key] = text
			}
		}
		safe["matchLabels"] = safeLabels
	}
	if expressions, ok := selector["matchExpressions"].([]any); ok {
		var safeExpressions []map[string]any
		for _, raw := range expressions {
			expression, _ := raw.(map[string]any)
			item := map[string]any{}
			for _, key := range []string{"key", "operator"} {
				if text, ok := expression[key].(string); ok {
					item[key] = text
				}
			}
			if values, ok := expression["values"].([]any); ok {
				item["values"] = askStringSlice(values)
			}
			if len(item) > 0 {
				safeExpressions = append(safeExpressions, item)
			}
		}
		if len(safeExpressions) > 0 {
			safe["matchExpressions"] = safeExpressions
		}
	}
	if len(safe) == 0 {
		return ""
	}
	encoded, err := json.Marshal(safe)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func askStringSliceSummary(value any) string {
	items, _ := value.([]any)
	return strings.Join(askStringSlice(items), ",")
}

func askStringSlice(items []any) []string {
	var safe []string
	for _, item := range items {
		if text, ok := item.(string); ok {
			safe = append(safe, text)
		}
	}
	sort.Strings(safe)
	return safe
}

func askServicePortsSummary(value any) string {
	ports, _ := value.([]any)
	var summaries []string
	for _, raw := range ports {
		port, _ := raw.(map[string]any)
		parts := []string{}
		for _, key := range []string{"name", "port", "targetPort", "protocol"} {
			if text, valid := askScalarText(port[key]); valid && text != "" {
				parts = append(parts, key+"="+text)
			}
		}
		if len(parts) > 0 {
			summaries = append(summaries, strings.Join(parts, ":"))
		}
	}
	sort.Strings(summaries)
	return strings.Join(summaries, ",")
}

func askAppendContainerContext(add func(string, any), container map[string]any) {
	name, _ := container["name"].(string)
	if name == "" {
		return
	}
	if image, ok := container["image"].(string); ok && image != "" {
		add("Container image", name+"="+image)
	}
	if ports, ok := container["ports"].([]any); ok {
		for _, raw := range ports {
			port, _ := raw.(map[string]any)
			portName, _ := port["name"].(string)
			protocol, _ := port["protocol"].(string)
			if protocol == "" {
				protocol = "TCP"
			}
			if number, valid := askScalarText(port["containerPort"]); valid {
				add("Container ports", fmt.Sprintf("%s=%s:%s/%s", name, portName, number, protocol))
			}
		}
	}
	for _, probe := range []string{"readiness", "liveness", "startup"} {
		probeConfig, ok := container[probe+"Probe"].(map[string]any)
		if !ok {
			continue
		}
		probeType := "configured"
		for _, key := range []string{"httpGet", "tcpSocket", "grpc", "exec"} {
			if _, exists := probeConfig[key]; exists {
				probeType = key
				break
			}
		}
		add("Configured "+probe+" probe", name+"="+probeType)
	}
	if resources, ok := container["resources"].(map[string]any); ok {
		for _, resourceType := range []string{"requests", "limits"} {
			if values, ok := resources[resourceType].(map[string]any); ok && len(values) > 0 {
				add("Container resource "+resourceType, name+"="+askResourceSummary(values))
			}
		}
	}
}

func askNestedMap(object map[string]any, path ...string) map[string]any {
	current := object
	for _, key := range path {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func askSliceLength(value any) int {
	items, _ := value.([]any)
	return len(items)
}

func askIngressSummary(value any) []string {
	rules, _ := value.([]any)
	var hosts []string
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		if host, ok := rule["host"].(string); ok && host != "" {
			hosts = append(hosts, host)
		}
	}
	sort.Strings(hosts)
	return hosts
}
