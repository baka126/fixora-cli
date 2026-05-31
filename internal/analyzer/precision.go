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
