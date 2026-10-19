package hpafix

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type planReader struct {
	hpa      *autoscalingv2.HorizontalPodAutoscaler
	workload map[string]any
}

func (r planReader) GetHPA(context.Context, string, string) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	return r.hpa, nil
}
func (r planReader) GetWorkload(context.Context, string, string, string) (map[string]any, error) {
	return r.workload, nil
}
func (r planReader) ListPods(context.Context, string) ([]corev1.Pod, error) { return nil, nil }
func (r planReader) ListHPAs(context.Context, string) ([]autoscalingv2.HorizontalPodAutoscaler, error) {
	return nil, nil
}
func (r planReader) ListPodMetrics(context.Context, string) ([]map[string]any, error) {
	return nil, nil
}
func (r planReader) ResolveOwner(context.Context, corev1.Pod) (kube.WorkloadOwner, error) {
	return kube.WorkloadOwner{}, nil
}

func candidateFixture(t *testing.T) (*Authority, planReader, analyzer.Finding) {
	t.Helper()
	body := `apiVersion: fixora.io/v1alpha1
kind: HPARepairPolicy
metadata: {name: api-policy, namespace: prod}
spec:
  hpa: {name: api-scale}
  target: {kind: Deployment, name: api}
  requests: [{container: web, cpu: 250m, memory: 128Mi}]
`
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	hpa := &autoscalingv2.HorizontalPodAutoscaler{}
	hpa.Name = "api-scale"
	hpa.Namespace = "prod"
	hpa.Spec.ScaleTargetRef = autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}
	hpa.Spec.Metrics = []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: "cpu", Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: ptr(int32(70))}}}, {Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: "memory", Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: ptr(int32(80))}}}}
	w := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "api", "namespace": "prod", "uid": "target-uid"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "web", "image": "example/app", "resources": map[string]any{"requests": map[string]any{"cpu": "100m"}}}}}}}}
	f := analyzer.Finding{Namespace: "prod", ResourceKind: "HorizontalPodAutoscaler", ResourceName: "api-scale", Status: "MissingResourceRequests"}
	return a, planReader{hpa, w}, f
}
func ptr[T any](v T) *T { return &v }

func TestPrepareAddsOnlyPolicyApprovedMissingRequests(t *testing.T) {
	a, r, f := candidateFixture(t)
	p, err := Prepare(context.Background(), r, a, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ChangedPaths) != 1 || p.ChangedPaths[0] != "spec.template.spec.containers[name=web].resources.requests.memory" {
		t.Fatalf("unexpected changed paths: %#v", p.ChangedPaths)
	}
	patch, err := p.PatchYAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patch, "memory: 128Mi") || strings.Contains(patch, "cpu: 250m") {
		t.Fatalf("candidate did not limit patch to the missing request:\n%s", patch)
	}
}

func TestPrepareRejectsUnknownOrDuplicateContainer(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		a, r, f := candidateFixture(t)
		a.intent.Spec.Requests[0].Container = "missing"
		if _, err := Prepare(context.Background(), r, a, f); err == nil {
			t.Fatal("unknown container accepted")
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		a, r, f := candidateFixture(t)
		a.intent.Spec.Requests = append(a.intent.Spec.Requests, a.intent.Spec.Requests[0])
		if _, err := Prepare(context.Background(), r, a, f); err == nil {
			t.Fatal("duplicate policy container accepted")
		}
	})
}
func TestPrepareRejectsNonUtilizationAndUnsupportedTarget(t *testing.T) {
	t.Run("average value", func(t *testing.T) {
		a, r, f := candidateFixture(t)
		r.hpa.Spec.Metrics[0].Resource.Target = autoscalingv2.MetricTarget{Type: autoscalingv2.AverageValueMetricType, AverageValue: resource.NewQuantity(1, resource.DecimalSI)}
		if _, err := Prepare(context.Background(), r, a, f); err == nil {
			t.Fatal("AverageValue metric authorized request patch")
		}
	})
	t.Run("unsupported workload", func(t *testing.T) {
		a, r, f := candidateFixture(t)
		r.hpa.Spec.ScaleTargetRef.Kind = "CronJob"
		if _, err := Prepare(context.Background(), r, a, f); err == nil {
			t.Fatal("unsupported target accepted")
		}
	})
}
func TestPrepareRequiresExactHPAAndTargetIdentity(t *testing.T) {
	t.Run("HPA mismatch", func(t *testing.T) {
		a, r, f := candidateFixture(t)
		f.ResourceName = "other"
		if _, err := Prepare(context.Background(), r, a, f); err == nil {
			t.Fatal("finding for another HPA accepted")
		}
	})
	t.Run("target mismatch", func(t *testing.T) {
		a, r, f := candidateFixture(t)
		r.hpa.Spec.ScaleTargetRef.Name = "other"
		if _, err := Prepare(context.Background(), r, a, f); err == nil {
			t.Fatal("different scale target accepted")
		}
	})
}
