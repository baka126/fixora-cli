package analyzer

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func (a Analyzer) runPrecisionAnalyzers(ctx context.Context) ([]Finding, []SkippedCheck) {
	type precisionAnalyzer struct {
		name    string
		aliases []string
		run     func(context.Context) ([]Finding, error)
	}
	analyzers := []precisionAnalyzer{
		{name: "service-endpoints", aliases: []string{"service", "services", "networking"}, run: a.analyzeServiceEndpoints},
		{name: "ingress-backends", aliases: []string{"ingress", "ingresses", "networking"}, run: a.analyzeIngressBackends},
		{name: "hpa-targets", aliases: []string{"hpa", "horizontalpodautoscaler", "autoscaling"}, run: a.analyzeHPATargets},
		{name: "pdb-disruptions", aliases: []string{"pdb", "poddisruptionbudget", "policy"}, run: a.analyzePDBs},
		{name: "webhook-backends", aliases: []string{"webhook", "mutatingwebhook", "validatingwebhook", "policy"}, run: a.analyzeWebhooks},
		{name: "gateway-api", aliases: []string{"gateway", "gatewayclass", "httproute", "networking"}, run: a.analyzeGatewayAPI},
		{name: "rbac-risk", aliases: []string{"rbac", "role", "clusterrole", "rolebinding", "clusterrolebinding", "security"}, run: a.analyzeRBAC},
		{name: "pod-security", aliases: []string{"pod", "pods", "security"}, run: a.analyzePodSecurity},
		{name: "storage", aliases: []string{"storage", "pv", "persistentvolume", "storageclass"}, run: a.analyzeStorage},
	}
	findings := []Finding{}
	skipped := []SkippedCheck{}
	selected := filterSet(a.opts.Filters)
	for _, analyzer := range analyzers {
		if len(selected) > 0 && !matchesAny(selected, analyzer.aliases...) {
			continue
		}
		list, err := analyzer.run(ctx)
		if err != nil {
			skipped = append(skipped, SkippedCheck{Name: analyzer.name, Reason: err.Error()})
			continue
		}
		findings = append(findings, list...)
	}
	return findings, skipped
}

func (a Analyzer) analyzeServiceEndpoints(ctx context.Context) ([]Finding, error) {
	services, err := a.k.GetResourceItems(ctx, a.opts.Namespace, a.opts.AllNS, "services")
	if err != nil {
		return nil, err
	}
	endpoints, err := a.k.GetResourceItems(ctx, a.opts.Namespace, a.opts.AllNS, "endpoints")
	if err != nil {
		return nil, err
	}
	endpointByKey := map[string]map[string]any{}
	for _, endpoint := range endpoints {
		endpointByKey[objectKey(endpoint)] = endpoint
	}
	out := []Finding{}
	for _, service := range services {
		spec := nestedMap(service, "spec")
		if strings.EqualFold(strValue(spec["type"]), "ExternalName") {
			continue
		}
		selector := stringMap(spec["selector"])
		if len(selector) == 0 {
			continue
		}
		namespace, name := objectNamespaceName(service)
		endpoint := endpointByKey[keyFor(namespace, name)]
		subsets := nestedSlice(endpoint, "subsets")
		ready, notReady := endpointAddressCounts(subsets)
		if len(subsets) == 0 || ready == 0 {
			out = append(out, Finding{
				ID:           keyFor(namespace, "Service/"+name+"/NoEndpoints"),
				Namespace:    namespace,
				ResourceKind: "Service",
				ResourceName: name,
				Status:       "NoEndpoints",
				Severity:     "high",
				Category:     "networking",
				Summary:      "Service selector currently resolves to no ready endpoints.",
				Evidence: []Evidence{
					{Label: "Selector", Value: compactStringMap(selector)},
					{Label: "Ready endpoints", Value: fmt.Sprint(ready)},
				},
				GitOps: gitOpsForObject(service),
				Recommendations: []Recommendation{{
					Title:         "Repair service backend selection",
					Description:   "Verify the selector labels match ready pods, then check readiness probes, targetPort, and rollout health before changing traffic.",
					PatchType:     "service",
					SafeByDefault: false,
				}},
			})
		}
		if notReady > 0 {
			out = append(out, Finding{
				ID:           keyFor(namespace, "Service/"+name+"/NotReadyEndpoints"),
				Namespace:    namespace,
				ResourceKind: "Service",
				ResourceName: name,
				Status:       "NotReadyEndpoints",
				Severity:     "medium",
				Category:     "networking",
				Summary:      "Service has backend endpoints that are not ready.",
				Evidence: []Evidence{
					{Label: "Ready endpoints", Value: fmt.Sprint(ready)},
					{Label: "Not-ready endpoints", Value: fmt.Sprint(notReady)},
				},
				GitOps: gitOpsForObject(service),
				Recommendations: []Recommendation{{
					Title:         "Inspect backend pod readiness",
					Description:   "Check pod conditions, readiness probe failures, recent events, and deployment rollout state for the selected backend pods.",
					PatchType:     "readiness",
					SafeByDefault: true,
				}},
			})
		}
	}
	return out, nil
}

func (a Analyzer) analyzeIngressBackends(ctx context.Context) ([]Finding, error) {
	ingresses, err := a.k.GetResourceItems(ctx, a.opts.Namespace, a.opts.AllNS, "ingresses")
	if err != nil {
		return nil, err
	}
	services, err := a.k.GetResourceItems(ctx, a.opts.Namespace, a.opts.AllNS, "services")
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
			if secretName == "" || a.objectNameExists(ctx, namespace, "secret", secretName) {
				continue
			}
			out = append(out, Finding{
				ID:           keyFor(namespace, "Ingress/"+name+"/MissingTLSSecret/"+secretName),
				Namespace:    namespace,
				ResourceKind: "Ingress",
				ResourceName: name,
				Status:       "MissingTLSSecret",
				Severity:     "high",
				Category:     "networking",
				Summary:      "Ingress references a TLS Secret that does not exist or is not readable.",
				Evidence:     []Evidence{{Label: "TLS secret", Value: secretName}},
				GitOps:       gitOpsForObject(ingress),
				Recommendations: []Recommendation{{
					Title:         "Restore the TLS Secret reference",
					Description:   "Create the expected Secret through the cluster's certificate workflow or update the Ingress TLS reference. Fixora checks only object existence and does not read Secret values.",
					PatchType:     "ingress",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}

func (a Analyzer) analyzeHPATargets(ctx context.Context) ([]Finding, error) {
	hpas, err := a.k.GetResourceItems(ctx, a.opts.Namespace, a.opts.AllNS, "hpa")
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for _, hpa := range hpas {
		namespace, name := objectNamespaceName(hpa)
		spec := nestedMap(hpa, "spec")
		target := nestedMap(spec, "scaleTargetRef")
		targetKind, targetName := strValue(target["kind"]), strValue(target["name"])
		if targetKind == "" || targetName == "" {
			out = append(out, Finding{
				ID:           keyFor(namespace, "HPA/"+name+"/MissingTargetRef"),
				Namespace:    namespace,
				ResourceKind: "HorizontalPodAutoscaler",
				ResourceName: name,
				Status:       "MissingScaleTargetRef",
				Severity:     "high",
				Category:     "autoscaling",
				Summary:      "HPA does not have a complete scaleTargetRef.",
				Evidence:     []Evidence{{Label: "scaleTargetRef", Value: compactMap(target)}},
				GitOps:       gitOpsForObject(hpa),
				Recommendations: []Recommendation{{
					Title:         "Set a valid scale target",
					Description:   "Point the HPA at an existing scalable workload such as Deployment, StatefulSet, or ReplicaSet.",
					PatchType:     "hpa",
					SafeByDefault: false,
				}},
			})
			continue
		}
		targetResource := strings.ToLower(targetKind) + "/" + targetName
		targetObj, targetErr := a.k.GetResource(ctx, namespace, targetResource)
		if targetErr != nil {
			out = append(out, Finding{
				ID:           keyFor(namespace, "HPA/"+name+"/MissingTarget/"+targetKind+"/"+targetName),
				Namespace:    namespace,
				ResourceKind: "HorizontalPodAutoscaler",
				ResourceName: name,
				Status:       "MissingScaleTarget",
				Severity:     "high",
				Category:     "autoscaling",
				Summary:      "HPA references a scale target that could not be read.",
				Evidence: []Evidence{
					{Label: "Target", Value: targetKind + "/" + targetName},
					{Label: "Error", Value: targetErr.Error()},
				},
				GitOps: gitOpsForObject(hpa),
				Recommendations: []Recommendation{{
					Title:         "Fix or restore the autoscale target",
					Description:   "Confirm the target kind, name, namespace, and API availability before the HPA is allowed to drive scaling decisions.",
					PatchType:     "hpa",
					SafeByDefault: false,
				}},
			})
			continue
		}
		for _, metric := range hpaResourceMetricNames(spec) {
			missing := containersMissingResourceRequest(targetObj, metric)
			if len(missing) == 0 {
				continue
			}
			out = append(out, Finding{
				ID:           keyFor(namespace, "HPA/"+name+"/MissingRequests/"+metric),
				Namespace:    namespace,
				ResourceKind: "HorizontalPodAutoscaler",
				ResourceName: name,
				Status:       "MissingResourceRequests",
				Severity:     "medium",
				Category:     "autoscaling",
				Summary:      "HPA uses a resource metric but target containers are missing matching resource requests.",
				Evidence: []Evidence{
					{Label: "Metric", Value: metric},
					{Label: "Containers", Value: strings.Join(missing, ", ")},
				},
				GitOps: gitOpsForObject(hpa),
				Recommendations: []Recommendation{{
					Title:         "Add resource requests before relying on HPA",
					Description:   "Set realistic container requests in the workload source so utilization-based scaling has stable inputs.",
					PatchType:     "resources",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}

func (a Analyzer) analyzePodSecurity(ctx context.Context) ([]Finding, error) {
	pods, err := a.k.GetResourceItems(ctx, a.opts.Namespace, a.opts.AllNS, "pods")
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for _, pod := range pods {
		namespace, name := objectNamespaceName(pod)
		spec := nestedMap(pod, "spec")
		if boolValue(spec["hostNetwork"]) || boolValue(spec["hostPID"]) || boolValue(spec["hostIPC"]) {
			out = append(out, podSecurityFinding(pod, "HostNamespaceEnabled", "high", "Pod uses host networking, PID, or IPC namespaces.", "Move host namespace access behind a reviewed exception and prefer standard pod networking where possible."))
		}
		serviceAccount := firstNonEmpty(strValue(spec["serviceAccountName"]), strValue(spec["serviceAccount"]))
		if serviceAccount == "" || serviceAccount == "default" {
			out = append(out, podSecurityFinding(pod, "DefaultServiceAccount", "medium", "Pod is running with the default service account.", "Use a purpose-built service account with the narrowest RBAC required by the workload."))
		}
		for _, container := range append(nestedSlice(spec, "initContainers"), nestedSlice(spec, "containers")...) {
			containerMap, _ := container.(map[string]any)
			containerName := strValue(containerMap["name"])
			securityContext := nestedMap(containerMap, "securityContext")
			if boolValue(securityContext["privileged"]) {
				out = append(out, podSecurityFinding(pod, "PrivilegedContainer/"+containerName, "high", "Container is running privileged.", "Remove privileged mode or isolate it behind a reviewed, time-bound operational exception."))
			}
			if securityContext["runAsNonRoot"] == nil || !boolValue(securityContext["runAsNonRoot"]) {
				out = append(out, podSecurityFinding(pod, "RunAsNonRootMissing/"+containerName, "low", "Container does not explicitly require runAsNonRoot.", "Set runAsNonRoot and a non-root runAsUser in the workload source where the image supports it."))
			}
		}
		_ = namespace
		_ = name
	}
	return out, nil
}

func (a Analyzer) analyzePDBs(ctx context.Context) ([]Finding, error) {
	pdbs, err := a.k.GetResourceItems(ctx, a.opts.Namespace, a.opts.AllNS, "pdb")
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for _, pdb := range pdbs {
		namespace, name := objectNamespaceName(pdb)
		status := nestedMap(pdb, "status")
		current, desired := intValue(status["currentHealthy"]), intValue(status["desiredHealthy"])
		disruptions := intValue(status["disruptionsAllowed"])
		expected := intValue(status["expectedPods"])
		if expected == 0 || current < desired || disruptions == 0 && desired > 0 {
			severity := "medium"
			if current < desired {
				severity = "high"
			}
			out = append(out, Finding{
				ID:           keyFor(namespace, "PodDisruptionBudget/"+name+"/Unavailable"),
				Namespace:    namespace,
				ResourceKind: "PodDisruptionBudget",
				ResourceName: name,
				Status:       "DisruptionsBlocked",
				Severity:     severity,
				Category:     "policy",
				Summary:      "PodDisruptionBudget may block voluntary disruption or does not match healthy pods.",
				Evidence: []Evidence{
					{Label: "currentHealthy", Value: fmt.Sprint(current)},
					{Label: "desiredHealthy", Value: fmt.Sprint(desired)},
					{Label: "disruptionsAllowed", Value: fmt.Sprint(disruptions)},
					{Label: "expectedPods", Value: fmt.Sprint(expected)},
				},
				GitOps: gitOpsForObject(pdb),
				Recommendations: []Recommendation{{
					Title:         "Check PDB selector and workload health",
					Description:   "Confirm the PDB selector matches the intended pods, then fix unavailable replicas before relaxing disruption policy.",
					PatchType:     "pdb",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}

func (a Analyzer) analyzeWebhooks(ctx context.Context) ([]Finding, error) {
	out := []Finding{}
	for _, resource := range []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"} {
		items, err := a.k.GetResourceItems(ctx, "", true, resource)
		if err != nil {
			continue
		}
		kind := "MutatingWebhookConfiguration"
		if strings.HasPrefix(resource, "validating") {
			kind = "ValidatingWebhookConfiguration"
		}
		for _, cfg := range items {
			_, name := objectNamespaceName(cfg)
			for _, webhook := range nestedSlice(cfg, "webhooks") {
				webhookMap, _ := webhook.(map[string]any)
				webhookName := strValue(webhookMap["name"])
				clientConfig := nestedMap(webhookMap, "clientConfig")
				service := nestedMap(clientConfig, "service")
				serviceName, serviceNS := strValue(service["name"]), strValue(service["namespace"])
				if serviceName != "" && !a.objectNameExists(ctx, serviceNS, "service", serviceName) {
					out = append(out, clusterFinding(kind, name, "MissingWebhookService/"+webhookName, "high", "policy", "Admission webhook references a Service that does not exist or is not readable.", []Evidence{{Label: "Webhook", Value: webhookName}, {Label: "Service", Value: keyFor(serviceNS, serviceName)}}))
				}
				if seconds := intValue(webhookMap["timeoutSeconds"]); seconds > 10 {
					out = append(out, clusterFinding(kind, name, "HighWebhookTimeout/"+webhookName, "medium", "policy", "Admission webhook has a high timeout and can slow or block API writes during incidents.", []Evidence{{Label: "Webhook", Value: webhookName}, {Label: "timeoutSeconds", Value: fmt.Sprint(seconds)}}))
				}
				if failurePolicy := strValue(webhookMap["failurePolicy"]); strings.EqualFold(failurePolicy, "Fail") {
					out = append(out, clusterFinding(kind, name, "FailClosedWebhook/"+webhookName, "low", "policy", "Admission webhook fails closed; this can block remediation when the webhook backend is unhealthy.", []Evidence{{Label: "Webhook", Value: webhookName}, {Label: "failurePolicy", Value: failurePolicy}}))
				}
			}
		}
	}
	return out, nil
}

func (a Analyzer) analyzeGatewayAPI(ctx context.Context) ([]Finding, error) {
	out := []Finding{}
	services, _ := a.k.GetResourceItems(ctx, a.opts.Namespace, a.opts.AllNS, "services")
	serviceSet := map[string]bool{}
	for _, service := range services {
		serviceSet[objectKey(service)] = true
	}
	for _, resource := range []string{"gatewayclasses.gateway.networking.k8s.io", "gateways.gateway.networking.k8s.io", "httproutes.gateway.networking.k8s.io"} {
		items, err := a.k.GetResourceItems(ctx, a.opts.Namespace, a.opts.AllNS, resource)
		if err != nil {
			continue
		}
		for _, item := range items {
			namespace, name := objectNamespaceName(item)
			kind := strValue(item["kind"])
			if kind == "" {
				kind = resource
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
				for _, backend := range httpRouteBackendRefs(item) {
					if backend.Namespace == "" {
						backend.Namespace = namespace
					}
					if backend.Kind == "" || backend.Kind == "Service" {
						if !serviceSet[keyFor(backend.Namespace, backend.Name)] {
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
						}
					}
				}
			}
		}
	}
	return out, nil
}

func (a Analyzer) analyzeRBAC(ctx context.Context) ([]Finding, error) {
	out := []Finding{}
	for _, resource := range []string{"roles", "clusterroles"} {
		items, err := a.k.GetResourceItems(ctx, "", true, resource)
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
		items, err := a.k.GetResourceItems(ctx, "", true, resource)
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

func (a Analyzer) analyzeStorage(ctx context.Context) ([]Finding, error) {
	out := []Finding{}
	pvs, pvErr := a.k.GetResourceItems(ctx, "", true, "pv")
	if pvErr == nil {
		for _, pv := range pvs {
			_, name := objectNamespaceName(pv)
			phase := strValue(nestedMap(pv, "status")["phase"])
			if phase == "Released" || phase == "Failed" {
				out = append(out, Finding{
					ID:           "cluster/PersistentVolume/" + name + "/" + phase,
					ResourceKind: "PersistentVolume",
					ResourceName: name,
					Status:       phase,
					Severity:     "medium",
					Category:     "storage",
					Summary:      "PersistentVolume is not available for healthy binding.",
					Evidence:     []Evidence{{Label: "Phase", Value: phase}},
					GitOps:       gitOpsForObject(pv),
					Recommendations: []Recommendation{{
						Title:         "Review reclaim and binding state",
						Description:   "Check the claimRef, reclaim policy, provisioner events, and data-retention requirements before deleting or recycling storage.",
						PatchType:     "storage",
						SafeByDefault: false,
					}},
				})
			}
		}
	}
	storageClasses, scErr := a.k.GetResourceItems(ctx, "", true, "storageclasses")
	if scErr == nil {
		defaults := []string{}
		for _, sc := range storageClasses {
			_, name := objectNamespaceName(sc)
			_, annotations := objectLabelsAnnotations(sc)
			if annotations["storageclass.kubernetes.io/is-default-class"] == "true" || annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true" {
				defaults = append(defaults, name)
			}
		}
		sort.Strings(defaults)
		if len(defaults) > 1 {
			out = append(out, Finding{
				ID:           "cluster/StorageClass/MultipleDefaults",
				ResourceKind: "StorageClass",
				ResourceName: strings.Join(defaults, ","),
				Status:       "MultipleDefaultStorageClasses",
				Severity:     "medium",
				Category:     "storage",
				Summary:      "Cluster has multiple default StorageClasses.",
				Evidence:     []Evidence{{Label: "Default StorageClasses", Value: strings.Join(defaults, ", ")}},
				Recommendations: []Recommendation{{
					Title:         "Keep one default StorageClass",
					Description:   "Choose a single default class to avoid surprising PVC provisioning behavior across namespaces.",
					PatchType:     "storage",
					SafeByDefault: false,
				}},
			})
		}
	}
	if pvErr != nil && scErr != nil {
		return nil, fmt.Errorf("pv: %v; storageclasses: %v", pvErr, scErr)
	}
	return out, nil
}

func podSecurityFinding(pod map[string]any, status, severity, summary, recommendation string) Finding {
	namespace, name := objectNamespaceName(pod)
	return Finding{
		ID:           keyFor(namespace, "Pod/"+name+"/"+status),
		Namespace:    namespace,
		ResourceKind: "Pod",
		ResourceName: name,
		PodName:      name,
		Status:       status,
		Severity:     severity,
		Category:     "security",
		Summary:      summary,
		Evidence:     []Evidence{{Label: "Pod", Value: keyFor(namespace, name)}},
		GitOps:       gitOpsForObject(pod),
		Recommendations: []Recommendation{{
			Title:         "Harden pod security context",
			Description:   recommendation,
			PatchType:     "security",
			SafeByDefault: false,
		}},
	}
}

func matchesAny(selected map[string]bool, aliases ...string) bool {
	for _, alias := range aliases {
		if selected[strings.ToLower(alias)] {
			return true
		}
	}
	return false
}

func objectNamespaceName(obj map[string]any) (string, string) {
	meta := nestedMap(obj, "metadata")
	namespace := strValue(meta["namespace"])
	name := strValue(meta["name"])
	return namespace, name
}

func objectKey(obj map[string]any) string {
	namespace, name := objectNamespaceName(obj)
	return keyFor(namespace, name)
}

func keyFor(namespace, name string) string {
	if namespace == "" {
		return "cluster/" + name
	}
	return namespace + "/" + name
}

func nestedMap(obj map[string]any, key string) map[string]any {
	if obj == nil {
		return map[string]any{}
	}
	value, _ := obj[key].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func nestedSlice(obj map[string]any, key string) []any {
	if obj == nil {
		return nil
	}
	value, _ := obj[key].([]any)
	return value
}

func strValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func boolValue(value any) bool {
	v, _ := value.(bool)
	return v
}

func compactStringMap(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for key, value := range values {
		parts = append(parts, key+"="+value)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func gitOpsForObject(obj map[string]any) GitOpsHints {
	labels, annotations := objectLabelsAnnotations(obj)
	return gitOpsHints(labels, annotations)
}

func endpointAddressCounts(subsets []any) (ready, notReady int) {
	for _, subset := range subsets {
		subsetMap, _ := subset.(map[string]any)
		ready += len(nestedSlice(subsetMap, "addresses"))
		notReady += len(nestedSlice(subsetMap, "notReadyAddresses"))
	}
	return ready, notReady
}

func ingressBackendServices(spec map[string]any) []string {
	seen := map[string]bool{}
	add := func(value string) {
		if value != "" {
			seen[value] = true
		}
	}
	add(backendServiceName(nestedMap(spec, "defaultBackend")))
	for _, rule := range nestedSlice(spec, "rules") {
		ruleMap, _ := rule.(map[string]any)
		http := nestedMap(ruleMap, "http")
		for _, path := range nestedSlice(http, "paths") {
			pathMap, _ := path.(map[string]any)
			add(backendServiceName(nestedMap(pathMap, "backend")))
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func ingressTLSSecretNames(spec map[string]any) []string {
	seen := map[string]bool{}
	for _, tls := range nestedSlice(spec, "tls") {
		tlsMap, _ := tls.(map[string]any)
		if name := strValue(tlsMap["secretName"]); name != "" {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func backendServiceName(backend map[string]any) string {
	service := nestedMap(backend, "service")
	if name := strValue(service["name"]); name != "" {
		return name
	}
	return strValue(backend["serviceName"])
}

func hpaResourceMetricNames(spec map[string]any) []string {
	seen := map[string]bool{}
	for _, metric := range nestedSlice(spec, "metrics") {
		metricMap, _ := metric.(map[string]any)
		if strValue(metricMap["type"]) != "Resource" {
			continue
		}
		resource := nestedMap(metricMap, "resource")
		if name := strValue(resource["name"]); name != "" {
			seen[name] = true
		}
	}
	if len(seen) == 0 && spec["targetCPUUtilizationPercentage"] != nil {
		seen["cpu"] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func containersMissingResourceRequest(obj map[string]any, resource string) []string {
	containers := podTemplateContainers(obj)
	missing := []string{}
	for _, container := range containers {
		containerMap, _ := container.(map[string]any)
		name := strValue(containerMap["name"])
		requests := nestedMap(nestedMap(containerMap, "resources"), "requests")
		if strValue(requests[resource]) == "" {
			missing = append(missing, firstNonEmpty(name, "<unnamed>"))
		}
	}
	sort.Strings(missing)
	return missing
}

func podTemplateContainers(obj map[string]any) []any {
	kind := strings.ToLower(strValue(obj["kind"]))
	if kind == "pod" {
		return nestedSlice(nestedMap(obj, "spec"), "containers")
	}
	template := nestedMap(nestedMap(obj, "spec"), "template")
	return nestedSlice(nestedMap(template, "spec"), "containers")
}

type backendRef struct {
	Kind      string
	Namespace string
	Name      string
}

func httpRouteBackendRefs(route map[string]any) []backendRef {
	spec := nestedMap(route, "spec")
	out := []backendRef{}
	for _, rule := range nestedSlice(spec, "rules") {
		ruleMap, _ := rule.(map[string]any)
		for _, ref := range nestedSlice(ruleMap, "backendRefs") {
			refMap, _ := ref.(map[string]any)
			out = append(out, backendRef{Kind: firstNonEmpty(strValue(refMap["kind"]), "Service"), Namespace: strValue(refMap["namespace"]), Name: strValue(refMap["name"])})
		}
	}
	return out
}

func (a Analyzer) objectNameExists(ctx context.Context, namespace, resource, name string) bool {
	args := []string{"get", resource, name}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	args = append(args, "-o", "name")
	_, err := a.k.Run(ctx, args...)
	return err == nil
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		var out int
		_, _ = fmt.Sscanf(v, "%d", &out)
		return out
	default:
		return 0
	}
}

func sliceHasWildcard(values []any) bool {
	for _, value := range values {
		if strValue(value) == "*" {
			return true
		}
	}
	return false
}

func clusterFinding(kind, name, status, severity, category, summary string, evidence []Evidence) Finding {
	return Finding{
		ID:           "cluster/" + kind + "/" + name + "/" + status,
		ResourceKind: kind,
		ResourceName: name,
		Status:       status,
		Severity:     severity,
		Category:     category,
		Summary:      summary,
		Evidence:     evidence,
		Recommendations: []Recommendation{{
			Title:         "Review controller dependency",
			Description:   "Fix the referenced dependency or relax the policy only after confirming production blast radius.",
			PatchType:     strings.ToLower(kind),
			SafeByDefault: false,
		}},
	}
}
