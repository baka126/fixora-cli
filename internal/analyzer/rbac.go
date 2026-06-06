package analyzer

import (
	"strings"
)

func (a Analyzer) analyzeRBAC(ctx *ScanContext) ([]Finding, error) {
	out := []Finding{}
	for _, resource := range []string{"roles", "clusterroles"} {
		items, err := ctx.GetResourceItems("", true, resource)
		if err != nil {
			continue
		}
		for _, item := range items {
			namespace, name := objectNamespaceName(item)
			kind := firstNonEmpty(strValue(item["kind"]), "Role")
			for _, rule := range nestedSlice(item, "rules") {
				ruleMap, _ := rule.(map[string]any)
				if sliceHasWildcard(nestedSlice(ruleMap, "verbs")) || sliceHasWildcard(nestedSlice(ruleMap, "resources")) || sliceHasWildcard(nestedSlice(ruleMap, "apiGroups")) {
					out = append(out, Finding{
						ID:           keyFor(namespace, kind+"/"+name+"/WildcardRule"),
						Namespace:    namespace,
						ResourceKind: kind,
						ResourceName: name,
						Status:       "WildcardRBAC",
						Severity:     "high",
						Category:     "security",
						Summary:      "RBAC role contains wildcard permissions.",
						Evidence:     []Evidence{{Label: "Rule", Value: compactMap(ruleMap)}},
						GitOps:       gitOpsForObject(item),
						Recommendations: []Recommendation{{
							Title:         "Reduce RBAC wildcard scope",
							Description:   "Replace wildcard verbs, resources, or API groups with explicit permissions required by the workload.",
							PatchType:     "rbac",
							SafeByDefault: false,
						}},
					})
				}
			}
		}
	}
	for _, resource := range []string{"rolebindings", "clusterrolebindings"} {
		items, err := ctx.GetResourceItems("", true, resource)
		if err != nil {
			continue
		}
		for _, item := range items {
			namespace, name := objectNamespaceName(item)
			roleRef := nestedMap(item, "roleRef")
			if strings.EqualFold(strValue(roleRef["name"]), "cluster-admin") {
				out = append(out, Finding{
					ID:           keyFor(namespace, strValue(item["kind"])+"/"+name+"/ClusterAdmin"),
					Namespace:    namespace,
					ResourceKind: firstNonEmpty(strValue(item["kind"]), "ClusterRoleBinding"),
					ResourceName: name,
					Status:       "ClusterAdminBinding",
					Severity:     "high",
					Category:     "security",
					Summary:      "Binding grants cluster-admin privileges.",
					Evidence:     []Evidence{{Label: "roleRef", Value: compactMap(roleRef)}},
					GitOps:       gitOpsForObject(item),
					Recommendations: []Recommendation{{
						Title:         "Review cluster-admin binding",
						Description:   "Replace broad cluster-admin grants with scoped roles, especially for workload service accounts and automation identities.",
						PatchType:     "rbac",
						SafeByDefault: false,
					}},
				})
			}
		}
	}
	return out, nil
}
