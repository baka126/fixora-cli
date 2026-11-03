package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"k8s.io/apimachinery/pkg/labels"
)

// askInventoryFindings builds bounded Ask targets and safe resource context.
// Secret objects are not read unless their existing opt-in is enabled, and
// their values are never included in generated context.
func askInventoryFindings(ctx context.Context, reader kube.Reader, opts options, filters []string) ([]analyzer.Finding, []analyzer.SkippedCheck) {
	selector := labels.Everything()
	if strings.TrimSpace(opts.labelSelector) != "" {
		parsed, err := labels.Parse(opts.labelSelector)
		if err != nil {
			return nil, []analyzer.SkippedCheck{{Name: "Ask inventory", Reason: "invalid label selector"}}
		}
		selector = parsed
	}

	var findings []analyzer.Finding
	var skipped []analyzer.SkippedCheck
	for _, def := range analyzer.AskInventoryDefinitions(filters) {
		if def.Name == "Secret" && !opts.checkSecretKeys {
			continue
		}
		namespace, allNS := opts.namespace, opts.allNS
		if def.Scope == "cluster" {
			namespace, allNS = "", true
		}
		items, err := reader.GetResourceItems(ctx, namespace, allNS, def.Resource)
		if err != nil {
			skipped = append(skipped, analyzer.SkippedCheck{Name: def.Resource, Reason: err.Error()})
			continue
		}
		for _, item := range items {
			meta, _ := item["metadata"].(map[string]any)
			name := strings.TrimSpace(fmt.Sprint(meta["name"]))
			if name == "" || name == "<nil>" {
				continue
			}
			objectNamespace := strings.TrimSpace(fmt.Sprint(meta["namespace"]))
			if objectNamespace == "<nil>" {
				objectNamespace = ""
			}
			if def.Scope != "cluster" && !opts.allNS && objectNamespace != opts.namespace {
				continue
			}
			if !selector.Matches(askObjectLabels(meta["labels"])) {
				continue
			}
			uid, _ := meta["uid"].(string)
			evidence := []analyzer.Evidence{{Label: "Resource identity", Value: def.Kind + "/" + name}}
			evidence = append(evidence, askObjectContextEvidence(def.Kind, item)...)
			findings = append(findings, analyzer.Finding{
				ID:           askInventoryID(objectNamespace, def.Kind, name),
				ResourceUID:  uid,
				Namespace:    firstAskValue(objectNamespace, "cluster"),
				ResourceKind: def.Kind,
				ResourceName: name,
				Status:       "Observed",
				Severity:     "info",
				Category:     "inventory",
				Summary:      "Live resource exists; selected analyzers reported no finding for this object.",
				Evidence:     evidence,
			})
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		return askFindingIdentity(findings[i]) < askFindingIdentity(findings[j])
	})
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Name < skipped[j].Name })
	return findings, skipped
}

func askObjectContextEvidence(kind string, object map[string]any) []analyzer.Evidence {
	var evidence []analyzer.Evidence
	add := func(label string, value any) {
		if len(evidence) >= 12 || value == nil {
			return
		}
		var text string
		switch typed := value.(type) {
		case string:
			text = typed
		default:
			encoded, err := json.Marshal(typed)
			if err != nil {
				return
			}
			text = string(encoded)
		}
		text = strings.TrimSpace(text)
		if text == "" || len(text) > 512 {
			return
		}
		evidence = append(evidence, analyzer.Evidence{Label: label, Value: text})
	}

	spec, _ := object["spec"].(map[string]any)
	status, _ := object["status"].(map[string]any)
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
	switch kind {
	case "Service":
		add("Service type", spec["type"])
		add("Service selector", spec["selector"])
		add("Service ports", spec["ports"])
	case "ConfigMap":
		data, _ := object["data"].(map[string]any)
		binaryData, _ := object["binaryData"].(map[string]any)
		keys := make([]string, 0, len(data)+len(binaryData))
		for key := range data {
			keys = append(keys, key)
		}
		for key := range binaryData {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			add("ConfigMap keys", strings.Join(keys, ","))
		}
	case "Secret":
		add("Secret type", object["type"])
	case "PersistentVolumeClaim":
		add("PVC storage class", spec["storageClassName"])
		add("PVC access modes", spec["accessModes"])
		add("PVC requested storage", askNestedMap(spec, "resources", "requests")["storage"])
		add("PVC phase", status["phase"])
		add("PVC capacity", askNestedMap(status, "capacity")["storage"])
	case "HorizontalPodAutoscaler":
		add("HPA target", spec["scaleTargetRef"])
		add("HPA minimum replicas", spec["minReplicas"])
		add("HPA maximum replicas", spec["maxReplicas"])
		add("HPA current replicas", status["currentReplicas"])
	case "NetworkPolicy":
		add("NetworkPolicy pod selector", spec["podSelector"])
		add("NetworkPolicy types", spec["policyTypes"])
		add("NetworkPolicy ingress rule count", askSliceLength(spec["ingress"]))
		add("NetworkPolicy egress rule count", askSliceLength(spec["egress"]))
	case "Ingress":
		add("Ingress rules", askIngressSummary(spec["rules"]))
	}
	if _, ok := status["phase"]; ok && kind == "Pod" {
		add("Pod phase", status["phase"])
		if containerStatuses, ok := status["containerStatuses"].([]any); ok {
			for _, raw := range containerStatuses {
				containerStatus, _ := raw.(map[string]any)
				name, _ := containerStatus["name"].(string)
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
				if exitCode, exists := stateValue["exitCode"]; exists {
					stateSummary += ":exit=" + fmt.Sprint(exitCode)
				}
				if name != "" {
					add("Container status", fmt.Sprintf("%s ready=%t restarts=%v state=%s", name, ready, restarts, stateSummary))
				}
			}
		}
	}
	if _, ok := status["readyReplicas"]; ok {
		add("Ready replicas", status["readyReplicas"])
	}
	if _, ok := status["availableReplicas"]; ok {
		add("Available replicas", status["availableReplicas"])
	}
	if conditions, ok := status["conditions"].([]any); ok {
		var summaries []string
		for _, raw := range conditions {
			condition, _ := raw.(map[string]any)
			typeName, _ := condition["type"].(string)
			conditionStatus, _ := condition["status"].(string)
			if typeName != "" && conditionStatus != "" {
				summaries = append(summaries, typeName+"="+conditionStatus)
			}
			if len(summaries) >= 8 {
				break
			}
		}
		if len(summaries) > 0 {
			add("Condition states", strings.Join(summaries, ","))
		}
	}
	return evidence
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
			if number, exists := port["containerPort"]; exists {
				add("Container ports", fmt.Sprintf("%s=%s:%v/%s", name, portName, number, protocol))
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
				add("Container resource "+resourceType, name+"="+askJSON(values))
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

func askJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
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

func askObjectLabels(value any) labels.Set {
	values, _ := value.(map[string]any)
	out := labels.Set{}
	for key, item := range values {
		out[key] = fmt.Sprint(item)
	}
	return out
}

func askInventoryID(namespace, kind, name string) string {
	if namespace == "" {
		namespace = "cluster"
	}
	return "ask/" + namespace + "/" + kind + "/" + name
}

func askFindingIdentity(finding analyzer.Finding) string {
	return strings.ToLower(firstAskValue(finding.Namespace, "cluster") + "/" + finding.ResourceKind + "/" + finding.ResourceName)
}

func mergeAskInventoryFindings(findings, inventory []analyzer.Finding) []analyzer.Finding {
	out := append([]analyzer.Finding(nil), findings...)
	seen := make(map[string]int, len(findings))
	for index, finding := range findings {
		seen[askFindingIdentity(finding)] = index
	}
	for _, finding := range inventory {
		identity := askFindingIdentity(finding)
		if index, exists := seen[identity]; exists {
			out[index].Evidence = mergeAskEvidence(out[index].Evidence, finding.Evidence)
			continue
		}
		seen[identity] = len(out)
		out = append(out, finding)
	}
	return out
}

func mergeAskEvidence(primary, additional []analyzer.Evidence) []analyzer.Evidence {
	out := append([]analyzer.Evidence(nil), primary...)
	seen := make(map[string]bool, len(out))
	for _, item := range out {
		seen[item.Label+"\x00"+item.Value] = true
	}
	for _, item := range additional {
		key := item.Label + "\x00" + item.Value
		if !seen[key] && len(out) < 32 {
			out = append(out, item)
			seen[key] = true
		}
	}
	return out
}

func rankAskFindings(findings []analyzer.Finding, request string, limit int) []analyzer.Finding {
	type rankedFinding struct {
		finding analyzer.Finding
		score   int
	}
	request = strings.ToLower(strings.TrimSpace(request))
	words := strings.FieldsFunc(request, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-')
	})
	ranked := make([]rankedFinding, 0, len(findings))
	for _, finding := range findings {
		name := strings.ToLower(finding.ResourceName)
		kind := strings.ToLower(finding.ResourceKind)
		namespace := strings.ToLower(finding.Namespace)
		status := strings.ToLower(finding.Status)
		score := 0
		if name != "" && strings.Contains(request, name) {
			score += 100
		}
		for _, word := range words {
			if len(word) < 3 {
				continue
			}
			if strings.Contains(name, word) {
				score += 20
			}
			if strings.Contains(kind, word) {
				score += 10
			}
			if strings.Contains(namespace, word) {
				score += 8
			}
			if strings.Contains(status, word) {
				score += 5
			}
		}
		ranked = append(ranked, rankedFinding{finding: finding, score: score})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		left := askFindingIdentity(ranked[i].finding) + "/" + ranked[i].finding.Status + "/" + ranked[i].finding.ID
		right := askFindingIdentity(ranked[j].finding) + "/" + ranked[j].finding.Status + "/" + ranked[j].finding.ID
		return left < right
	})
	if limit <= 0 || limit > len(ranked) {
		limit = len(ranked)
	}
	out := make([]analyzer.Finding, 0, limit)
	for _, item := range ranked[:limit] {
		out = append(out, item.finding)
	}
	return out
}

func askWantsSummary(request string) bool {
	request = strings.ToLower(strings.TrimSpace(request))
	for _, phrase := range []string{"summarize", "summary", "overview", "what is unhealthy", "what's unhealthy", "all issues", "all problems", "anything wrong", "across the cluster", "across namespaces"} {
		if strings.Contains(request, phrase) {
			return true
		}
	}
	return false
}

func askWantsFix(request string) bool {
	words := strings.FieldsFunc(strings.ToLower(request), func(r rune) bool {
		return !(r >= 'a' && r <= 'z')
	})
	for _, word := range words {
		switch word {
		case "fix", "repair", "patch", "remediate", "preview":
			return true
		}
	}
	return false
}

func askRelatedFindings(findings []analyzer.Finding, primary analyzer.Finding, request string, allNS bool) []analyzer.Finding {
	limit := 8
	selected := []analyzer.Finding{primary}
	seen := map[string]bool{primary.ID: true}
	var candidates []analyzer.Finding
	broad := askWantsSummary(request)
	for _, finding := range findings {
		if finding.ID == primary.ID || seen[finding.ID] {
			continue
		}
		if !allNS && finding.Namespace != primary.Namespace {
			continue
		}
		if broad {
			if strings.EqualFold(finding.Status, "Observed") {
				continue
			}
			candidates = append(candidates, finding)
			continue
		}
		if strings.EqualFold(finding.ResourceName, primary.ResourceName) || (finding.PodName != "" && finding.PodName == primary.PodName) || askOwnerChainsOverlap(finding.OwnerChain, primary.OwnerChain) {
			candidates = append(candidates, finding)
		}
	}
	for _, finding := range rankAskFindings(candidates, request, limit-1) {
		if !seen[finding.ID] {
			selected = append(selected, finding)
			seen[finding.ID] = true
		}
	}
	return selected
}

func askOwnerChainsOverlap(left, right []string) bool {
	for _, a := range left {
		for _, b := range right {
			if a == b {
				return true
			}
		}
	}
	return false
}
