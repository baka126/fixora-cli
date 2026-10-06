package repairpolicy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/image"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"
)

func Placement(ctx context.Context, s Snapshot, a *Authority, patch string, inspect func(context.Context, string) (image.Result, error)) (*Proof, error) {
	if _, err := snapshotBinding(s); err != nil {
		return nil, err
	}
	if a == nil || a.digest != digest(a.object) || a.digest == "" {
		return nil, errors.New("placement repair requires an explicit baseline")
	}
	if err := identityMatches(a.object, s.Target); err != nil {
		return nil, err
	}
	if s.Pod == nil || s.Pod.Status.Phase != "Pending" {
		return nil, errors.New("placement repair requires a current Pending Pod")
	}
	message := ""
	for _, c := range s.Pod.Status.Conditions {
		if c.Type == "PodScheduled" && c.Status == "False" && c.Reason == "Unschedulable" {
			message = strings.ToLower(c.Message)
		}
	}
	if message == "" || (!strings.Contains(message, "affinity") && !strings.Contains(message, "selector") && !strings.Contains(message, "taint")) {
		return nil, errors.New("current scheduler evidence does not establish a placement predicate failure")
	}
	for _, block := range []string{"insufficient", "unbound", "volume", "quota", "too many pods", "ports", "schedulinggate", "scheduling gate"} {
		if strings.Contains(message, block) {
			return nil, errors.New("remaining capacity, volume or quota failure requires separate repair")
		}
	}
	source, _ := kube.WorkloadPodSpec(s.Object)
	if err := supportedPlacement(source); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(copyValue(source["nodeSelector"]), copyValue(s.Pod.Spec.NodeSelector)) && !emptyMap(source["nodeSelector"], s.Pod.Spec.NodeSelector) {
		return nil, errors.New("Pending Pod belongs to a different selector revision")
	}
	if !reflect.DeepEqual(copyValue(source["affinity"]), copyValue(s.Pod.Spec.Affinity)) && !emptyMap(source["affinity"], s.Pod.Spec.Affinity) {
		return nil, errors.New("Pending Pod belongs to a different affinity revision")
	}
	if !reflect.DeepEqual(copyValue(source["tolerations"]), copyValue(s.Pod.Spec.Tolerations)) && source["tolerations"] != nil {
		return nil, errors.New("Pending Pod belongs to a different toleration revision")
	}
	obj, changes, err := policyPatch(patch, s.Target)
	if err != nil {
		return nil, err
	}
	_ = obj
	if len(changes) == 0 {
		return nil, errors.New("placement candidate is empty")
	}
	updated := copyObject(source)
	fields := []string{}
	for key, value := range changes {
		switch key {
		case "nodeSelector", "affinity", "tolerations":
		default:
			return nil, errors.New("placement repair may change only selectors, required node affinity and exact tolerations")
		}
		effective := value
		if key == "nodeSelector" {
			merged := copyObject(mapAny(source[key]))
			for label, v := range mapAny(value) {
				if v == nil {
					delete(merged, label)
				} else {
					merged[label] = v
				}
			}
			effective = merged
		}
		if !a.authorizes([]string{key}, effective) {
			return nil, fmt.Errorf("placement field %s does not exactly match operator authority", key)
		}
		if reflect.DeepEqual(copyValue(value), copyValue(source[key])) {
			return nil, errors.New("placement candidate includes an unchanged field")
		}
		if key == "tolerations" {
			if err = validateTolerationAdditions(source[key], value); err != nil {
				return nil, err
			}
		}
		updated[key] = copyValue(effective)
		fields = append(fields, key)
	}
	if err = supportedPlacement(updated); err != nil {
		return nil, err
	}
	if inspect == nil {
		return nil, errors.New("placement requires image platform inspection")
	}
	cs, err := containerMap(source)
	if err != nil {
		return nil, err
	}
	platforms := map[string]image.Result{}
	for _, c := range cs {
		ref, _ := c["image"].(string)
		result, err := inspect(ctx, ref)
		if err != nil || len(result.Platforms) == 0 {
			return nil, errors.New("placement image platforms could not be verified")
		}
		platforms[ref] = result
	}
	if raw, ok := source["initContainers"].([]any); ok {
		for _, v := range raw {
			c, _ := v.(map[string]any)
			ref, _ := c["image"].(string)
			result, err := inspect(ctx, ref)
			if err != nil || len(result.Platforms) == 0 {
				return nil, errors.New("init image platform could not be verified")
			}
			platforms[ref] = result
		}
	}
	eligible := map[string]string{}
	for _, node := range s.Nodes {
		if nodeEligible(node, updated, platforms) && !nodeEligible(node, source, platforms) {
			eligible[node.Metadata.Name] = nodeBinding(node)
		}
	}
	if len(eligible) == 0 {
		return nil, errors.New("no Ready schedulable platform-compatible node proves the changed predicate")
	}
	p, err := bind(s, patch, "restore-placement", a, fields)
	if err != nil {
		return nil, err
	}
	p.placement = updated
	p.nodes = eligible
	p.platforms = platforms
	p.inspect = inspect
	return p, nil
}
func policyPatch(patch string, target Target) (map[string]any, map[string]any, error) {
	decoder := yaml.NewDecoder(strings.NewReader(patch))
	var obj map[string]any
	if err := decoder.Decode(&obj); err != nil {
		return nil, nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, nil, errors.New("candidate must contain one document")
	}
	if err := identityMatches(obj, target); err != nil {
		return nil, nil, err
	}
	for key := range obj {
		if key != "apiVersion" && key != "kind" && key != "metadata" && key != "spec" {
			return nil, nil, errors.New("candidate contains fields outside its template")
		}
	}
	meta, _ := obj["metadata"].(map[string]any)
	if len(meta) != 2 {
		return nil, nil, errors.New("candidate metadata may contain only name and namespace")
	}
	if err := kube.ValidateWorkloadPatchEnvelope(obj, target.Kind); err != nil {
		return nil, nil, err
	}
	spec, _ := kube.WorkloadPodSpec(obj)
	return obj, spec, nil
}
func supportedPlacement(spec map[string]any) error {
	for _, key := range []string{"nodeName", "schedulerName", "priorityClassName", "priority", "preemptionPolicy", "topologySpreadConstraints", "schedulingGates", "runtimeClassName", "resourceClaims", "overhead", "hostNetwork", "hostPID", "hostIPC"} {
		if v, ok := spec[key]; ok && v != nil && v != "" && v != false {
			if key == "schedulerName" && v == "default-scheduler" {
				continue
			}
			return fmt.Errorf("placement cannot verify remaining %s constraint", key)
		}
	}
	if raw, exists := spec["volumes"]; exists {
		vols, ok := raw.([]any)
		if !ok || len(vols) > 0 {
			return errors.New("placement with volumes requires storage verification")
		}
	}
	if value, exists := spec["nodeSelector"]; exists {
		selector, ok := value.(map[string]any)
		if !ok || len(selector) == 0 {
			return errors.New("approved nodeSelector must be nonempty")
		}
		for key, v := range selector {
			str, ok := v.(string)
			if key == "" || !ok || str == "" {
				return errors.New("selector labels must be explicit nonempty strings")
			}
		}
	}
	if value, exists := spec["affinity"]; exists {
		affinity, ok := value.(map[string]any)
		if !ok || len(affinity) != 1 {
			return errors.New("only required node affinity is supported")
		}
		node, ok := affinity["nodeAffinity"].(map[string]any)
		if !ok || len(node) != 1 {
			return errors.New("only required node affinity is supported")
		}
		required, ok := node["requiredDuringSchedulingIgnoredDuringExecution"].(map[string]any)
		if !ok || len(required) != 1 {
			return errors.New("required node affinity is malformed")
		}
		terms, ok := required["nodeSelectorTerms"].([]any)
		if !ok || len(terms) == 0 || len(terms) > 8 {
			return errors.New("required affinity must have bounded explicit terms")
		}
		for _, v := range terms {
			term, ok := v.(map[string]any)
			if !ok || len(term) != 1 {
				return errors.New("affinity matchFields and broad terms require review")
			}
			expressions, ok := term["matchExpressions"].([]any)
			if !ok || len(expressions) == 0 || len(expressions) > 16 {
				return errors.New("affinity predicates must be explicit and bounded")
			}
			for _, v := range expressions {
				ex, ok := v.(map[string]any)
				if !ok || len(ex) != 3 {
					return errors.New("affinity expression is malformed")
				}
				key, _ := ex["key"].(string)
				operator, _ := ex["operator"].(string)
				values, ok := stringArray(ex["values"])
				if key == "" || (operator != "In" && operator != "NotIn") || !ok || len(values) == 0 || len(values) > 32 {
					return errors.New("only bounded In/NotIn label affinity is supported")
				}
			}
		}
	}
	return nil
}
func validateTolerationAdditions(before, after any) error {
	old, _ := before.([]any)
	next, ok := after.([]any)
	if !ok || len(next) <= len(old) {
		return errors.New("only missing exact NoSchedule tolerations may be added")
	}
	for _, v := range old {
		found := false
		for _, n := range next {
			if reflect.DeepEqual(copyValue(v), copyValue(n)) {
				found = true
			}
		}
		if !found {
			return errors.New("placement must preserve existing tolerations")
		}
	}
	for _, v := range next {
		existing := false
		for _, o := range old {
			if reflect.DeepEqual(copyValue(v), copyValue(o)) {
				existing = true
			}
		}
		if existing {
			continue
		}
		t, ok := v.(map[string]any)
		if !ok || len(t) != 4 || t["operator"] != "Equal" || t["effect"] != "NoSchedule" {
			return errors.New("new toleration must be exact Equal/key/value/NoSchedule")
		}
		key, _ := t["key"].(string)
		value, _ := t["value"].(string)
		if key == "" || value == "" || strings.HasPrefix(key, "node.kubernetes.io/") || strings.HasPrefix(key, "node-role.kubernetes.io/") {
			return errors.New("system taints and wildcard tolerations require review")
		}
	}
	return nil
}
func nodeEligible(node kube.Node, spec map[string]any, platforms map[string]image.Result) bool {
	if node.Metadata.UID == "" || node.Spec.Unschedulable {
		return false
	}
	ready := false
	for _, c := range node.Status.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			ready = true
		}
	}
	if !ready {
		return false
	}
	labels := node.Metadata.Labels
	for key, value := range mapAny(spec["nodeSelector"]) {
		if labels[key] != value {
			return false
		}
	}
	if affinity, ok := spec["affinity"].(map[string]any); ok {
		nodeAffinity := mapAny(affinity["nodeAffinity"])
		required := mapAny(nodeAffinity["requiredDuringSchedulingIgnoredDuringExecution"])
		terms, _ := required["nodeSelectorTerms"].([]any)
		match := false
		for _, v := range terms {
			term := mapAny(v)
			expressions, _ := term["matchExpressions"].([]any)
			all := true
			for _, v := range expressions {
				ex := mapAny(v)
				key, _ := ex["key"].(string)
				values, _ := stringArray(ex["values"])
				found := false
				for _, value := range values {
					if labels[key] == value {
						found = true
					}
				}
				if ex["operator"] == "In" && !found || ex["operator"] == "NotIn" && found {
					all = false
				}
			}
			if all {
				match = true
			}
		}
		if !match {
			return false
		}
	}
	tolerations, _ := spec["tolerations"].([]any)
	for _, taint := range node.Spec.Taints {
		effect := taint["effect"]
		if effect != "NoSchedule" && effect != "NoExecute" {
			continue
		}
		tolerated := false
		for _, v := range tolerations {
			t := mapAny(v)
			if t["key"] == taint["key"] && t["value"] == taint["value"] && (t["operator"] == "Equal" || t["operator"] == nil) && (t["effect"] == effect || t["effect"] == nil || t["effect"] == "") {
				tolerated = true
			}
		}
		if !tolerated {
			return false
		}
	}
	for _, result := range platforms {
		if !result.Supports(labels["kubernetes.io/os"], labels["kubernetes.io/arch"]) {
			return false
		}
	}
	return true
}
func nodeBinding(n kube.Node) string {
	return digest([]any{n.Metadata.UID, n.Metadata.Labels, n.Spec.Unschedulable, n.Spec.Taints})
}
func mapAny(v any) map[string]any { m, _ := v.(map[string]any); return m }
func emptyMap(a, b any) bool      { return len(mapAny(copyValue(a))) == 0 && len(mapAny(copyValue(b))) == 0 }
func VerifyPlacement(ctx context.Context, r kube.Reader, p *Proof, pod *kube.Pod, patch string) (*Receipt, error) {
	if p == nil || p.family != "restore-placement" || p.authority == "" || pod == nil {
		return nil, errors.New("bound placement proof is required")
	}
	nodes, err := r.GetNodes(ctx)
	if err != nil {
		return nil, err
	}
	found := false
	for _, node := range nodes {
		if node.Metadata.Name == pod.Spec.NodeName && p.nodes[node.Metadata.Name] == nodeBinding(node) && nodeEligible(node, p.placement, p.platforms) {
			found = true
		}
	}
	if !found {
		return nil, errors.New("scheduler-selected node is not in the verified eligible pool")
	}
	return witness(ctx, r, p, pod, patch)
}

// Capacity reads cluster-wide requests instead of assuming allocatable equals free.
func CheckPlacementCapacity(ctx context.Context, r kube.Reader, p *Proof) error {
	if p == nil || p.family != "restore-placement" {
		return errors.New("placement proof is missing")
	}
	pods, err := r.GetPods(ctx, "", true)
	if err != nil {
		return fmt.Errorf("placement capacity evidence: %w", err)
	}
	nodes, err := r.GetNodes(ctx)
	if err != nil {
		return err
	}
	want, err := requested(p.placement)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if p.nodes[node.Metadata.Name] != nodeBinding(node) {
			continue
		}
		used := map[string]resource.Quantity{}
		count := int64(0)
		for _, pod := range pods.Items {
			if pod.Spec.NodeName != node.Metadata.Name || pod.Status.Phase == "Succeeded" || pod.Status.Phase == "Failed" {
				continue
			}
			count++
			raw := copyValue(pod.Spec).(map[string]any)
			requests, err := requested(raw)
			if err != nil {
				return err
			}
			for key, q := range requests {
				v := used[key]
				v.Add(q)
				used[key] = v
			}
		}
		fits := true
		for key, q := range want {
			available, err := resource.ParseQuantity(node.Status.Allocatable[key])
			if err != nil {
				fits = false
				break
			}
			available.Sub(used[key])
			if available.Cmp(q) < 0 {
				fits = false
			}
		}
		slots, err := resource.ParseQuantity(node.Status.Allocatable["pods"])
		if err != nil || slots.Value() <= count {
			fits = false
		}
		if fits {
			return nil
		}
	}
	return errors.New("no approved node has verified capacity for the shadow Pod")
}
func requested(spec map[string]any) (map[string]resource.Quantity, error) {
	if spec["overhead"] != nil && !emptyMap(spec["overhead"], nil) {
		return nil, errors.New("Pod overhead capacity requires dedicated verification")
	}
	out := map[string]resource.Quantity{}
	for _, section := range []string{"containers", "initContainers"} {
		values, _ := spec[section].([]any)
		for _, v := range values {
			c := mapAny(v)
			if section == "initContainers" && c["restartPolicy"] == "Always" {
				return nil, errors.New("native sidecar capacity requires dedicated verification")
			}
			req := mapAny(mapAny(c["resources"])["requests"])
			for key, v := range req {
				if key != "cpu" && key != "memory" {
					return nil, errors.New("extended resource capacity requires dedicated verification")
				}
				text, ok := v.(string)
				if !ok {
					return nil, errors.New("malformed request")
				}
				q, err := resource.ParseQuantity(text)
				if err != nil {
					return nil, err
				}
				existing := out[key]
				if section == "containers" {
					existing.Add(q)
				} else if existing.Cmp(q) < 0 {
					existing = q
				}
				out[key] = existing
			}
		}
	}
	return out, nil
}
