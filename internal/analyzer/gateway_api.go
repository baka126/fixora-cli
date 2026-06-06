package analyzer

import (
	"strings"
)

func (a Analyzer) analyzeGatewayAPI(ctx *ScanContext) ([]Finding, error) {
	out := []Finding{}
	services, _ := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "services")
	serviceSet := map[string]bool{}
	servicePorts := map[string]map[int]bool{}
	for _, service := range services {
		key := objectKey(service)
		serviceSet[key] = true
		servicePorts[key] = servicePortSet(service)
	}
	gateways, _ := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "gateways.gateway.networking.k8s.io")
	gatewaySet := map[string]bool{}
	for _, gateway := range gateways {
		gatewaySet[objectKey(gateway)] = true
	}
	for _, target := range []struct {
		resource  string
		namespace string
		allNS     bool
	}{
		{resource: "gatewayclasses.gateway.networking.k8s.io", namespace: "", allNS: true},
		{resource: "gateways.gateway.networking.k8s.io", namespace: a.opts.Namespace, allNS: a.opts.AllNS},
		{resource: "httproutes.gateway.networking.k8s.io", namespace: a.opts.Namespace, allNS: a.opts.AllNS},
	} {
		items, err := ctx.GetResourceItems(target.namespace, target.allNS, target.resource)
		if err != nil {
			continue
		}
		for _, item := range items {
			namespace, name := objectNamespaceName(item)
			kind := strValue(item["kind"])
			if kind == "" {
				kind = target.resource
			}
			for _, condition := range nestedSlice(nestedMap(item, "status"), "conditions") {
				conditionMap, _ := condition.(map[string]any)
				if strValue(conditionMap["status"]) == "False" {
					out = append(out, Finding{
						ID:           keyFor(namespace, kind+"/"+name+"/Condition/"+strValue(conditionMap["type"])),
						Namespace:    namespace,
						ResourceKind: kind,
						ResourceName: name,
						Status:       firstNonEmpty(strValue(conditionMap["reason"]), "ConditionFalse"),
						Severity:     "medium",
						Category:     "networking",
						Summary:      "Gateway API resource reports a false condition.",
						Evidence:     []Evidence{{Label: strValue(conditionMap["type"]), Value: strValue(conditionMap["message"])}},
						GitOps:       gitOpsForObject(item),
						Recommendations: []Recommendation{{
							Title:         "Inspect Gateway API controller status",
							Description:   "Check controller events, listener attachment, route acceptance, backend refs, and ReferenceGrant requirements.",
							PatchType:     "gateway",
							SafeByDefault: false,
						}},
					})
				}
			}
			if strings.EqualFold(kind, "HTTPRoute") {
				for _, parent := range httpRouteParentRefs(item) {
					if parent.Namespace == "" {
						parent.Namespace = namespace
					}
					if parent.Kind == "" || parent.Kind == "Gateway" {
						if !gatewaySet[keyFor(parent.Namespace, parent.Name)] {
							out = append(out, Finding{
								ID:           keyFor(namespace, "HTTPRoute/"+name+"/MissingParent/"+parent.Name),
								Namespace:    namespace,
								ResourceKind: "HTTPRoute",
								ResourceName: name,
								Status:       "MissingParentRef",
								Severity:     "high",
								Category:     "networking",
								Summary:      "HTTPRoute references a parent Gateway that does not exist or is not readable.",
								Evidence:     []Evidence{{Label: "ParentRef", Value: keyFor(parent.Namespace, parent.Name)}},
								GitOps:       gitOpsForObject(item),
								Recommendations: []Recommendation{{
									Title:         "Fix HTTPRoute parent reference",
									Description:   "Restore the referenced Gateway, correct the parentRef namespace/name, or add the required cross-namespace attachment policy.",
									PatchType:     "gateway",
									SafeByDefault: false,
								}},
							})
						}
					}
				}
				for _, backend := range httpRouteBackendRefs(item) {
					if backend.Namespace == "" {
						backend.Namespace = namespace
					}
					if backend.Kind == "" || backend.Kind == "Service" {
						key := keyFor(backend.Namespace, backend.Name)
						if !serviceSet[key] {
							out = append(out, Finding{
								ID:           keyFor(namespace, "HTTPRoute/"+name+"/MissingBackend/"+backend.Name),
								Namespace:    namespace,
								ResourceKind: "HTTPRoute",
								ResourceName: name,
								Status:       "MissingBackendRef",
								Severity:     "high",
								Category:     "networking",
								Summary:      "HTTPRoute references a backend Service that does not exist or is not readable.",
								Evidence:     []Evidence{{Label: "Backend", Value: keyFor(backend.Namespace, backend.Name)}},
								GitOps:       gitOpsForObject(item),
								Recommendations: []Recommendation{{
									Title:         "Fix HTTPRoute backend reference",
									Description:   "Restore the referenced Service or retarget the route after confirming cross-namespace ReferenceGrant requirements.",
									PatchType:     "gateway",
									SafeByDefault: false,
								}},
							})
							continue
						}
						if backend.Port > 0 && !servicePorts[key][backend.Port] {
							out = append(out, Finding{
								ID:           keyFor(namespace, "HTTPRoute/"+name+"/BackendPortMismatch/"+backend.Name),
								Namespace:    namespace,
								ResourceKind: "HTTPRoute",
								ResourceName: name,
								Status:       "BackendPortMismatch",
								Severity:     "high",
								Category:     "networking",
								Summary:      "HTTPRoute backend port does not exist on the referenced Service.",
								Evidence:     []Evidence{{Label: "Backend", Value: key + " port " + strValue(backend.Port)}},
								GitOps:       gitOpsForObject(item),
								Recommendations: []Recommendation{{
									Title:         "Align HTTPRoute backend port",
									Description:   "Update the route backend port or Service port after checking the serving container and targetPort mapping.",
									PatchType:     "gateway",
									SafeByDefault: false,
								}},
							})
						}
					}
				}
			}
		}
	}
	return out, nil
}

func servicePortSet(service map[string]any) map[int]bool {
	out := map[int]bool{}
	for _, port := range nestedSlice(nestedMap(service, "spec"), "ports") {
		portMap, _ := port.(map[string]any)
		if value := intValue(portMap["port"]); value > 0 {
			out[value] = true
		}
	}
	return out
}
