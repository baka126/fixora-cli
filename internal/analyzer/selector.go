package analyzer

import (
	"fmt"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

type labelRequirement struct {
	key    string
	op     string
	value  string
	exists bool
}

func parseLabelSelector(selector string) ([]labelRequirement, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, nil
	}
	parts := strings.Split(selector, ",")
	out := make([]labelRequirement, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "!") {
			key := strings.TrimSpace(strings.TrimPrefix(part, "!"))
			if key == "" {
				return nil, fmt.Errorf("invalid label selector %q", selector)
			}
			out = append(out, labelRequirement{key: key, op: "not-exists"})
			continue
		}
		if strings.Contains(part, "!=") || strings.Contains(part, "==") || strings.Contains(part, "=") {
			req, err := parseBinaryLabelRequirement(part)
			if err != nil {
				return nil, fmt.Errorf("invalid label selector %q", selector)
			}
			out = append(out, req)
			continue
		}
		out = append(out, labelRequirement{key: part, op: "exists", exists: true})
	}
	return out, nil
}

func parseBinaryLabelRequirement(part string) (labelRequirement, error) {
	for _, sep := range []string{"!=", "==", "="} {
		if !strings.Contains(part, sep) {
			continue
		}
		pieces := strings.SplitN(part, sep, 2)
		key := strings.TrimSpace(pieces[0])
		value := strings.TrimSpace(pieces[1])
		if key == "" || value == "" {
			return labelRequirement{}, fmt.Errorf("empty key or value")
		}
		op := sep
		if op == "==" {
			op = "="
		}
		return labelRequirement{key: key, op: op, value: value}, nil
	}
	return labelRequirement{}, fmt.Errorf("missing operator")
}

func labelsSatisfySelector(labels map[string]string, selector string) (bool, error) {
	requirements, err := parseLabelSelector(selector)
	if err != nil {
		return false, err
	}
	for _, req := range requirements {
		value, exists := labels[req.key]
		switch req.op {
		case "exists":
			if !exists {
				return false, nil
			}
		case "not-exists":
			if exists {
				return false, nil
			}
		case "=":
			if !exists || value != req.value {
				return false, nil
			}
		case "!=":
			if exists && value == req.value {
				return false, nil
			}
		default:
			return false, fmt.Errorf("unsupported label selector operator %q", req.op)
		}
	}
	return true, nil
}

func filterPodsByLabelSelector(pods kube.PodList, selector string) (kube.PodList, error) {
	if strings.TrimSpace(selector) == "" {
		return pods, nil
	}
	out := kube.PodList{}
	for _, pod := range pods.Items {
		ok, err := labelsSatisfySelector(pod.Metadata.Labels, selector)
		if err != nil {
			return kube.PodList{}, err
		}
		if ok {
			out.Items = append(out.Items, pod)
		}
	}
	return out, nil
}

func filterObjectsByLabelSelector(items []map[string]any, selector string) ([]map[string]any, error) {
	if strings.TrimSpace(selector) == "" {
		return items, nil
	}
	out := []map[string]any{}
	for _, item := range items {
		labels, _ := objectLabelsAnnotations(item)
		ok, err := labelsSatisfySelector(labels, selector)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, item)
		}
	}
	return out, nil
}
