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
	endpoints, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "endpoints")
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
