package hpafix

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const operationTimeout = 3 * time.Minute

type Snapshot struct {
	HPA      autoscalingv2.HorizontalPodAutoscaler
	Workload map[string]any
	Pods     []corev1.Pod
	HPAs     []autoscalingv2.HorizontalPodAutoscaler
	Metrics  []map[string]any
	Owners   map[string]kube.WorkloadOwner
	Digest   string
}

func OperationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, operationTimeout)
}

func collectSnapshot(ctx context.Context, r Reader, p *Plan) (Snapshot, error) {
	if p == nil {
		return Snapshot{}, errors.New("exact HPA repair plan required")
	}
	a := p.policy
	if err := a.check(); err != nil {
		return Snapshot{}, err
	}
	hpa, err := r.GetHPA(ctx, p.Namespace, p.HPAName)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read HPA: %w", err)
	}
	if hpa == nil || hpa.Namespace != p.Namespace || hpa.Name != p.HPAName || hpa.UID == "" || hpa.ResourceVersion == "" || hpa.Generation <= 0 {
		return Snapshot{}, errors.New("HPA identity or version is incomplete")
	}
	if string(hpa.UID) != p.hpaUID || hpa.Generation != p.hpaGeneration || !reflect.DeepEqual(hpa.Spec, p.hpaSpec) {
		return Snapshot{}, errors.New("HPA identity or spec changed after candidate preparation")
	}
	if sourceManagedHPA(hpa) {
		return Snapshot{}, errors.New("source-managed HPA cannot be changed directly")
	}
	if hpa.Status.ObservedGeneration == nil || *hpa.Status.ObservedGeneration != hpa.Generation {
		return Snapshot{}, errors.New("HPA status is stale")
	}
	if hpa.Spec.ScaleTargetRef.Kind != p.TargetKind || hpa.Spec.ScaleTargetRef.Name != p.TargetName || hpa.Spec.ScaleTargetRef.APIVersion != "apps/v1" {
		return Snapshot{}, errors.New("HPA target identity changed")
	}
	workload, err := r.GetWorkload(ctx, p.Namespace, p.TargetKind, p.TargetName)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read HPA target: %w", err)
	}
	meta := nestedMap(workload, "metadata")
	if strMap(workload["kind"]) != p.TargetKind || strMap(meta["name"]) != p.TargetName || strMap(meta["namespace"]) != p.Namespace || strMap(meta["uid"]) == "" || strMap(meta["resourceVersion"]) == "" {
		return Snapshot{}, errors.New("target identity or version is incomplete")
	}
	if strMap(meta["uid"]) != strMap(nestedMap(p.original, "metadata")["uid"]) {
		return Snapshot{}, errors.New("HPA target was replaced after candidate preparation")
	}
	if sourceManagedObject(workload) {
		return Snapshot{}, errors.New("source-managed HPA target cannot be changed directly")
	}
	metricResources, err := repairableMetricResources(hpa)
	if err != nil {
		return Snapshot{}, err
	}
	if err = validatePolicyMetrics(a.intent, metricResources); err != nil {
		return Snapshot{}, err
	}
	if err := stableRollout(workload); err != nil {
		return Snapshot{}, err
	}
	pods, err := r.ListPods(ctx, p.Namespace)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list namespace Pods: %w", err)
	}
	hpas, err := r.ListHPAs(ctx, p.Namespace)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list namespace HPAs: %w", err)
	}
	metrics, err := r.ListPodMetrics(ctx, p.Namespace)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read PodMetrics: %w", err)
	}
	owners := map[string]kube.WorkloadOwner{}
	owned := []corev1.Pod{}
	for _, pod := range pods {
		owner, e := r.ResolveOwner(ctx, pod)
		if e != nil {
			return Snapshot{}, fmt.Errorf("resolve Pod %s owner: %w", pod.Name, e)
		}
		if owner.Matches(p.TargetKind, p.TargetName, strMap(meta["uid"])) {
			owners[string(pod.UID)] = owner
			if pod.DeletionTimestamp != nil || pod.Namespace != p.Namespace || pod.UID == "" || !podReady(pod) {
				return Snapshot{}, fmt.Errorf("owned Pod %s is not stably Ready", pod.Name)
			}
			owned = append(owned, pod)
		}
	}
	desired := replicas(nestedMap(workload, "spec")["replicas"])
	if desired < 1 || int32(len(owned)) != desired {
		return Snapshot{}, errors.New("owned Pod inventory does not match desired replicas")
	}
	if hpa.Status.DesiredReplicas < 1 || hpa.Status.CurrentReplicas != hpa.Status.DesiredReplicas {
		return Snapshot{}, errors.New("HPA is actively changing replica count")
	}
	if hpa.Status.DesiredReplicas != desired {
		return Snapshot{}, errors.New("HPA desired replicas differ from the stable target workload")
	}
	if conditionTrue(hpa, autoscalingv2.ScalingLimited) {
		return Snapshot{}, errors.New("HPA reports a replica scaling limit")
	}
	if !conditionTrue(hpa, autoscalingv2.AbleToScale) {
		return Snapshot{}, errors.New("HPA scale target is not currently operable")
	}
	if !conditionTrue(hpa, autoscalingv2.ScalingActive) {
		if !conditionReason(hpa, autoscalingv2.ScalingActive, "FailedGetResourceMetric") || p == nil {
			return Snapshot{}, errors.New("HPA has no active supported metric")
		}
	}
	foundHPA := false
	for _, other := range hpas {
		if other.Namespace == p.Namespace && other.Name == p.HPAName && other.UID == hpa.UID && other.ResourceVersion == hpa.ResourceVersion {
			foundHPA = true
		}
		if other.Namespace == p.Namespace && other.Name != p.HPAName && other.Spec.ScaleTargetRef.Kind == p.TargetKind && other.Spec.ScaleTargetRef.Name == p.TargetName {
			return Snapshot{}, errors.New("multiple HPAs target the same workload")
		}
	}
	if !foundHPA {
		return Snapshot{}, errors.New("exact HPA is absent from namespace inventory")
	}
	if err := validateMetricsForPods(metrics, owned, hpa); err != nil {
		return Snapshot{}, err
	}
	ownedMetrics := make([]map[string]any, 0, len(owned))
	metricByName := map[string]map[string]any{}
	for _, metric := range metrics {
		name := strMap(nestedMap(metric, "metadata")["name"])
		if name != "" {
			metricByName[name] = metric
		}
	}
	for _, pod := range owned {
		if metric, ok := metricByName[pod.Name]; ok {
			ownedMetrics = append(ownedMetrics, metric)
		}
	}
	s := Snapshot{HPA: *hpa, Workload: workload, Pods: owned, HPAs: hpas, Metrics: ownedMetrics, Owners: owners}
	s.Digest, err = snapshotDigest(s)
	if err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

func stableRollout(w map[string]any) error {
	spec, status := nestedMap(w, "spec"), nestedMap(w, "status")
	desired := replicas(spec["replicas"])
	if desired < 1 || int32Value(status["observedGeneration"]) != int32Value(nestedMap(w, "metadata")["generation"]) {
		return errors.New("target rollout has not observed its current generation")
	}
	if strMap(w["kind"]) == "Deployment" {
		if spec["paused"] == true || int32Value(status["updatedReplicas"]) != desired || int32Value(status["readyReplicas"]) != desired || int32Value(status["availableReplicas"]) != desired {
			return errors.New("Deployment rollout is paused or incomplete")
		}
	} else {
		if int32Value(status["readyReplicas"]) != desired || strMap(status["currentRevision"]) == "" || status["currentRevision"] != status["updateRevision"] {
			return errors.New("StatefulSet rollout is incomplete")
		}
	}
	return nil
}
func podReady(p corev1.Pod) bool {
	if p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
func conditionTrue(h *autoscalingv2.HorizontalPodAutoscaler, t autoscalingv2.HorizontalPodAutoscalerConditionType) bool {
	for _, c := range h.Status.Conditions {
		if c.Type == t {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
func conditionReason(h *autoscalingv2.HorizontalPodAutoscaler, t autoscalingv2.HorizontalPodAutoscalerConditionType, reason string) bool {
	for _, c := range h.Status.Conditions {
		if c.Type == t && c.Status == corev1.ConditionFalse && c.Reason == reason {
			return true
		}
	}
	return false
}
func validateMetricsForPods(metrics []map[string]any, pods []corev1.Pod, h *autoscalingv2.HorizontalPodAutoscaler) error {
	return validateMetricsForPodsAt(metrics, pods, h, time.Now())
}

func validateMetricsForPodsAt(metrics []map[string]any, pods []corev1.Pod, h *autoscalingv2.HorizontalPodAutoscaler, now time.Time) error {
	byPod := map[string]map[string]any{}
	for _, m := range metrics {
		metadata := nestedMap(m, "metadata")
		name := strMap(metadata["name"])
		if name != "" {
			if _, duplicate := byPod[name]; duplicate {
				return fmt.Errorf("duplicate PodMetrics for owned Pod %s", name)
			}
			byPod[name] = m
		}
	}
	required, err := repairableMetricResources(h)
	if err != nil {
		return err
	}
	for _, pod := range pods {
		m, ok := byPod[pod.Name]
		if !ok {
			return fmt.Errorf("PodMetrics unavailable for owned Pod %s", pod.Name)
		}
		metadata := nestedMap(m, "metadata")
		if ns := strMap(metadata["namespace"]); ns == "" || ns != pod.Namespace {
			return fmt.Errorf("PodMetrics namespace differs for owned Pod %s", pod.Name)
		}
		timestamp, parseErr := time.Parse(time.RFC3339, strMap(m["timestamp"]))
		window, windowErr := time.ParseDuration(strMap(m["window"]))
		if parseErr != nil || windowErr != nil || window <= 0 || window > 5*time.Minute || pod.CreationTimestamp.IsZero() || timestamp.Before(pod.CreationTimestamp.Time) || timestamp.After(now.Add(30*time.Second)) || now.Sub(timestamp) > 2*time.Minute {
			return fmt.Errorf("PodMetrics for owned Pod %s is stale or incomplete", pod.Name)
		}
		containers := map[string]map[string]any{}
		for _, raw := range nestedSlice(m, "containers") {
			c, ok := raw.(map[string]any)
			if !ok || strMap(c["name"]) == "" {
				return fmt.Errorf("PodMetrics has an invalid container entry for Pod %s", pod.Name)
			}
			if _, duplicate := containers[strMap(c["name"])]; duplicate {
				return fmt.Errorf("PodMetrics has duplicate container usage for Pod %s", pod.Name)
			}
			containers[strMap(c["name"])] = nestedMap(c, "usage")
		}
		if len(pod.Spec.Containers) == 0 {
			return fmt.Errorf("owned Pod %s has no regular containers", pod.Name)
		}
		for _, container := range pod.Spec.Containers {
			usage, exists := containers[container.Name]
			if !exists {
				return fmt.Errorf("PodMetrics lacks usage for container %s in Pod %s", container.Name, pod.Name)
			}
			for name := range required {
				quantityText := strMap(usage[name])
				quantity, quantityErr := resource.ParseQuantity(quantityText)
				if quantityText == "" || quantityErr != nil || quantity.Sign() < 0 {
					return fmt.Errorf("PodMetrics lacks valid %s usage for container %s in Pod %s", name, container.Name, pod.Name)
				}
			}
		}
	}
	return nil
}
func snapshotDigest(s Snapshot) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	d := sha256.Sum256(b)
	return fmt.Sprintf("%x", d), nil
}
func replicas(v any) int32 {
	if v == nil {
		return 1
	}
	return int32Value(v)
}
func int32Value(v any) int32 {
	switch n := v.(type) {
	case int32:
		return n
	case int64:
		return int32(n)
	case int:
		return int32(n)
	case float64:
		return int32(n)
	}
	return 0
}
func nestedSlice(m map[string]any, key string) []any { v, _ := m[key].([]any); return v }
