package analyzer

func (a Analyzer) analyzeReplicaSets(ctx *ScanContext) ([]Finding, error) {
	replicasets, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "replicasets")
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for _, rs := range replicasets {
		namespace, name := objectNamespaceName(rs)
		status := nestedMap(rs, "status")

		replicas := intValue(status["replicas"])

		if replicas == 0 {
			conditions := nestedSlice(status, "conditions")
			for _, cond := range conditions {
				c, ok := cond.(map[string]any)
				if !ok {
					continue
				}
				if strValue(c["type"]) == "ReplicaFailure" && strValue(c["reason"]) == "FailedCreate" {
					out = append(out, Finding{
						ID:           keyFor(namespace, "ReplicaSet/"+name+"/FailedCreate"),
						Namespace:    namespace,
						ResourceKind: "ReplicaSet",
						ResourceName: name,
						Status:       "FailedCreate",
						Severity:     "high",
						Category:     "workload",
						Summary:      strValue(c["message"]),
						Evidence: []Evidence{
							{Label: "Replicas", Value: "0"},
						},
						GitOps: gitOpsForObject(rs),
						Recommendations: []Recommendation{{
							Title:         "Inspect replicaset events",
							Description:   "Check if there are RBAC issues, missing secrets, or invalid pod specs preventing creation.",
							PatchType:     "replicaset",
							SafeByDefault: false,
						}},
					})
				}
			}
		}
	}
	return out, nil
}
