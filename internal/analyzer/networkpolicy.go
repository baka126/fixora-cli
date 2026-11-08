package analyzer

import (
	"encoding/json"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func (a Analyzer) analyzeNetworkPolicies(ctx *ScanContext) ([]Finding, error) {
	policies, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "networkpolicies")
	if err != nil {
		return nil, err
	}
	pods, podErr := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "pods")

	out := []Finding{}
	selectors := make([]labels.Selector, len(policies))
	selectorValid := make([]bool, len(policies))
	for i, policy := range policies {
		namespace, name := objectNamespaceName(policy)
		selector := nestedMap(nestedMap(policy, "spec"), "podSelector")
		matchLabels := stringMap(selector["matchLabels"])
		matchExpressions := nestedSlice(selector, "matchExpressions")
		if len(matchLabels) == 0 && len(matchExpressions) == 0 {
			out = append(out, networkPolicyFinding(policy, "EmptyPodSelector", "medium", "NetworkPolicy podSelector selects all pods in its namespace.", "An empty podSelector applies this policy to every pod in "+namespace+"."))
			continue
		}
		matcher, selectorErr := networkPolicySelector(selector)
		if selectorErr != nil {
			out = append(out, networkPolicyFinding(policy, "InvalidPodSelector", "high", "NetworkPolicy podSelector is malformed.", "The selector cannot be evaluated with Kubernetes label-selector semantics."))
			continue
		}
		selectors[i], selectorValid[i] = matcher, true
		if podErr != nil {
			continue
		}
		if !networkPolicySelectorMatchesPods(namespace, matcher, pods) {
			selectorText := compactStringMap(matchLabels)
			if len(matchExpressions) > 0 {
				selectorText = matcher.String()
			}
			out = append(out, networkPolicyFinding(policy, "NoSelectedPods", "low", "NetworkPolicy does not select any observable pods.", "No pods matched selector "+selectorText+" for "+keyFor(namespace, name)+"."))
		}
	}
	if podErr == nil {
		overlaps := map[int]bool{}
		for _, pod := range pods {
			podNamespace, _ := objectNamespaceName(pod)
			podLabels, _ := objectLabelsAnnotations(pod)
			selected := []int{}
			for i, policy := range policies {
				policyNamespace, _ := objectNamespaceName(policy)
				if podNamespace == policyNamespace && selectorValid[i] && selectors[i].Matches(labels.Set(podLabels)) {
					selected = append(selected, i)
				}
			}
			if len(selected) > 1 {
				for _, i := range selected {
					overlaps[i] = true
				}
			}
		}
		for i, overlapping := range overlaps {
			if overlapping {
				out = append(out, networkPolicyFinding(policies[i], "OverlappingPolicies", "medium", "Multiple NetworkPolicies select the same Pod; allowed traffic is additive.", "This policy shares at least one Pod with another standard NetworkPolicy."))
			}
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

func networkPolicySelector(selector map[string]any) (labels.Selector, error) {
	data, err := json.Marshal(selector)
	if err != nil {
		return nil, err
	}
	var parsed metav1.LabelSelector
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	return metav1.LabelSelectorAsSelector(&parsed)
}

func networkPolicySelectorMatchesPods(namespace string, selector labels.Selector, pods []map[string]any) bool {
	for _, pod := range pods {
		podNamespace, _ := objectNamespaceName(pod)
		if podNamespace != namespace {
			continue
		}
		podLabels, _ := objectLabelsAnnotations(pod)
		if selector.Matches(labels.Set(podLabels)) {
			return true
		}
	}
	return false
}
