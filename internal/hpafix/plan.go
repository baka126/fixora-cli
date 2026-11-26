package hpafix

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/resource"
	syaml "sigs.k8s.io/yaml"
)

func Prepare(ctx context.Context, r Reader, authority *Authority, finding analyzer.Finding) (Plan, error) {
	if err := authority.check(); err != nil {
		return Plan{}, err
	}
	p := authority.intent
	if finding.ResourceKind != "HorizontalPodAutoscaler" || finding.Status != "MissingResourceRequests" || finding.Namespace != p.Metadata.Namespace || finding.ResourceName != p.Spec.HPA.Name {
		return Plan{}, errors.New("finding must identify the exact policy HPA and missing utilization requests")
	}
	hpa, err := r.GetHPA(ctx, p.Metadata.Namespace, p.Spec.HPA.Name)
	if err != nil {
		return Plan{}, fmt.Errorf("read policy HPA: %w", err)
	}
	if hpa == nil || hpa.Namespace != p.Metadata.Namespace || hpa.Name != p.Spec.HPA.Name || hpa.UID == "" || hpa.Generation < 1 || hpa.Spec.ScaleTargetRef.Kind != p.Spec.Target.Kind || hpa.Spec.ScaleTargetRef.Name != p.Spec.Target.Name || hpa.Spec.ScaleTargetRef.APIVersion != "apps/v1" {
		return Plan{}, errors.New("live HPA identity does not match exact policy target")
	}
	metricResources, err := repairableMetricResources(hpa)
	if err != nil {
		return Plan{}, err
	}
	if err := validatePolicyMetrics(p, metricResources); err != nil {
		return Plan{}, err
	}
	workload, err := r.GetWorkload(ctx, p.Metadata.Namespace, p.Spec.Target.Kind, p.Spec.Target.Name)
	if err != nil {
		return Plan{}, fmt.Errorf("read HPA target: %w", err)
	}
	if workload == nil || strMap(nestedMap(workload, "metadata")["name"]) != p.Spec.Target.Name || strMap(nestedMap(workload, "metadata")["namespace"]) != p.Metadata.Namespace || strMap(workload["kind"]) != p.Spec.Target.Kind {
		return Plan{}, errors.New("live workload identity does not match exact policy target")
	}
	original, candidate, err := cloneMap(workload)
	if err != nil {
		return Plan{}, err
	}
	template := nestedMap(candidate, "spec", "template")
	podSpec := nestedMap(template, "spec")
	containers, ok := podSpec["containers"].([]any)
	if !ok || len(containers) == 0 {
		return Plan{}, errors.New("target has no regular containers")
	}
	containerIndex := map[string]int{}
	for i, raw := range containers {
		c, ok := raw.(map[string]any)
		if !ok {
			return Plan{}, errors.New("malformed workload container")
		}
		name := strMap(c["name"])
		if name == "" || containerIndex[name] != 0 {
			return Plan{}, errors.New("target has invalid or duplicate regular container names")
		}
		containerIndex[name] = i + 1
	}
	seen := map[string]bool{}
	changed := []string{}
	for _, req := range p.Spec.Requests {
		if seen[req.Container] {
			return Plan{}, errors.New("duplicate policy container entry")
		}
		seen[req.Container] = true
		i, exists := containerIndex[req.Container]
		if !exists {
			return Plan{}, fmt.Errorf("policy container %q is not present in the target", req.Container)
		}
		container := containers[i-1].(map[string]any)
		resources, err := mapField(container, "resources")
		if err != nil {
			return Plan{}, err
		}
		requests, err := mapField(resources, "requests")
		if err != nil {
			return Plan{}, err
		}
		for resourceName, value := range map[string]string{"cpu": req.CPU, "memory": req.Memory} {
			if value == "" {
				continue
			}
			if _, exists := requests[resourceName]; exists {
				continue
			}
			q, parseErr := resource.ParseQuantity(value)
			if parseErr != nil || q.Sign() <= 0 {
				return Plan{}, errors.New("invalid policy resource quantity")
			}
			requests[resourceName] = value
			changed = append(changed, fmt.Sprintf("spec.template.spec.containers[name=%s].resources.requests.%s", req.Container, resourceName))
		}
	}
	if len(changed) == 0 {
		return Plan{}, errors.New("policy requests add no missing target fields")
	}
	sort.Strings(changed)
	digest := fmt.Sprintf("%x", authority.digest)
	plan := Plan{Namespace: p.Metadata.Namespace, HPAName: p.Spec.HPA.Name, TargetKind: p.Spec.Target.Kind, TargetName: p.Spec.Target.Name, ChangedPaths: changed, original: original, candidate: candidate, policy: authority, policyDigest: digest, hpaSpec: *hpa.Spec.DeepCopy(), hpaUID: string(hpa.UID), hpaGeneration: hpa.Generation, hpaSourceManaged: sourceManagedHPA(hpa)}
	plan.planDigest, err = planDigest(plan)
	if err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (p Plan) PatchYAML() (string, error) {
	if err := p.check(); err != nil {
		return "", err
	}
	grouped := map[string]map[string]string{}
	for _, change := range p.RequestChanges() {
		if grouped[change.Container] == nil {
			grouped[change.Container] = map[string]string{}
		}
		grouped[change.Container][change.Resource] = change.Value
	}
	containerNames := make([]string, 0, len(grouped))
	for name := range grouped {
		containerNames = append(containerNames, name)
	}
	sort.Strings(containerNames)
	containers := make([]any, 0, len(grouped))
	for _, name := range containerNames {
		requests := grouped[name]
		containers = append(containers, map[string]any{"name": name, "resources": map[string]any{"requests": requests}})
	}
	metadata := nestedMap(p.original, "metadata")
	patch := map[string]any{"apiVersion": p.original["apiVersion"], "kind": p.original["kind"], "metadata": map[string]any{"name": metadata["name"], "namespace": metadata["namespace"]}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": containers}}}}
	b, err := syaml.Marshal(patch)
	return string(b), err
}
func (p Plan) check() error {
	if err := p.policy.check(); err != nil {
		return err
	}
	if fmt.Sprintf("%x", p.policy.digest) != p.policyDigest {
		return errors.New("HPA policy authority changed")
	}
	d, err := planDigest(p)
	if err != nil {
		return err
	}
	if d != p.planDigest {
		return errors.New("HPA repair plan changed after preparation")
	}
	return nil
}
func planDigest(p Plan) (string, error) {
	b, err := json.Marshal(struct {
		Namespace, HPA, Kind, Target, Policy string
		Paths                                []string
		Original, Candidate                  map[string]any
		HPASpec                              autoscalingv2.HorizontalPodAutoscalerSpec
		HPAUID                               string
		HPAGeneration                        int64
		HPASourceManaged                     bool
	}{p.Namespace, p.HPAName, p.TargetKind, p.TargetName, p.policyDigest, p.ChangedPaths, p.original, p.candidate, p.hpaSpec, p.hpaUID, p.hpaGeneration, p.hpaSourceManaged})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum), nil
}

func repairableMetricResources(hpa *autoscalingv2.HorizontalPodAutoscaler) (map[string]bool, error) {
	resources := map[string]bool{}
	if hpa == nil {
		return nil, errors.New("live HPA is required")
	}
	for _, metric := range hpa.Spec.Metrics {
		if metric.Type != autoscalingv2.ResourceMetricSourceType || metric.Resource == nil || metric.Resource.Target.Type != autoscalingv2.UtilizationMetricType || metric.Resource.Target.AverageUtilization == nil {
			return nil, errors.New("HPA includes an unsupported metric source or target")
		}
		if metric.Resource.Name != "cpu" && metric.Resource.Name != "memory" {
			return nil, errors.New("HPA resource metric must be CPU or memory")
		}
		resources[string(metric.Resource.Name)] = true
	}
	if len(resources) == 0 {
		return nil, errors.New("HPA has no supported CPU or memory utilization metric")
	}
	return resources, nil
}

func validatePolicyMetrics(policy policyDocument, resources map[string]bool) error {
	for _, req := range policy.Spec.Requests {
		if req.CPU != "" && !resources["cpu"] || req.Memory != "" && !resources["memory"] {
			return fmt.Errorf("policy request for %s lacks a matching utilization metric", req.Container)
		}
	}
	return nil
}

func sourceManagedHPA(h *autoscalingv2.HorizontalPodAutoscaler) bool {
	if h == nil {
		return false
	}
	if len(h.OwnerReferences) > 0 {
		return true
	}
	return sourceManagedObject(map[string]any{"metadata": map[string]any{"labels": anyStringMap(h.Labels), "annotations": anyStringMap(h.Annotations), "ownerReferences": h.OwnerReferences}})
}

func sourceManagedObject(obj map[string]any) bool {
	if obj == nil {
		return false
	}
	meta := nestedMap(obj, "metadata")
	if len(nestedSlice(meta, "ownerReferences")) > 0 {
		return true
	}
	for _, field := range []string{"labels", "annotations"} {
		for key, raw := range nestedMap(meta, field) {
			lower, value := strings.ToLower(key), strings.TrimSpace(fmt.Sprint(raw))
			if lower == "app.kubernetes.io/managed-by" && value != "" || lower == "app.kubernetes.io/instance" && value != "" || lower == "helm.sh/chart" && value != "" || strings.Contains(lower, "helm") || strings.Contains(lower, "argocd.argoproj.io") || strings.Contains(lower, "toolkit.fluxcd.io") {
				return true
			}
		}
	}
	return false
}

func anyStringMap(in map[string]string) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		out[key] = value
	}
	return out
}
func cloneMap(in map[string]any) (map[string]any, map[string]any, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return nil, nil, err
	}
	var a, c map[string]any
	if err = json.Unmarshal(b, &a); err != nil {
		return nil, nil, err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return nil, nil, err
	}
	return a, c, nil
}
func nestedMap(root map[string]any, path ...string) map[string]any {
	m := root
	for _, key := range path {
		next, ok := m[key].(map[string]any)
		if !ok {
			return nil
		}
		m = next
	}
	return m
}
func mapField(owner map[string]any, key string) (map[string]any, error) {
	if value, exists := owner[key]; exists {
		result, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("target %s field is malformed", key)
		}
		return result, nil
	}
	result := map[string]any{}
	owner[key] = result
	return result, nil
}
func strMap(v any) string { s, _ := v.(string); return strings.TrimSpace(s) }
