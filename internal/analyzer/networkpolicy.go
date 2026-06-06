package analyzer

func (a Analyzer) analyzeNetworkPolicies(ctx *ScanContext) ([]Finding, error) {
	policies, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "networkpolicies")
	if err != nil {
		return nil, err
	}
	pods, podErr := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "pods")

	out := []Finding{}
	for _, policy := range policies {
		namespace, name := objectNamespaceName(policy)
		selector := nestedMap(nestedMap(policy, "spec"), "podSelector")
		matchLabels := stringMap(selector["matchLabels"])
		matchExpressions := nestedSlice(selector, "matchExpressions")
		if len(matchLabels) == 0 && len(matchExpressions) == 0 {
			out = append(out, networkPolicyFinding(policy, "EmptyPodSelector", "medium", "NetworkPolicy podSelector selects all pods in its namespace.", "An empty podSelector applies this policy to every pod in "+namespace+"."))
			continue
		}
		if podErr != nil {
			continue
		}
		if !networkPolicySelectorMatchesPods(namespace, matchLabels, pods) {
			out = append(out, networkPolicyFinding(policy, "NoSelectedPods", "low", "NetworkPolicy does not select any observable pods.", "No pods matched selector "+compactStringMap(matchLabels)+" for "+keyFor(namespace, name)+"."))
		}
	}
	if podErr != nil && len(policies) > 0 {
		out = append(out, Finding{
			ID:           "cluster/NetworkPolicy/SelectorCheckSkipped",
			ResourceKind: "NetworkPolicy",
			ResourceName: "selector",
			Status:       "PodListUnavailable",
			Severity:     "low",
			Category:     "networking",
			Summary:      "NetworkPolicy selector checks could not read pods.",
			Evidence:     []Evidence{{Label: "Pod list error", Value: podErr.Error()}},
			Recommendations: []Recommendation{{
				Title:         "Grant pod read access before changing policies",
				Description:   "Fix pod list permissions or scan a narrower namespace before changing NetworkPolicy selectors.",
				PatchType:     "networkpolicy",
				SafeByDefault: false,
			}},
		})
	}
	return out, nil
}

func networkPolicyFinding(policy map[string]any, status, severity, summary, evidence string) Finding {
	namespace, name := objectNamespaceName(policy)
	return Finding{
		ID:           keyFor(namespace, "NetworkPolicy/"+name+"/"+status),
		Namespace:    namespace,
		ResourceKind: "NetworkPolicy",
		ResourceName: name,
		Status:       status,
		Severity:     severity,
		Category:     "networking",
		Summary:      summary,
		Evidence:     []Evidence{{Label: "NetworkPolicy", Value: evidence}},
		GitOps:       gitOpsForObject(policy),
		Recommendations: []Recommendation{{
			Title:         "Review traffic isolation intent",
			Description:   "Confirm intended pod selectors, namespace selectors, and ingress/egress direction before changing connectivity policy.",
			PatchType:     "networkpolicy",
			SafeByDefault: false,
		}},
	}
}

func networkPolicySelectorMatchesPods(namespace string, selector map[string]string, pods []map[string]any) bool {
	if len(selector) == 0 {
		return false
	}
	for _, pod := range pods {
		podNamespace, _ := objectNamespaceName(pod)
		if podNamespace != namespace {
			continue
		}
		labels, _ := objectLabelsAnnotations(pod)
		if labelsMatch(selector, labels) {
			return true
		}
	}
	return false
}
