package analyzer

func (a Analyzer) analyzeIngressBackends(ctx *ScanContext) ([]Finding, error) {
	ingresses, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "ingresses")
	if err != nil {
		return nil, err
	}
	services, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "services")
	if err != nil {
		return nil, err
	}
	serviceSet := map[string]bool{}
	for _, service := range services {
		serviceSet[objectKey(service)] = true
	}
	out := []Finding{}
	for _, ingress := range ingresses {
		namespace, name := objectNamespaceName(ingress)
		spec := nestedMap(ingress, "spec")
		if strValue(spec["ingressClassName"]) == "" {
			out = append(out, Finding{
				ID:           keyFor(namespace, "Ingress/"+name+"/MissingClass"),
				Namespace:    namespace,
				ResourceKind: "Ingress",
				ResourceName: name,
				Status:       "MissingIngressClass",
				Severity:     "low",
				Category:     "networking",
				Summary:      "Ingress does not declare spec.ingressClassName.",
				Evidence:     []Evidence{{Label: "IngressClass", Value: "empty"}},
				GitOps:       gitOpsForObject(ingress),
				Recommendations: []Recommendation{{
					Title:         "Pin the intended ingress controller",
					Description:   "Set spec.ingressClassName in the GitOps source so routing ownership is explicit across clusters.",
					PatchType:     "ingress",
					SafeByDefault: true,
				}},
			})
		}
		for _, backend := range ingressBackendServices(spec) {
			if backend == "" || serviceSet[keyFor(namespace, backend)] {
				continue
			}
			out = append(out, Finding{
				ID:           keyFor(namespace, "Ingress/"+name+"/MissingService/"+backend),
				Namespace:    namespace,
				ResourceKind: "Ingress",
				ResourceName: name,
				Status:       "MissingBackendService",
				Severity:     "high",
				Category:     "networking",
				Summary:      "Ingress references a backend Service that does not exist in the same namespace.",
				Evidence:     []Evidence{{Label: "Missing service", Value: backend}},
				GitOps:       gitOpsForObject(ingress),
				Recommendations: []Recommendation{{
					Title:         "Restore or retarget the backend Service",
					Description:   "Create the expected Service or update the Ingress backend reference after confirming the intended workload and release source.",
					PatchType:     "ingress",
					SafeByDefault: false,
				}},
			})
		}
		for _, secretName := range ingressTLSSecretNames(spec) {
			state := a.objectNameState(ctx, namespace, "secret", secretName)
			if secretName == "" || state.Exists {
				continue
			}
			status := "MissingTLSSecret"
			summary := "Ingress references a TLS Secret that does not exist."
			if state.Forbidden {
				status = "TLSSecretUnreadable"
				summary = "Ingress references a TLS Secret that exists but is not readable with current RBAC."
			}
			out = append(out, Finding{
				ID:           keyFor(namespace, "Ingress/"+name+"/"+status+"/"+secretName),
				Namespace:    namespace,
				ResourceKind: "Ingress",
				ResourceName: name,
				Status:       status,
				Severity:     "high",
				Category:     "networking",
				Summary:      summary,
				Evidence:     []Evidence{{Label: "TLS secret", Value: secretName}, {Label: "State", Value: state.Message}},
				GitOps:       gitOpsForObject(ingress),
				Recommendations: []Recommendation{{
					Title:         "Restore the TLS Secret reference",
					Description:   "If missing, restore the Secret through the certificate workflow. If unreadable, grant read diagnostics RBAC before generating fixes. Fixora does not read Secret values.",
					PatchType:     "ingress",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}
