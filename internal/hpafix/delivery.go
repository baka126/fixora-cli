package hpafix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
)

type Outcome struct {
	WorkloadConverged bool   `json:"workloadConverged"`
	HPAHealthy        bool   `json:"hpaHealthy"`
	Partial           bool   `json:"partial"`
	RolledBack        bool   `json:"rolledBack,omitempty"`
	Message           string `json:"message"`
}

type Applied struct {
	namespace, hpaName, targetKind, targetName, targetUID, hpaUID, resourceVersion string
	original, candidate                                                            map[string]any
	changedPaths                                                                   []string
	policy                                                                         *Authority
	uncertain                                                                      bool
}

func Apply(ctx context.Context, r Runtime, p Plan, receipt *Receipt) (*Applied, Outcome, error) {
	if receipt == nil || receipt.state == nil {
		return nil, Outcome{}, errors.New("fresh private HPA verification receipt required")
	}
	if err := p.check(); err != nil {
		return nil, Outcome{}, err
	}
	snapshot, err := collectSnapshot(ctx, r, &p)
	if err != nil {
		return nil, Outcome{}, err
	}
	state := receipt.state
	if state.planDigest != p.planDigest || state.snapshotDigest != snapshot.Digest || state.hpaUID != string(snapshot.HPA.UID) || state.targetUID != strMap(nestedMap(snapshot.Workload, "metadata")["uid"]) || state.hpaRV != snapshot.HPA.ResourceVersion || state.targetRV != strMap(nestedMap(snapshot.Workload, "metadata")["resourceVersion"]) {
		return nil, Outcome{}, errors.New("HPA evidence changed after verification; prepare and verify again")
	}
	patch, err := requestPatch(p, snapshot.Workload)
	if err != nil {
		return nil, Outcome{}, err
	}
	if err = receipt.consume(); err != nil {
		return nil, Outcome{}, err
	}
	updated, err := r.PatchWorkload(ctx, p.Namespace, p.TargetKind, p.TargetName, patch, false)
	if err != nil {
		readback, readErr := r.GetWorkload(ctx, p.Namespace, p.TargetKind, p.TargetName)
		if readErr == nil && strMap(nestedMap(readback, "metadata")["uid"]) == state.targetUID && reflectSpecEqual(readback, p.candidate) {
			a := newApplied(p, snapshot.Workload, readback, state.hpaUID, true)
			return a, Outcome{Partial: true, Message: "write response was lost; approved candidate is present, but authorship is uncertain"}, fmt.Errorf("HPA repair write outcome is uncertain: %w", err)
		}
		return nil, Outcome{Partial: true, Message: "write response was lost; read-back could not confirm the result"}, fmt.Errorf("HPA repair write outcome is uncertain: %w", err)
	}
	if err = validateAdmitted(p, snapshot.Workload, updated); err != nil {
		return nil, Outcome{Partial: true, Message: "write occurred but the returned target failed validation"}, fmt.Errorf("HPA repair write needs review: %w", err)
	}
	return newApplied(p, snapshot.Workload, updated, state.hpaUID, false), Outcome{Message: "approved request fields were applied; rollout and HPA health must be observed"}, nil
}

func newApplied(p Plan, original, candidate map[string]any, hpaUID string, uncertain bool) *Applied {
	o, _, _ := cloneMap(original)
	c, _, _ := cloneMap(candidate)
	return &Applied{namespace: p.Namespace, hpaName: p.HPAName, targetKind: p.TargetKind, targetName: p.TargetName, targetUID: strMap(nestedMap(original, "metadata")["uid"]), hpaUID: hpaUID, resourceVersion: strMap(nestedMap(candidate, "metadata")["resourceVersion"]), original: o, candidate: c, changedPaths: append([]string(nil), p.ChangedPaths...), policy: p.policy, uncertain: uncertain}
}

func Observe(ctx context.Context, r Runtime, a *Applied) (Outcome, error) {
	if a == nil {
		return Outcome{}, errors.New("applied HPA repair required")
	}
	if a.uncertain {
		return Outcome{Partial: true, Message: "write authorship is uncertain; observe the target before further action"}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = OperationContext(ctx)
		defer cancel()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var last Outcome
	for {
		last = observeOnce(ctx, r, a)
		if last.WorkloadConverged && last.HPAHealthy {
			return last, nil
		}
		select {
		case <-ctx.Done():
			last.Partial = true
			last.Message = "workload and autoscaler did not both verify before the operation deadline"
			return last, nil
		case <-ticker.C:
		}
	}
}

func observeOnce(ctx context.Context, r Runtime, a *Applied) Outcome {
	w, err := r.GetWorkload(ctx, a.namespace, a.targetKind, a.targetName)
	if err != nil {
		return Outcome{Partial: true, Message: "target workload could not be observed"}
	}
	meta := nestedMap(w, "metadata")
	workloadOK := strMap(meta["uid"]) == a.targetUID && stableRollout(w) == nil && appliedRequestsPresent(w, a)
	pods, podsErr := r.ListPods(ctx, a.namespace)
	owned, ownersComplete := ownedTargetPods(ctx, r, pods, a, w)
	workloadOK = workloadOK && podsErr == nil && ownersComplete && targetPodsReady(owned, w)
	h, err := r.GetHPA(ctx, a.namespace, a.hpaName)
	if err != nil {
		return Outcome{WorkloadConverged: workloadOK, Partial: true, Message: "HPA could not be observed"}
	}
	metrics, metricsErr := r.ListPodMetrics(ctx, a.namespace)
	hpaOK := string(h.UID) == a.hpaUID && h.Spec.ScaleTargetRef.Kind == a.targetKind && h.Spec.ScaleTargetRef.Name == a.targetName && h.Status.ObservedGeneration != nil && *h.Status.ObservedGeneration == h.Generation && h.Status.DesiredReplicas == replicas(nestedMap(w, "spec")["replicas"]) && h.Status.CurrentReplicas == h.Status.DesiredReplicas && !conditionTrue(h, autoscalingv2.ScalingLimited) && conditionTrue(h, autoscalingv2.AbleToScale) && conditionTrue(h, autoscalingv2.ScalingActive) && currentMetricsReady(h) && metricsErr == nil
	if hpaOK && (!ownersComplete || len(owned) == 0 || validateMetricsForPods(metrics, owned, h) != nil) {
		hpaOK = false
	}
	message := ""
	switch {
	case workloadOK && hpaOK:
		message = "target rollout and HPA metrics are healthy"
	case workloadOK:
		message = "target rollout converged; HPA metrics are not yet healthy"
	case hpaOK:
		message = "HPA metrics are healthy; target rollout is incomplete"
	default:
		message = "target rollout and HPA metrics remain unverified"
	}
	return Outcome{WorkloadConverged: workloadOK, HPAHealthy: hpaOK, Partial: !workloadOK || !hpaOK, Message: message}
}

func targetPodsReady(target []corev1.Pod, w map[string]any) bool {
	desired := replicas(nestedMap(w, "spec")["replicas"])
	if len(target) == 0 || int32(len(target)) != desired {
		return false
	}
	for _, pod := range target {
		if pod.DeletionTimestamp != nil || !podReady(pod) {
			return false
		}
	}
	return true
}
func ownedTargetPods(ctx context.Context, r Runtime, pods []corev1.Pod, a *Applied, w map[string]any) ([]corev1.Pod, bool) {
	out := []corev1.Pod{}
	for _, pod := range pods {
		owner, err := r.ResolveOwner(ctx, pod)
		if err != nil {
			return nil, false
		}
		if owner.Matches(a.targetKind, a.targetName, strMap(nestedMap(w, "metadata")["uid"])) {
			out = append(out, pod)
		}
	}
	return out, true
}

func currentMetricsReady(h *autoscalingv2.HorizontalPodAutoscaler) bool {
	required := map[string]bool{}
	for _, m := range h.Spec.Metrics {
		if m.Type == autoscalingv2.ResourceMetricSourceType && m.Resource != nil && m.Resource.Target.Type == autoscalingv2.UtilizationMetricType {
			required[string(m.Resource.Name)] = false
		}
	}
	if len(required) == 0 {
		return false
	}
	for _, current := range h.Status.CurrentMetrics {
		if current.Type == autoscalingv2.ResourceMetricSourceType && current.Resource != nil {
			n := string(current.Resource.Name)
			if _, ok := required[n]; ok && current.Resource.Current.AverageUtilization != nil {
				required[n] = true
			}
		}
	}
	for _, ready := range required {
		if !ready {
			return false
		}
	}
	return true
}

func Rollback(ctx context.Context, r Runtime, a *Applied) (Outcome, error) {
	if a == nil || a.policy == nil {
		return Outcome{}, errors.New("applied HPA repair required")
	}
	if a.uncertain {
		return Outcome{Partial: true, Message: "cannot roll back a write with uncertain authorship"}, errors.New("HPA write authorship is uncertain")
	}
	if err := a.policy.check(); err != nil {
		return Outcome{}, err
	}
	current, err := r.GetWorkload(ctx, a.namespace, a.targetKind, a.targetName)
	if err != nil {
		return Outcome{}, fmt.Errorf("read target before rollback: %w", err)
	}
	meta := nestedMap(current, "metadata")
	if strMap(meta["uid"]) != a.targetUID {
		return Outcome{}, errors.New("target UID changed; refusing HPA rollback")
	}
	containers, ok := nestedMap(current, "spec", "template", "spec")["containers"].([]any)
	if !ok {
		return Outcome{}, errors.New("target containers unavailable")
	}
	indices := map[string]int{}
	for i, raw := range containers {
		c, ok := raw.(map[string]any)
		if !ok {
			return Outcome{}, errors.New("malformed target container")
		}
		name := strMap(c["name"])
		if name == "" {
			return Outcome{}, errors.New("target container name is missing")
		}
		if _, duplicate := indices[name]; duplicate {
			return Outcome{}, errors.New("duplicate target container name")
		}
		indices[name] = i
	}
	candidateContainers, ok := nestedMap(a.candidate, "spec", "template", "spec")["containers"].([]any)
	if !ok {
		return Outcome{}, errors.New("saved candidate containers unavailable")
	}
	candidateByName := map[string]map[string]any{}
	for _, raw := range candidateContainers {
		container, ok := raw.(map[string]any)
		if !ok || strMap(container["name"]) == "" {
			return Outcome{}, errors.New("malformed saved candidate container")
		}
		candidateByName[strMap(container["name"])] = container
	}
	ops := []patchOperation{{Op: "test", Path: "/metadata/uid", Value: meta["uid"]}, {Op: "test", Path: "/metadata/resourceVersion", Value: meta["resourceVersion"]}}
	for _, changed := range a.changedPaths {
		name, res, ok := parseChangedPath(changed)
		if !ok {
			return Outcome{}, errors.New("invalid rollback request path")
		}
		idx, ok := indices[name]
		if !ok {
			return Outcome{}, errors.New("container changed; refusing rollback")
		}
		container := containers[idx].(map[string]any)
		requests := nestedMap(container, "resources", "requests")
		expected, ok := candidateByName[name]
		if !ok {
			return Outcome{}, errors.New("saved candidate container is missing")
		}
		want := nestedMap(expected, "resources", "requests")[res]
		if value, exists := requests[res]; !exists || value != want {
			return Outcome{}, errors.New("approved request changed; refusing rollback")
		}
		path := fmt.Sprintf("/spec/template/spec/containers/%d/resources/requests/%s", idx, res)
		ops = append(ops, patchOperation{Op: "test", Path: path, Value: want}, patchOperation{Op: "remove", Path: path})
	}
	patch, err := json.Marshal(ops)
	if err != nil {
		return Outcome{}, err
	}
	updated, err := r.PatchWorkload(ctx, a.namespace, a.targetKind, a.targetName, patch, false)
	if err != nil {
		return Outcome{Partial: true, Message: "rollback response was not confirmed"}, fmt.Errorf("HPA rollback outcome uncertain: %w", err)
	}
	if updated == nil || strMap(nestedMap(updated, "metadata")["uid"]) != a.targetUID {
		return Outcome{Partial: true, Message: "rollback returned a different target"}, errors.New("HPA rollback target identity mismatch")
	}
	expectedSpec, err := rollbackExpectedSpec(current, a.changedPaths)
	if err != nil {
		return Outcome{Partial: true, Message: "rollback response could not be validated"}, err
	}
	if !reflect.DeepEqual(expectedSpec, nestedMap(updated, "spec")) {
		return Outcome{Partial: true, Message: "rollback response did not confirm only the approved request fields were removed"}, errors.New("HPA rollback response differs from the field-only inverse")
	}
	return Outcome{RolledBack: true, Message: "Fixora-added request fields were removed; unrelated workload fields were preserved"}, nil
}

func appliedRequestsPresent(workload map[string]any, a *Applied) bool {
	containers, ok := nestedMap(workload, "spec", "template", "spec")["containers"].([]any)
	if !ok {
		return false
	}
	byName := map[string]map[string]any{}
	for _, raw := range containers {
		container, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		name := strMap(container["name"])
		if name == "" || byName[name] != nil {
			return false
		}
		byName[name] = container
	}
	for _, path := range a.changedPaths {
		name, resource, ok := parseChangedPath(path)
		if !ok {
			return false
		}
		container := byName[name]
		if container == nil {
			return false
		}
		expected := nestedMap(a.candidate, "spec", "template", "spec")["containers"].([]any)
		var want any
		for _, raw := range expected {
			c, ok := raw.(map[string]any)
			if ok && strMap(c["name"]) == name {
				want = nestedMap(c, "resources", "requests")[resource]
				break
			}
		}
		if want == nil || nestedMap(container, "resources", "requests")[resource] != want {
			return false
		}
	}
	return true
}

func rollbackExpectedSpec(current map[string]any, paths []string) (map[string]any, error) {
	copy, _, err := cloneMap(map[string]any{"spec": nestedMap(current, "spec")})
	if err != nil {
		return nil, err
	}
	containers, ok := nestedMap(copy, "spec", "template", "spec")["containers"].([]any)
	if !ok {
		return nil, errors.New("target containers unavailable while validating rollback")
	}
	byName := map[string]map[string]any{}
	for _, raw := range containers {
		container, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("malformed target container")
		}
		name := strMap(container["name"])
		if name == "" || byName[name] != nil {
			return nil, errors.New("target container names are ambiguous")
		}
		byName[name] = container
	}
	for _, path := range paths {
		name, resource, ok := parseChangedPath(path)
		if !ok || byName[name] == nil {
			return nil, errors.New("rollback request path no longer resolves")
		}
		requests := nestedMap(byName[name], "resources", "requests")
		delete(requests, resource)
	}
	return nestedMap(copy, "spec"), nil
}

func parseChangedPath(path string) (string, string, bool) {
	start := strings.Index(path, "containers[name=")
	if start < 0 {
		return "", "", false
	}
	rest := path[start+len("containers[name="):]
	end := strings.Index(rest, "]")
	if end < 0 {
		return "", "", false
	}
	container := rest[:end]
	resource := strings.TrimPrefix(rest[end+1:], ".resources.requests.")
	return container, resource, (resource == "cpu" || resource == "memory")
}
func reflectSpecEqual(a, b map[string]any) bool {
	return reflect.DeepEqual(nestedMap(a, "spec"), nestedMap(b, "spec"))
}
