package analyzer

import (
	"sort"
	"strconv"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func (a Analyzer) analyzeServicePortTargets(ctx *ScanContext) ([]Finding, error) {
	services, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "services")
	if err != nil {
		return nil, err
	}
	pods, err := ctx.GetPods()
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
		numeric, named, hasPorts := selectedContainerPorts(pods, namespace, selector)
		if !hasPorts {
			// No selected pods, or no declared container ports: containerPort is
			// informational, so a "mismatch" here would be a false positive.
			continue
		}
		var mismatches []string
		high := false
		for _, p := range nestedSlice(spec, "ports") {
			portMap, _ := p.(map[string]any)
			gotName, gotNumber, isNamed := serviceTargetPort(portMap)
			if isNamed {
				if !named[gotName] {
					mismatches = append(mismatches, "targetPort "+gotName+" (named)")
					high = true
				}
			} else if gotNumber != 0 && !numeric[gotNumber] {
				mismatches = append(mismatches, "targetPort "+strconv.Itoa(gotNumber))
			}
		}
		if len(mismatches) == 0 {
			continue
		}
		severity := "medium"
		if high {
			severity = "high"
		}
		out = append(out, Finding{
			ID:           keyFor(namespace, "Service/"+name+"/ServicePortMismatch"),
			Namespace:    namespace,
			ResourceKind: "Service",
			ResourceName: name,
			Status:       "ServicePortMismatch",
			Severity:     severity,
			Category:     "networking",
			Summary:      "Service targets a port no selected backing container exposes.",
			Evidence: []Evidence{
				{Label: "Selector", Value: compactStringMap(selector)},
				{Label: "Unmatched targetPorts", Value: strings.Join(mismatches, ", ")},
				{Label: "Declared container ports", Value: portsLabel(numeric, named)},
			},
			GitOps: gitOpsForObject(service),
			Recommendations: []Recommendation{{
				Title:         "Align the Service targetPort with a container port",
				Description:   "The Service targetPort does not resolve to a port exposed by the selected pods. Confirm whether the Service targetPort or the container's declared ports is correct, then fix at the source manifest. Fixora does not auto-choose which side to change.",
				PatchType:     "service",
				SafeByDefault: false,
			}},
		})
	}
	return out, nil
}

// selectedContainerPorts collects declared container ports across pods in
// namespace whose labels match selector. hasPorts is false when no pod is
// selected or no selected container declares any port.
func selectedContainerPorts(pods kube.PodList, namespace string, selector map[string]string) (numeric map[int]bool, named map[string]bool, hasPorts bool) {
	numeric = map[int]bool{}
	named = map[string]bool{}
	selectedAny := false
	for _, pod := range pods.Items {
		if pod.Metadata.Namespace != namespace || !labelsMatch(selector, pod.Metadata.Labels) {
			continue
		}
		selectedAny = true
		containers := append(append([]kube.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...)
		for _, c := range containers {
			for _, port := range c.Ports {
				if port.ContainerPort != 0 {
					numeric[port.ContainerPort] = true
				}
				if port.Name != "" {
					named[port.Name] = true
				}
			}
		}
	}
	if !selectedAny {
		return nil, nil, false
	}
	return numeric, named, len(numeric) > 0 || len(named) > 0
}

// serviceTargetPort resolves a Service port's targetPort. A named target returns
// (name, 0, true); a numeric target returns ("", number, false). An absent
// targetPort defaults to the numeric port value.
func serviceTargetPort(portMap map[string]any) (named string, number int, isNamed bool) {
	tp, ok := portMap["targetPort"]
	if !ok || tp == nil {
		return "", intValue(portMap["port"]), false
	}
	if s, isStr := tp.(string); isStr {
		if n, convErr := strconv.Atoi(strings.TrimSpace(s)); convErr == nil {
			return "", n, false
		}
		return s, 0, true
	}
	return "", intValue(tp), false
}

// portsLabel renders a sorted, human-readable list of declared/exposed ports.
func portsLabel(numeric map[int]bool, named map[string]bool) string {
	nums := make([]int, 0, len(numeric))
	for n := range numeric {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	names := make([]string, 0, len(named))
	for s := range named {
		names = append(names, s)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(nums)+len(names))
	for _, n := range nums {
		parts = append(parts, strconv.Itoa(n))
	}
	parts = append(parts, names...)
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}
