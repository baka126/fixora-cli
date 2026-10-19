package hpafix

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type verifyRuntime struct {
	planReader
	pods            []corev1.Pod
	hpas            []autoscalingv2.HorizontalPodAutoscaler
	metrics         []map[string]any
	owners          map[string]kube.WorkloadOwner
	patchResult     map[string]any
	patchErr        error
	metricsErr      error
	afterPatch      func()
	dryRuns, writes int
}

func (r *verifyRuntime) ListPods(context.Context, string) ([]corev1.Pod, error) { return r.pods, nil }
func (r *verifyRuntime) ListHPAs(context.Context, string) ([]autoscalingv2.HorizontalPodAutoscaler, error) {
	return r.hpas, nil
}
func (r *verifyRuntime) ListPodMetrics(context.Context, string) ([]map[string]any, error) {
	if r.metricsErr != nil {
		return nil, r.metricsErr
	}
	return r.metrics, nil
}
func (r *verifyRuntime) ResolveOwner(_ context.Context, p corev1.Pod) (kube.WorkloadOwner, error) {
	return r.owners[string(p.UID)], nil
}
func (r *verifyRuntime) PatchWorkload(_ context.Context, _, _, _ string, _ []byte, dry bool) (map[string]any, error) {
	if dry {
		r.dryRuns++
	} else {
		r.writes++
	}
	if r.afterPatch != nil {
		r.afterPatch()
	}
	return r.patchResult, r.patchErr
}

func TestOperationContextPreservesParentDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx, done := OperationContext(parent)
	defer done()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("operation context has no deadline")
	}
	parentDeadline, _ := parent.Deadline()
	if !deadline.Equal(parentDeadline) {
		t.Fatalf("deadline %v replaced earlier parent deadline %v", deadline, parentDeadline)
	}
}

func TestSnapshotRequiresStableOwnedReadyPods(t *testing.T) {
	p, _, _, runtime := verifyFixture(t)
	if _, err := collectSnapshot(context.Background(), runtime, &p); err != nil {
		t.Fatal(err)
	}
	runtime.pods[0].Status.Conditions = nil
	if _, err := collectSnapshot(context.Background(), runtime, &p); err == nil {
		t.Fatal("pod without Ready condition accepted")
	}
}

func TestSnapshotPreservesPodMetricsReadFailure(t *testing.T) {
	p, _, _, runtime := verifyFixture(t)
	runtime.metricsErr = errors.New("metrics API forbidden")
	if _, err := collectSnapshot(context.Background(), runtime, &p); err == nil {
		t.Fatal("metrics read error treated as empty metrics")
	}
}

func TestSnapshotRejectsHPAReplicaDrift(t *testing.T) {
	p, _, _, runtime := verifyFixture(t)
	runtime.hpa.Status.DesiredReplicas = 2
	runtime.hpa.Status.CurrentReplicas = 2
	if _, err := collectSnapshot(context.Background(), runtime, &p); err == nil {
		t.Fatal("HPA desired replicas differing from the stable target were accepted")
	}
}

func TestVerifyDryRunsExactTemplatePatch(t *testing.T) {
	p, _, _, runtime := verifyFixture(t)
	receipt, err := Verify(context.Background(), runtime, p)
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil || runtime.dryRuns != 1 || runtime.writes != 0 {
		t.Fatalf("receipt=%v dryRuns=%d writes=%d", receipt, runtime.dryRuns, runtime.writes)
	}
}

func TestVerifyRejectsAdmissionRewrite(t *testing.T) {
	p, _, _, runtime := verifyFixture(t)
	runtime.patchResult = cloneObject(runtime.patchResult)
	nestedMap(runtime.patchResult, "spec", "template", "spec")["containers"].([]any)[0].(map[string]any)["image"] = "mutated"
	if _, err := Verify(context.Background(), runtime, p); err == nil {
		t.Fatal("admission rewrite accepted")
	}
}

func TestVerifyRejectsAdmissionMetadataMutation(t *testing.T) {
	p, _, _, runtime := verifyFixture(t)
	runtime.patchResult = cloneObject(runtime.patchResult)
	nestedMap(runtime.patchResult, "metadata")["labels"] = map[string]any{"operator.example/changed": "true"}
	if _, err := Verify(context.Background(), runtime, p); err == nil {
		t.Fatal("admission metadata mutation outside the request patch was accepted")
	}
}

func TestVerifyRejectsReplacedTargetUID(t *testing.T) {
	p, _, _, runtime := verifyFixture(t)
	runtime.patchResult["metadata"].(map[string]any)["uid"] = "replacement"
	if _, err := Verify(context.Background(), runtime, p); err == nil {
		t.Fatal("replaced target UID accepted")
	}
}

func TestVerifyRejectsHPAOrMetricsDrift(t *testing.T) {
	t.Run("hpa drift", func(t *testing.T) {
		p, _, _, runtime := verifyFixture(t)
		runtime.hpa.ResourceVersion = "changed"
		if _, err := Verify(context.Background(), runtime, p); err == nil {
			t.Fatal("HPA drift accepted")
		}
	})
	t.Run("metrics unavailable", func(t *testing.T) {
		p, _, _, runtime := verifyFixture(t)
		runtime.metricsErr = errors.New("metrics unavailable")
		if _, err := Verify(context.Background(), runtime, p); err == nil {
			t.Fatal("metrics error accepted")
		}
	})
}

func TestVerifyRejectsTargetReplacementAfterPrepare(t *testing.T) {
	p, _, _, runtime := verifyFixture(t)
	nestedMap(runtime.workload, "metadata")["uid"] = "replacement"
	runtime.owners["pod-uid"] = kube.WorkloadOwner{Kind: "Deployment", Name: "api", UID: "replacement", Identities: []kube.OwnerIdentity{{Kind: "Deployment", Name: "api", UID: "replacement"}}}
	if _, err := Verify(context.Background(), runtime, p); err == nil {
		t.Fatal("replacement target with the same name was accepted")
	}
}

func TestVerifyRejectsUnsupportedMetricAddedAfterPrepare(t *testing.T) {
	p, _, _, runtime := verifyFixture(t)
	runtime.hpa.Spec.Metrics = append(runtime.hpa.Spec.Metrics, autoscalingv2.MetricSpec{Type: autoscalingv2.ExternalMetricSourceType, External: &autoscalingv2.ExternalMetricSource{Metric: autoscalingv2.MetricIdentifier{Name: "queue-depth"}, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.AverageValueMetricType, AverageValue: resource.NewQuantity(1, resource.DecimalSI)}}})
	if _, err := Verify(context.Background(), runtime, p); err == nil {
		t.Fatal("HPA with a newly added unsupported metric was accepted")
	}
}

func TestVerifyRejectsPolicyRequestWhoseMetricWasRemoved(t *testing.T) {
	p, _, _, runtime := verifyFixture(t)
	runtime.hpa.Spec.Metrics = runtime.hpa.Spec.Metrics[:1]
	if _, err := Verify(context.Background(), runtime, p); err == nil {
		t.Fatal("request for removed utilization metric was still authorized")
	}
}

func TestSnapshotRejectsStaleOrIncompletePodMetrics(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		p, _, _, runtime := verifyFixture(t)
		runtime.metrics[0]["timestamp"] = time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
		runtime.metrics[0]["window"] = "30s"
		if _, err := collectSnapshot(context.Background(), runtime, &p); err == nil {
			t.Fatal("stale PodMetrics were accepted")
		}
	})
	t.Run("missing regular container usage", func(t *testing.T) {
		p, _, _, runtime := verifyFixture(t)
		runtime.pods[0].Spec.Containers = append(runtime.pods[0].Spec.Containers, corev1.Container{Name: "sidecar"})
		if _, err := collectSnapshot(context.Background(), runtime, &p); err == nil {
			t.Fatal("PodMetrics omitting a target sidecar were accepted")
		}
	})
}

func TestReceiptCannotBeCopiedOrReused(t *testing.T) {
	p, _, _, runtime := verifyFixture(t)
	receipt, err := Verify(context.Background(), runtime, p)
	if err != nil {
		t.Fatal(err)
	}
	copied := *receipt
	if err := receipt.consume(); err != nil {
		t.Fatal(err)
	}
	if err := copied.consume(); err == nil {
		t.Fatal("copied receipt reused shared authority")
	}
	if err := receipt.consume(); err == nil {
		t.Fatal("receipt reused")
	}
}

func TestPolicyRecheckRejectsPathReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(validPolicy), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".new", []byte(validPolicy), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	if err := a.check(); err == nil {
		t.Fatal("replaced policy accepted")
	}
}

func newVerifyRuntime(t *testing.T, r planReader) *verifyRuntime {
	t.Helper()
	target := r.workload
	targetMeta := nestedMap(target, "metadata")
	targetMeta["uid"] = "target-uid"
	targetMeta["generation"] = int64(2)
	targetMeta["resourceVersion"] = "target-rv"
	nestedMap(target, "spec")["replicas"] = int64(1)
	target["status"] = map[string]any{"observedGeneration": int64(2), "replicas": int64(1), "updatedReplicas": int64(1), "readyReplicas": int64(1), "availableReplicas": int64(1)}
	r.hpa.Generation = 2
	r.hpa.ResourceVersion = "hpa-rv"
	r.hpa.UID = "hpa-uid"
	r.hpa.Status = autoscalingv2.HorizontalPodAutoscalerStatus{ObservedGeneration: ptr(int64(2)), CurrentReplicas: 1, DesiredReplicas: 1, CurrentMetrics: []autoscalingv2.MetricStatus{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricStatus{Name: "cpu", Current: autoscalingv2.MetricValueStatus{AverageUtilization: ptr(int32(30))}}}, {Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricStatus{Name: "memory", Current: autoscalingv2.MetricValueStatus{AverageUtilization: ptr(int32(40))}}}}, Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{{Type: autoscalingv2.AbleToScale, Status: corev1.ConditionTrue, Reason: "ReadyForNewScale"}, {Type: autoscalingv2.ScalingActive, Status: corev1.ConditionTrue, Reason: "ValidMetricFound"}}}
	pod := corev1.Pod{}
	pod.Name = "api-abc"
	pod.Namespace = "prod"
	pod.UID = "pod-uid"
	pod.Spec.Containers = []corev1.Container{{Name: "web"}}
	pod.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Minute))
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	owner := kube.WorkloadOwner{Kind: "Deployment", Name: "api", UID: "target-uid", Identities: []kube.OwnerIdentity{{Kind: "Deployment", Name: "api", UID: "target-uid"}}}
	return &verifyRuntime{planReader: r, pods: []corev1.Pod{pod}, hpas: []autoscalingv2.HorizontalPodAutoscaler{*r.hpa}, metrics: []map[string]any{{"metadata": map[string]any{"name": "api-abc", "namespace": "prod"}, "timestamp": time.Now().UTC().Format(time.RFC3339), "window": "30s", "containers": []any{map[string]any{"name": "web", "usage": map[string]any{"cpu": "10m", "memory": "12Mi"}}}}}, owners: map[string]kube.WorkloadOwner{"pod-uid": owner}, patchResult: target}
}

func verifyFixture(t *testing.T) (Plan, Authority, analyzer.Finding, *verifyRuntime) {
	t.Helper()
	a, r, f := candidateFixture(t)
	plan, err := Prepare(context.Background(), r, a, f)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVerifyRuntime(t, r)
	runtime.patchResult = cloneObject(plan.candidate)
	return plan, *a, f, runtime
}
func cloneObject(in map[string]any) map[string]any { _, out, _ := cloneMap(in); return out }
