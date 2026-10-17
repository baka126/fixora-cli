package pdbfix

import (
	"context"
	"encoding/json"
	"github.com/fixora/kubectl-fixora/internal/kube"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"strings"
	"testing"
)

type candidateReader struct {
	evidenceReader
	workload map[string]any
	hpas     []autoscalingv2.HorizontalPodAutoscaler
	owners   map[string]kube.WorkloadOwner
}

func (r *candidateReader) GetWorkload(context.Context, string, string, string) (map[string]any, error) {
	return r.workload, nil
}
func (r *candidateReader) ListHPAs(context.Context, string) ([]autoscalingv2.HorizontalPodAutoscaler, error) {
	return r.hpas, nil
}
func (r *candidateReader) ResolveOwner(_ context.Context, p corev1.Pod) (kube.WorkloadOwner, error) {
	if r.owners != nil {
		return r.owners[string(p.UID)], nil
	}
	return kube.WorkloadOwner{Kind: "Deployment", Name: "backend", UID: "owner-id"}, nil
}
func candidateFixture(t *testing.T) (*Authority, *candidateReader) {
	t.Helper()
	a, e := loadText(t, validPolicy)
	if e != nil {
		t.Fatal(e)
	}
	p := examplePDB()
	v := intstr.FromInt32(3)
	p.Spec.MinAvailable = &v
	p.Status.CurrentHealthy = 3
	p.Status.DesiredHealthy = 3
	p.Status.ExpectedPods = 3
	pods := []corev1.Pod{}
	for i := 0; i < 3; i++ {
		pod := examplePod()
		pod.Name = string(rune('a' + i))
		pod.UID = types.UID("pod-" + pod.Name)
		pods = append(pods, pod)
	}
	d := appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "team", UID: "owner-id", Generation: 2}, Spec: appsv1.DeploymentSpec{Replicas: ptr32(3), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "api"}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 3, UpdatedReplicas: 3, ReadyReplicas: 3, AvailableReplicas: 3}}
	b, _ := json.Marshal(d)
	var w map[string]any
	json.Unmarshal(b, &w)
	return a, &candidateReader{evidenceReader: evidenceReader{pdb: p, pods: pods, budgets: []policyv1.PodDisruptionBudget{*p.DeepCopy()}}, workload: w}
}
func ptr32(n int32) *int32 { return &n }
func TestPDBPrepareExactCorrections(t *testing.T) {
	for _, kind := range []string{"budget", "selector", "switch", "statefulset"} {
		t.Run(kind, func(t *testing.T) {
			a, r := candidateFixture(t)
			if kind == "selector" {
				r.pdb.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"old": "absent"}}
				r.pdb.Status.CurrentHealthy = 0
				r.pdb.Status.ExpectedPods = 0
			}
			if kind == "switch" {
				r.pdb.Spec.MinAvailable = nil
				v := intstr.FromInt32(0)
				r.pdb.Spec.MaxUnavailable = &v
			}
			if kind == "statefulset" {
				a, e := loadText(t, strings.Replace(validPolicy, "kind: Deployment", "kind: StatefulSet", 1))
				if e != nil {
					t.Fatal(e)
				}
				_ = a
				s := appsv1.StatefulSet{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"}, ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "team", UID: "owner-id", Generation: 2}, Spec: appsv1.StatefulSetSpec{Replicas: ptr32(3), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "api"}}, UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType}}, Status: appsv1.StatefulSetStatus{ObservedGeneration: 2, Replicas: 3, ReadyReplicas: 3, CurrentReplicas: 3, UpdatedReplicas: 3, CurrentRevision: "rev", UpdateRevision: "rev"}}
				b, _ := json.Marshal(s)
				json.Unmarshal(b, &r.workload)
				r.owners = map[string]kube.WorkloadOwner{}
				for _, p := range r.pods {
					r.owners[string(p.UID)] = kube.WorkloadOwner{Kind: "StatefulSet", Name: "backend", UID: "owner-id"}
				}
				p, e := Prepare(context.Background(), r, a, "")
				if e != nil || p.proof == nil {
					t.Fatalf("stateful candidate %+v %v", p, e)
				}
				return
			}
			r.budgets = []policyv1.PodDisruptionBudget{*r.pdb.DeepCopy()}
			p, e := Prepare(context.Background(), r, a, "")
			if e != nil || p.proof == nil {
				t.Fatalf("candidate %+v %v", p, e)
			}
			raw, e := p.PatchYAML()
			if e != nil || !strings.Contains(raw, "minAvailable: 2") || strings.Contains(raw, "intVal") {
				t.Fatalf("invalid patch %q %v", raw, e)
			}
			if kind == "switch" && !strings.Contains(raw, "maxUnavailable: null") {
				t.Fatal("opposite field not removed", raw)
			}
		})
	}
	a, r := candidateFixture(t)
	r.pdb.Spec.MinAvailable = func() *intstr.IntOrString { v := intstr.FromInt32(2); return &v }()
	r.budgets = []policyv1.PodDisruptionBudget{*r.pdb.DeepCopy()}
	p, e := Prepare(context.Background(), r, a, "")
	if e == nil && p.proof != nil {
		t.Fatal("no-op authorized")
	}
}
func TestPDBPrepareRefusesUnsafeMembership(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*candidateReader)
	}{
		{"unready", func(r *candidateReader) { r.pods[0].Status.Conditions = nil }},
		{"terminating", func(r *candidateReader) { now := metav1.Now(); r.pods[0].DeletionTimestamp = &now }},
		{"partial current selection", func(r *candidateReader) {
			r.pdb.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"one": "yes"}}
			r.pods[0].Labels["one"] = "yes"
		}},
		{"unrelated", func(r *candidateReader) { r.owners = map[string]kube.WorkloadOwner{} }},
		{"stale", func(r *candidateReader) { r.pdb.Status.ObservedGeneration = 0 }},
		{"active disruption", func(r *candidateReader) { r.pdb.Status.DisruptedPods = map[string]metav1.Time{"a": metav1.Now()} }},
		{"rollout", func(r *candidateReader) { r.workload["status"].(map[string]any)["updatedReplicas"] = float64(2) }},
		{"paused", func(r *candidateReader) { r.workload["spec"].(map[string]any)["paused"] = true }},
		{"hpa", func(r *candidateReader) {
			r.hpas = []autoscalingv2.HorizontalPodAutoscaler{{ObjectMeta: metav1.ObjectMeta{Name: "scale", Namespace: "team", UID: "hpa"}, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "backend"}}}}
		}},
		{"overlap", func(r *candidateReader) {
			p := r.pdb.DeepCopy()
			p.Name = "other"
			p.UID = "other"
			r.budgets = append(r.budgets, *p)
		}},
		{"denied pods", func(r *candidateReader) { r.denied = "pods" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, r := candidateFixture(t)
			tc.edit(r)
			p, e := Prepare(context.Background(), r, a, "")
			if e == nil && p.proof != nil {
				t.Fatal("unsafe membership authorized", p)
			}
		})
	}
}
func TestPDBCandidateAndIntentBindings(t *testing.T) {
	a, r := candidateFixture(t)
	p, e := Prepare(context.Background(), r, a, "apiVersion: policy/v1\nkind: PodDisruptionBudget\nmetadata: {name: protect, namespace: team}\nspec: {minAvailable: 1}\n")
	if e == nil && p.proof != nil {
		t.Fatal("AI exceeded approved intent")
	}
	p, e = Prepare(context.Background(), r, a, "")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "owner-id") || strings.Contains(string(b), a.path) {
		t.Fatal("serialized private authority")
	}
	r.pdb.Labels = map[string]string{"app.kubernetes.io/managed-by": "Helm"}
	r.budgets = []policyv1.PodDisruptionBudget{*r.pdb.DeepCopy()}
	p, e = Prepare(context.Background(), r, a, "")
	if e != nil || p.Status != "source-review-required" {
		t.Fatalf("source ownership lost: %+v %v", p, e)
	}
}
