package analyzer

import (
	"fmt"
	"strings"
)

func (a Analyzer) analyzeServiceEndpoints(ctx *ScanContext) ([]Finding, error) {
	services, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "services")
	if err != nil {
		return nil, err
	}
	endpointCounts, err := serviceEndpointCounts(ctx, a.opts.Namespace, a.opts.AllNS)
	if err != nil {
		return nil, err
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
		counts := endpointCounts[keyFor(namespace, name)]
		ready, notReady := counts.Ready, counts.NotReady
		if !counts.Seen || ready == 0 {
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

type endpointCounts struct {
	Seen     bool
	Ready    int
	NotReady int
}

func serviceEndpointCounts(ctx *ScanContext, namespace string, allNS bool) (map[string]endpointCounts, error) {
	slices, sliceErr := ctx.GetResourceItems(namespace, allNS, "endpointslices.discovery.k8s.io")
	if sliceErr == nil {
		return endpointSliceCounts(slices), nil
	}
	endpoints, err := ctx.GetResourceItems(namespace, allNS, "endpoints")
	if err != nil {
		return nil, err
	}
	return legacyEndpointCounts(endpoints), nil
}

func legacyEndpointCounts(endpoints []map[string]any) map[string]endpointCounts {
	out := map[string]endpointCounts{}
	for _, endpoint := range endpoints {
		ready, notReady := endpointAddressCounts(nestedSlice(endpoint, "subsets"))
		counts := endpointCounts{Seen: true, Ready: ready, NotReady: notReady}
		out[objectKey(endpoint)] = counts
	}
	return out
}

func endpointSliceCounts(slices []map[string]any) map[string]endpointCounts {
	out := map[string]endpointCounts{}
	for _, slice := range slices {
		namespace, _ := objectNamespaceName(slice)
		labels, _ := objectLabelsAnnotations(slice)
		serviceName := labels["kubernetes.io/service-name"]
		if serviceName == "" {
			continue
		}
		key := keyFor(namespace, serviceName)
		counts := out[key]
		counts.Seen = true
		for _, endpoint := range nestedSlice(slice, "endpoints") {
			endpointMap, _ := endpoint.(map[string]any)
			addresses := nestedSlice(endpointMap, "addresses")
			if endpointReady(endpointMap) {
				counts.Ready += len(addresses)
			} else {
				counts.NotReady += len(addresses)
			}
		}
		out[key] = counts
	}
	return out
}

func endpointReady(endpoint map[string]any) bool {
	conditions := nestedMap(endpoint, "conditions")
	if conditions["ready"] == nil {
		return true
	}
	return boolValue(conditions["ready"])
}
