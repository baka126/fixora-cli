package analyzer

import (
	"fmt"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
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
	pods, _ := ctx.GetPods() // used only for readiness-gate refinement; tolerate errors
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
			if condType, podName, blocked := readinessGateBlock(pods, namespace, selector); blocked {
				out = append(out, Finding{
					ID:           keyFor(namespace, "Service/"+name+"/EndpointsBlockedByReadinessGate"),
					Namespace:    namespace,
					ResourceKind: "Service",
					ResourceName: name,
					Status:       "EndpointsBlockedByReadinessGate",
					Severity:     "high",
					Category:     "networking",
					Summary:      "Service has no ready endpoints because backing pods are blocked by an unsatisfied readiness gate.",
					Evidence: []Evidence{
						{Label: "Readiness gate", Value: condType},
						{Label: "Example pod", Value: podName},
						{Label: "Selector", Value: compactStringMap(selector)},
					},
					GitOps: gitOpsForObject(service),
					Recommendations: []Recommendation{{
						Title:         "Resolve the readiness gate condition",
						Description:   "The backing pods pass their own probes but are held out of the Service because an external controller has not set the readiness-gate condition to True. Investigate the controller that owns this condition. Do NOT modify the readiness probe or Service selector — neither is the cause.",
						PatchType:     "readiness",
						SafeByDefault: false,
					}},
				})
				continue
			}
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
	if sliceErr == nil && len(slices) > 0 {
		return endpointSliceCounts(slices), nil
	}
	// EndpointSlices were missing (absent key returns nil,nil) or empty: fall
	// back to legacy Endpoints so we don't fabricate NoEndpoints findings.
	endpoints, err := ctx.GetResourceItems(namespace, allNS, "endpoints")
	if err != nil {
		if sliceErr == nil {
			// Slices queried cleanly but empty and legacy endpoints are
			// unavailable: trust the (empty) slice view.
			return endpointSliceCounts(slices), nil
		}
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

// readinessGateBlock returns a non-empty (conditionType, podName, true) when at
// least one pod selected by selector in namespace is held out of endpoints
// solely by an unsatisfied readiness gate: it declares a readinessGate whose
// matching status condition is not True, while all its containers report ready.
func readinessGateBlock(pods kube.PodList, namespace string, selector map[string]string) (conditionType, podName string, blocked bool) {
	for _, pod := range pods.Items {
		if pod.Metadata.Namespace != namespace || !labelsMatch(selector, pod.Metadata.Labels) {
			continue
		}
		if len(pod.Spec.ReadinessGates) == 0 || !allContainersReady(pod) {
			continue
		}
		for _, gate := range pod.Spec.ReadinessGates {
			if gate.ConditionType == "" {
				continue
			}
			if !conditionTrue(pod.Status.Conditions, gate.ConditionType) {
				return gate.ConditionType, pod.Metadata.Name, true
			}
		}
	}
	return "", "", false
}

func allContainersReady(pod kube.Pod) bool {
	if len(pod.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		}
	}
	return true
}

func conditionTrue(conditions []kube.Condition, condType string) bool {
	for _, c := range conditions {
		if c.Type == condType {
			return strings.EqualFold(c.Status, "True")
		}
	}
	return false // an absent condition is not satisfied
}
