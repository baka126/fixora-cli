package analyzer

func (a Analyzer) analyzePodSecurity(ctx *ScanContext) ([]Finding, error) {
	pods, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "pods")
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
