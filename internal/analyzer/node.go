package analyzer

func (a Analyzer) analyzeNodes(ctx *ScanContext) ([]Finding, error) {
	nodes, err := ctx.GetResourceItems("", true, "nodes")
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for _, node := range nodes {
		_, name := objectNamespaceName(node)
		for _, condition := range nestedSlice(nestedMap(node, "status"), "conditions") {
			conditionMap, _ := condition.(map[string]any)
			conditionType := strValue(conditionMap["type"])
			status := strValue(conditionMap["status"])
			if !nodeConditionUnhealthy(conditionType, status) {
				continue
			}
			out = append(out, Finding{
				ID:           "cluster/Node/" + name + "/Condition/" + conditionType,
				ResourceKind: "Node",
				ResourceName: name,
				Status:       firstNonEmpty(strValue(conditionMap["reason"]), conditionType),
				Severity:     severityForNodeCondition(conditionType, status),
				Category:     "node",
				Summary:      "Node reports an unhealthy condition.",
				Evidence: []Evidence{
					{Label: "Condition", Value: conditionType + "=" + status},
					{Label: "Message", Value: strValue(conditionMap["message"])},
				},
				GitOps: gitOpsForObject(node),
				Recommendations: []Recommendation{{
					Title:         "Inspect node health and scheduling impact",
					Description:   "Check kubelet, pressure signals, taints, disk/network health, and affected pending or evicted pods before draining or cordoning.",
					PatchType:     "node",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}

func nodeConditionUnhealthy(conditionType, status string) bool {
	switch conditionType {
	case "Ready":
		return status != "True"
	case "MemoryPressure", "DiskPressure", "PIDPressure", "NetworkUnavailable":
		return status == "True" || status == "Unknown"
	case "EtcdIsVoter":
		return false
	default:
		return status == "True" || status == "Unknown"
	}
}

func severityForNodeCondition(conditionType, status string) string {
	if conditionType == "Ready" || status == "Unknown" {
		return "high"
	}
	return "medium"
}
