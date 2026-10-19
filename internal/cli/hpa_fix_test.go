package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
)

type hpaCLIStub struct {
	hpa      *autoscalingv2.HorizontalPodAutoscaler
	workload map[string]any
	writes   int
}

func (s *hpaCLIStub) GetHPA(context.Context, string, string) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	return s.hpa, nil
}
func (s *hpaCLIStub) GetWorkload(context.Context, string, string, string) (map[string]any, error) {
	return s.workload, nil
}
func (s *hpaCLIStub) ListPods(context.Context, string) ([]corev1.Pod, error) { return nil, nil }
func (s *hpaCLIStub) ListHPAs(context.Context, string) ([]autoscalingv2.HorizontalPodAutoscaler, error) {
	return nil, nil
}
func (s *hpaCLIStub) ListPodMetrics(context.Context, string) ([]map[string]any, error) {
	return nil, nil
}
func (s *hpaCLIStub) ResolveOwner(context.Context, corev1.Pod) (kube.WorkloadOwner, error) {
	return kube.WorkloadOwner{}, nil
}
func (s *hpaCLIStub) PatchWorkload(context.Context, string, string, string, []byte, bool) (map[string]any, error) {
	s.writes++
	return nil, nil
}

func hpaCLIFixture(t *testing.T) (options, *hpaCLIStub, analyzer.Finding) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hpa.yaml")
	body := `apiVersion: fixora.io/v1alpha1
kind: HPARepairPolicy
metadata: {name: api-policy, namespace: prod}
spec:
  hpa: {name: api-scale}
  target: {kind: Deployment, name: api}
  requests: [{container: app, cpu: 250m}]
	`
	body = strings.TrimSpace(body) + "\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	h := &autoscalingv2.HorizontalPodAutoscaler{}
	h.Name, h.Namespace = "api-scale", "prod"
	h.UID, h.Generation = "hpa-uid", 1
	h.Spec.ScaleTargetRef = autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}
	h.Spec.Metrics = []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: "cpu", Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: ptr32(70)}}}}
	w := map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "api", "namespace": "prod", "uid": "target-uid", "resourceVersion": "1"},
		"spec":     map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": "example/app"}}}}},
	}
	return options{hpaPolicy: path, preview: true, delivery: "patch", output: "text"}, &hpaCLIStub{hpa: h, workload: w}, analyzer.Finding{Namespace: "prod", ResourceKind: "HorizontalPodAutoscaler", ResourceName: "api-scale", Status: "MissingResourceRequests", Summary: "CPU utilization cannot be computed for container app."}
}
func ptr32(v int32) *int32 { return &v }

func TestHPAFixPreviewNeverWrites(t *testing.T) {
	o, r, f := hpaCLIFixture(t)
	var out, stderr strings.Builder
	if code := runHPAFixRuntime(context.Background(), &out, &stderr, o, r, f); code != 0 || r.writes != 0 {
		t.Fatalf("code=%d writes=%d stderr=%s output=%s", code, r.writes, stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "review only") {
		t.Fatalf("preview did not state review-only result: %s", out.String())
	}
}

func TestHPAFixRequiresPolicyAndExplicitClusterDelivery(t *testing.T) {
	o, _, f := hpaCLIFixture(t)
	o.hpaPolicy = ""
	o.delivery = "cluster"
	var out strings.Builder
	if code := runHPAFixRuntime(context.Background(), &out, &strings.Builder{}, o, nil, f); code != 0 || !strings.Contains(out.String(), "--hpa-policy") {
		t.Fatalf("missing policy result code=%d output=%s", code, out.String())
	}
}

func TestHPAFixRejectsMixedRepairPolicies(t *testing.T) {
	o, _, f := hpaCLIFixture(t)
	o.pdbPolicy = "pdb.yaml"
	if err := validateHPAOptions(o, f); err == nil {
		t.Fatal("mixed HPA and PDB policies accepted")
	}
}

func TestHPAFixAIProposalCannotChangePolicyValues(t *testing.T) {
	o, r, f := hpaCLIFixture(t)
	f.AI = &analyzer.AIResult{RootCause: "Missing CPU request", PatchYAML: "resources:\n  requests:\n    cpu: 999999", RecommendedFix: "Use 999999"}
	var out strings.Builder
	if code := runHPAFixRuntime(context.Background(), &out, &strings.Builder{}, o, r, f); code != 0 {
		t.Fatalf("code=%d output=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "250m") || strings.Contains(out.String(), "999999") {
		t.Fatalf("model changed policy authority: %s", out.String())
	}
}

func TestHPAFixBlocksSourceManagedTargets(t *testing.T) {
	o, r, f := hpaCLIFixture(t)
	o.preview = false
	o.delivery = "cluster"
	o.yes = true
	f.GitOps.FluxHint = "managed"
	var out strings.Builder
	if code := runHPAFixRuntime(context.Background(), &out, &strings.Builder{}, o, r, f); code != 0 || r.writes != 0 || !strings.Contains(out.String(), "direct delivery is blocked") {
		t.Fatalf("code=%d writes=%d output=%s", code, r.writes, out.String())
	}
}

func TestHPAFixBlocksTargetManagedByHelm(t *testing.T) {
	o, r, f := hpaCLIFixture(t)
	o.preview = false
	o.delivery = "cluster"
	o.yes = true
	r.workload["metadata"].(map[string]any)["labels"] = map[string]any{"app.kubernetes.io/managed-by": "Helm"}
	var out strings.Builder
	if code := runHPAFixRuntime(context.Background(), &out, &strings.Builder{}, o, r, f); code != 0 || r.writes != 0 || !strings.Contains(out.String(), "direct delivery is blocked") {
		t.Fatalf("code=%d writes=%d output=%s", code, r.writes, out.String())
	}
}

func TestHPAFixOutputIsConciseAndStructured(t *testing.T) {
	o, r, f := hpaCLIFixture(t)
	o.output = "json"
	var out strings.Builder
	if code := runHPAFixRuntime(context.Background(), &out, &strings.Builder{}, o, r, f); code != 0 {
		t.Fatalf("code=%d: %s", code, out.String())
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out.String()), &report); err != nil {
		t.Fatal(err)
	}
	if _, ok := report["receipt"]; ok {
		t.Fatal("serialized private preflight receipt")
	}
	if len(out.String()) > 2500 || r.writes != 0 {
		t.Fatalf("structured output not concise or wrote cluster: bytes=%d writes=%d", len(out.String()), r.writes)
	}
}

func TestHPARepairPolicyFlagParses(t *testing.T) {
	parsed, rest, err := parseFlags([]string{"hpa/api-scale", "--hpa-policy", "approved.yaml", "--preview"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.hpaPolicy != "approved.yaml" || !parsed.preview || len(rest) != 1 || rest[0] != "hpa/api-scale" {
		t.Fatalf("unexpected parsed HPA options: %+v rest=%v", parsed, rest)
	}
}

func TestHPAAIContextOmitsMetricSelectors(t *testing.T) {
	metrics := safeHPASpecMetrics([]any{map[string]any{"type": "External", "external": map[string]any{"metric": map[string]any{"name": "queue-depth", "selector": map[string]any{"matchLabels": map[string]any{"tenant-token": "do-not-send"}}}}}})
	encoded, _ := json.Marshal(metrics)
	if strings.Contains(string(encoded), "do-not-send") || strings.Contains(string(encoded), "tenant-token") {
		t.Fatalf("metric selector leaked into AI context: %s", encoded)
	}
}
