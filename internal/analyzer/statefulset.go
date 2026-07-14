package analyzer

import (
	"fmt"
)

func (a Analyzer) analyzeStatefulSets(ctx *ScanContext) ([]Finding, error) {
	statefulsets, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "statefulsets")
	if err != nil {
		return nil, err
	}
	services, servicesErr := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "services")
	storageClasses, storageClassesErr := ctx.GetResourceItems("", true, "storageclasses")
	pods, podsErr := ctx.GetPods()

	// Build lookup maps once.
	svcByName := make(map[string]map[string]any, len(services))
	if servicesErr == nil {
		for _, svc := range services {
			ns, name := objectNamespaceName(svc)
			svcByName[keyFor(ns, name)] = svc
		}
	}

	scNames := make(map[string]bool, len(storageClasses))
	if storageClassesErr == nil {
		for _, sc := range storageClasses {
			_, name := objectNamespaceName(sc)
			scNames[name] = true
		}
	}

	// Index pod readiness and presence by name+namespace for fast lookup.
	podReady := make(map[string]bool, len(pods.Items))
	podExists := make(map[string]bool, len(pods.Items))
	if podsErr == nil {
		for _, pod := range pods.Items {
			key := keyFor(pod.Metadata.Namespace, pod.Metadata.Name)
			podExists[key] = true
			podReady[key] = allContainersReady(pod)
		}
	}

	out := []Finding{}
	for _, sts := range statefulsets {
		namespace, name := objectNamespaceName(sts)
		spec := nestedMap(sts, "spec")
		status := nestedMap(sts, "status")

		specReplicas := 1
		if val, ok := spec["replicas"]; ok {
			specReplicas = intValue(val)
		}

		availableReplicas := intValue(status["availableReplicas"])

		if specReplicas != availableReplicas {
			summary := fmt.Sprintf("StatefulSet %s/%s has %d replicas in spec but %d are available", namespace, name, specReplicas, availableReplicas)

			out = append(out, Finding{
				ID:           keyFor(namespace, "StatefulSet/"+name+"/ReplicasMismatch"),
				Namespace:    namespace,
				ResourceKind: "StatefulSet",
				ResourceName: name,
				Status:       "ReplicasMismatch",
				Severity:     "high",
				Category:     "workload",
				Summary:      summary,
				Evidence: []Evidence{
					{Label: "Spec Replicas", Value: fmt.Sprint(specReplicas)},
					{Label: "Available Replicas", Value: fmt.Sprint(availableReplicas)},
				},
				GitOps: gitOpsForObject(sts),
				Recommendations: []Recommendation{{
					Title:         "Inspect statefulset pods",
					Description:   "Check the pod status and persistent volume claim bindings.",
					PatchType:     "statefulset",
					SafeByDefault: false,
				}},
			})
		}

		// StatefulSetRolloutBlocked: rolling update stalled because ordinal-0 pod is not ready.
		// Only applies to OrderedReady policy (the default); Parallel policy does not gate on pod-0.
		curRev := strValue(status["currentRevision"])
		updateRev := strValue(status["updateRevision"])
		podMgmt := strValue(spec["podManagementPolicy"])
		if podMgmt == "" {
			podMgmt = "OrderedReady"
		}
		if podsErr == nil && curRev != "" && updateRev != "" && curRev != updateRev && podMgmt == "OrderedReady" {
			ordinalZero := name + "-0"
			pod0Key := keyFor(namespace, ordinalZero)
			if podExists[pod0Key] && !podReady[pod0Key] {
				out = append(out, Finding{
					ID:           keyFor(namespace, "StatefulSet/"+name+"/StatefulSetRolloutBlocked"),
					Namespace:    namespace,
					ResourceKind: "StatefulSet",
					ResourceName: name,
					Status:       "StatefulSetRolloutBlocked",
					Severity:     "high",
					Category:     "workload",
					Summary:      fmt.Sprintf("StatefulSet %s/%s rollout is blocked: pod %s is not ready and update is pending", namespace, name, ordinalZero),
					Evidence: []Evidence{
						{Label: "Stuck pod", Value: ordinalZero},
						{Label: "Current revision", Value: curRev},
						{Label: "Update revision", Value: updateRev},
						{Label: "Pod management policy", Value: podMgmt},
					},
					GitOps: gitOpsForObject(sts),
					Recommendations: []Recommendation{{
						Title:         "Investigate the stuck ordinal-0 pod",
						Description:   "Check pod logs, events, and PVC bindings for " + ordinalZero + ". The OrderedReady policy requires each pod to be Ready before the next is updated.",
						PatchType:     "statefulset",
						SafeByDefault: false,
					}},
				})
			}
		}

		// HeadlessServiceMissing: spec.serviceName absent or not headless.
		svcName := strValue(spec["serviceName"])
		if svcName == "" {
			out = append(out, headlessServiceFinding(sts, namespace, name, svcName, "missing"))
		} else if servicesErr == nil {
			svc, exists := svcByName[keyFor(namespace, svcName)]
			clusterIP := "absent"
			if exists {
				clusterIP = strValue(nestedMap(svc, "spec")["clusterIP"])
			}
			if !exists || clusterIP != "None" {
				out = append(out, headlessServiceFinding(sts, namespace, name, svcName, clusterIP))
			}
		}

		// StatefulSetStorageUnbindable: a volumeClaimTemplate references a missing StorageClass.
		if storageClassesErr == nil {
			for _, vct := range nestedSlice(spec, "volumeClaimTemplates") {
				vctMap, ok := vct.(map[string]any)
				if !ok {
					continue
				}
				vctMeta := nestedMap(vctMap, "metadata")
				vctSpec := nestedMap(vctMap, "spec")
				className := strValue(vctSpec["storageClassName"])
				if className == "" {
					continue
				}
				if !scNames[className] {
					tplName := strValue(vctMeta["name"])
					out = append(out, Finding{
						ID:           keyFor(namespace, "StatefulSet/"+name+"/StatefulSetStorageUnbindable/"+tplName),
						Namespace:    namespace,
						ResourceKind: "StatefulSet",
						ResourceName: name,
						Status:       "StatefulSetStorageUnbindable",
						Severity:     "medium",
						Category:     "storage",
						Summary:      fmt.Sprintf("StatefulSet %s/%s volumeClaimTemplate %q references StorageClass %q which does not exist", namespace, name, tplName, className),
						Evidence: []Evidence{
							{Label: "VolumeClaimTemplate", Value: tplName},
							{Label: "StorageClass", Value: className},
						},
						GitOps: gitOpsForObject(sts),
						Recommendations: []Recommendation{{
							Title:         "Create the missing StorageClass or update the template",
							Description:   "StorageClass " + className + " referenced by volumeClaimTemplate " + tplName + " does not exist. Create it or change the storageClassName to an existing class.",
							PatchType:     "statefulset",
							SafeByDefault: false,
						}},
					})
				}
			}
		}
	}
	return out, nil
}

func headlessServiceFinding(sts map[string]any, namespace, name, svcName, clusterIP string) Finding {
	displayName := svcName
	if displayName == "" {
		displayName = "<missing>"
	}
	return Finding{
		ID:           keyFor(namespace, "StatefulSet/"+name+"/HeadlessServiceMissing"),
		Namespace:    namespace,
		ResourceKind: "StatefulSet",
		ResourceName: name,
		Status:       "HeadlessServiceMissing",
		Severity:     "high",
		Category:     "networking",
		Summary:      fmt.Sprintf("StatefulSet %s/%s references serviceName %q which is not a headless Service (clusterIP: None)", namespace, name, displayName),
		Evidence: []Evidence{
			{Label: "Service name", Value: displayName},
			{Label: "Observed clusterIP", Value: clusterIP},
		},
		GitOps: gitOpsForObject(sts),
		Recommendations: []Recommendation{{
			Title:         "Create or fix the headless Service",
			Description:   "Ensure spec.serviceName points to a Service in namespace " + namespace + " with clusterIP: None so StatefulSet pods get stable DNS entries.",
			PatchType:     "service",
			SafeByDefault: false,
		}},
	}
}
