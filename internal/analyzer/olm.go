package analyzer

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

func (a Analyzer) analyzeOLM(ctx *ScanContext) ([]Finding, error) {
	out := []Finding{}
	var errs []string

	for _, check := range []struct {
		resource string
		kind     string
		run      func(map[string]any) []Finding
	}{
		{resource: "catalogsources.operators.coreos.com", kind: "CatalogSource", run: catalogSourceFindings},
		{resource: "subscriptions.operators.coreos.com", kind: "Subscription", run: subscriptionFindings},
		{resource: "installplans.operators.coreos.com", kind: "InstallPlan", run: installPlanFindings},
		{resource: "clusterserviceversions.operators.coreos.com", kind: "ClusterServiceVersion", run: clusterServiceVersionFindings},
	} {
		items, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, check.resource)
		if err != nil {
			errs = append(errs, check.kind+": "+err.Error())
			continue
		}
		for _, item := range items {
			out = append(out, check.run(item)...)
		}
	}

	operatorGroups, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "operatorgroups.operators.coreos.com")
	if err != nil {
		errs = append(errs, "OperatorGroup: "+err.Error())
	} else {
		out = append(out, operatorGroupFindings(operatorGroups)...)
	}

	for _, check := range []struct {
		resource string
		kind     string
		run      func(map[string]any) []Finding
	}{
		{resource: "clustercatalogs.olm.operatorframework.io", kind: "ClusterCatalog", run: clusterCatalogFindings},
		{resource: "clusterextensions.olm.operatorframework.io", kind: "ClusterExtension", run: clusterExtensionFindings},
	} {
		items, err := ctx.GetResourceItems("", true, check.resource)
		if err != nil {
			errs = append(errs, check.kind+": "+err.Error())
			continue
		}
		for _, item := range items {
			out = append(out, check.run(item)...)
		}
	}

	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	return out, nil
}

func catalogSourceFindings(item map[string]any) []Finding {
	state := strValue(nestedMap(nestedMap(item, "status"), "connectionState")["lastObservedState"])
	if state == "" || strings.EqualFold(state, "READY") {
		return nil
	}
	address := strValue(nestedMap(nestedMap(item, "status"), "connectionState")["address"])
	return []Finding{olmFinding(item, "CatalogSource", "CatalogSourceConnectionUnhealthy", "high", "CatalogSource connection is not READY.", "connectionState="+state+" address="+address)}
}

func subscriptionFindings(item map[string]any) []Finding {
	state := strValue(nestedMap(item, "status")["state"])
	if state != "" && state != "UpgradePending" && state != "UpgradeAvailable" {
		return nil
	}
	evidence := "state=" + fmt.Sprintf("%q", state)
	if condition := pickWorstCondition(nestedSlice(nestedMap(item, "status"), "conditions")); condition != "" {
		evidence += "; " + condition
	}
	return []Finding{olmFinding(item, "Subscription", "SubscriptionNotCurrent", "medium", "OLM Subscription is not at the latest installed state.", evidence)}
}

func installPlanFindings(item map[string]any) []Finding {
	phase := strValue(nestedMap(item, "status")["phase"])
	if phase == "" || phase == "Complete" {
		return nil
	}
	evidence := "phase=" + fmt.Sprintf("%q", phase)
	if condition := pickWorstCondition(nestedSlice(nestedMap(item, "status"), "conditions")); condition != "" {
		evidence += "; " + condition
	}
	return []Finding{olmFinding(item, "InstallPlan", "InstallPlanIncomplete", "medium", "OLM InstallPlan is not complete.", evidence)}
}

func clusterServiceVersionFindings(item map[string]any) []Finding {
	phase := strValue(nestedMap(item, "status")["phase"])
	if phase == "" || phase == "Succeeded" {
		return nil
	}
	evidence := "phase=" + fmt.Sprintf("%q", phase)
	if condition := pickWorstCondition(nestedSlice(nestedMap(item, "status"), "conditions")); condition != "" {
		evidence += "; " + condition
	}
	return []Finding{olmFinding(item, "ClusterServiceVersion", "CSVNotSucceeded", "high", "OLM ClusterServiceVersion has not succeeded.", evidence)}
}

func operatorGroupFindings(items []map[string]any) []Finding {
	countByNS := map[string]int{}
	for _, item := range items {
		namespace, _ := objectNamespaceName(item)
		countByNS[namespace]++
	}
	out := []Finding{}
	for namespace, count := range countByNS {
		if count <= 1 {
			continue
		}
		out = append(out, Finding{
			ID:           keyFor(namespace, "OperatorGroup/Multiple"),
			Namespace:    namespace,
			ResourceKind: "OperatorGroup",
			ResourceName: "multiple",
			Status:       "MultipleOperatorGroups",
			Severity:     "high",
			Category:     "operator",
			Summary:      "Namespace has multiple OperatorGroups, which can break CSV resolution.",
			Evidence:     []Evidence{{Label: "OperatorGroups", Value: fmt.Sprintf("%d OperatorGroups in namespace", count)}},
			Recommendations: []Recommendation{{
				Title:         "Keep one OperatorGroup per namespace",
				Description:   "Review OLM ownership and remove or consolidate duplicate OperatorGroups before retrying operator installation.",
				PatchType:     "operatorgroup",
				SafeByDefault: false,
			}},
		})
	}
	return out
}

func clusterCatalogFindings(item map[string]any) []Finding {
	failures := []string{}
	ref := strValue(nestedMap(nestedMap(nestedMap(nestedMap(item, "spec"), "source"), "image"), "ref"))
	if ref != "" && !isValidImageRef(ref) {
		failures = append(failures, "invalid spec.source.image.ref="+ref)
	}
	resolved := strValue(nestedMap(nestedMap(nestedMap(nestedMap(item, "status"), "resolvedSource"), "image"), "ref"))
	if resolved != "" && !regexp.MustCompile(`@sha256:[a-f0-9]{64}$`).MatchString(resolved) {
		failures = append(failures, "status.resolvedSource.image.ref is not digest-pinned")
	}
	for _, condition := range nestedSlice(nestedMap(item, "status"), "conditions") {
		conditionMap, _ := condition.(map[string]any)
		typ := strValue(conditionMap["type"])
		status := strValue(conditionMap["status"])
		reason := strValue(conditionMap["reason"])
		if typ == "Serving" && status != "True" {
			failures = append(failures, "Serving="+status+" "+reason+": "+strValue(conditionMap["message"]))
		}
		if typ == "Progressing" && reason != "Succeeded" {
			failures = append(failures, "Progressing reason="+reason+": "+strValue(conditionMap["message"]))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return []Finding{olmFinding(item, "ClusterCatalog", "ClusterCatalogUnhealthy", "medium", "OLMv1 ClusterCatalog is not serving cleanly.", strings.Join(failures, "; "))}
}

func clusterExtensionFindings(item map[string]any) []Finding {
	failures := []string{}
	source := nestedMap(nestedMap(item, "spec"), "source")
	if sourceType := strValue(source["sourceType"]); sourceType != "" && sourceType != "Catalog" {
		failures = append(failures, "spec.source.sourceType must be Catalog")
	}
	catalog := nestedMap(source, "catalog")
	if policy := strValue(catalog["upgradeConstraintPolicy"]); policy != "" && policy != "CatalogProvided" && policy != "SelfCertified" {
		failures = append(failures, "invalid upgradeConstraintPolicy="+policy)
	}
	for _, condition := range nestedSlice(nestedMap(item, "status"), "conditions") {
		conditionMap, _ := condition.(map[string]any)
		typ := strValue(conditionMap["type"])
		status := strValue(conditionMap["status"])
		reason := strValue(conditionMap["reason"])
		if typ == "Installed" && status != "True" {
			failures = append(failures, "Installed="+status+" "+reason+": "+strValue(conditionMap["message"]))
		}
		if typ == "Progressing" && reason != "Succeeded" {
			failures = append(failures, "Progressing reason="+reason+": "+strValue(conditionMap["message"]))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return []Finding{olmFinding(item, "ClusterExtension", "ClusterExtensionUnhealthy", "high", "OLMv1 ClusterExtension is not installed cleanly.", strings.Join(failures, "; "))}
}

func olmFinding(item map[string]any, kind, status, severity, summary, evidence string) Finding {
	namespace, name := objectNamespaceName(item)
	return Finding{
		ID:           keyFor(namespace, kind+"/"+name+"/"+status),
		Namespace:    namespace,
		ResourceKind: kind,
		ResourceName: name,
		Status:       status,
		Severity:     severity,
		Category:     "operator",
		Summary:      summary,
		Evidence:     []Evidence{{Label: kind, Value: evidence}},
		GitOps:       gitOpsForObject(item),
		Recommendations: []Recommendation{{
			Title:         "Inspect operator lifecycle state",
			Description:   "Check OLM conditions, catalog availability, install plan approval, operator pods, and related events before forcing upgrades or deleting resources.",
			PatchType:     strings.ToLower(kind),
			SafeByDefault: false,
		}},
	}
}

func pickWorstCondition(conditions []any) string {
	for _, condition := range conditions {
		conditionMap, _ := condition.(map[string]any)
		if conditionMap == nil {
			continue
		}
		if strValue(conditionMap["status"]) == "True" {
			continue
		}
		reason := strValue(conditionMap["reason"])
		message := strValue(conditionMap["message"])
		if reason == "" && message == "" {
			continue
		}
		if reason != "" && message != "" {
			return reason + ": " + message
		}
		return reason + message
	}
	return ""
}

func isValidImageRef(ref string) bool {
	pattern := `^([a-zA-Z0-9\-.]+(?::[0-9]+)?/)?([a-z0-9]+(?:[._\-/][a-z0-9]+)*)(:[\w][\w.-]{0,127})?(?:@sha256:[a-f0-9]{64})?$`
	return regexp.MustCompile(pattern).MatchString(ref)
}
