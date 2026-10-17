package pdbfix

import (
	"context"
	"errors"
	"github.com/fixora/kubectl-fixora/internal/kube"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"testing"
)

func examplePDB() *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: "protect", Namespace: "team", UID: "budget-id", ResourceVersion: "7", Generation: 1}, Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "api"}}}, Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1, CurrentHealthy: 1, DesiredHealthy: 1, ExpectedPods: 1, DisruptionsAllowed: 0}}
}
func examplePod() corev1.Pod {
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-a", Namespace: "team", UID: "pod-id", Labels: map[string]string{"role": "api"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
}
func baseSnapshot() Snapshot {
	return Snapshot{PDB: examplePDB(), Pods: []corev1.Pod{examplePod()}, Owners: map[string]kube.WorkloadOwner{"pod-id": {Kind: "Deployment", Name: "backend", UID: "owner-id"}}}
}
func hasCode(ds []Diagnosis, code string) bool {
	for _, d := range ds {
		if d.Code == code {
			return true
		}
	}
	return false
}
func TestPDBDiagnosisClassifications(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		edit       func(*Snapshot)
	}{
		{"intentional zero", "DisruptionProtectionActive", func(s *Snapshot) {}},
		{"nil selector", "PDBNoSelectedPods", func(s *Snapshot) { s.PDB.Spec.Selector = nil }},
		{"empty selector", "DisruptionProtectionActive", func(s *Snapshot) { s.PDB.Spec.Selector = &metav1.LabelSelector{} }},
		{"expression", "DisruptionProtectionActive", func(s *Snapshot) {
			s.PDB.Spec.Selector = &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "role", Operator: metav1.LabelSelectorOpIn, Values: []string{"api"}}}}
		}},
		{"unhealthy", "DisruptionsBlocked", func(s *Snapshot) { s.PDB.Status.CurrentHealthy = 0; s.Pods[0].Status.Conditions = nil }},
		{"stale", "PDBStatusStale", func(s *Snapshot) { s.PDB.Status.ObservedGeneration = 0 }},
		{"failed", "PDBReconciliationFailed", func(s *Snapshot) {
			s.PDB.Status.Conditions = []metav1.Condition{{Type: "DisruptionAllowed", Status: metav1.ConditionFalse, Reason: "SyncFailed"}}
		}},
		{"overlap", "PDBOverlap", func(s *Snapshot) {
			p := s.PDB.DeepCopy()
			p.UID = "other"
			p.Name = "other"
			s.Budgets = []policyv1.PodDisruptionBudget{*p}
		}},
		{"unknown owner", "PDBUnsupportedOwnership", func(s *Snapshot) { s.Owners = nil }},
		{"read denied", "PDBEvidenceUnavailable", func(s *Snapshot) { s.Issues = []EvidenceIssue{{Code: "pods-unreadable", Detail: "forbidden"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSnapshot()
			tc.edit(&s)
			ds := Diagnose(s)
			if !hasCode(ds, tc.want) {
				t.Fatalf("want %s got %+v", tc.want, ds)
			}
			if tc.want == "PDBStatusStale" && hasCode(ds, "DisruptionProtectionActive") {
				t.Fatal("trusted stale counts")
			}
		})
	}
	s := baseSnapshot()
	s.PDB.Status.CurrentHealthy = 0
	s.Pods[0].Status.Conditions = nil
	p := s.PDB.DeepCopy()
	p.UID = "other"
	s.Budgets = []policyv1.PodDisruptionBudget{*p}
	ds := Diagnose(s)
	if !hasCode(ds, "PDBOverlap") || !hasCode(ds, "DisruptionsBlocked") {
		t.Fatal("lost simultaneous causes", ds)
	}
}

type evidenceReader struct {
	pdb     *policyv1.PodDisruptionBudget
	pods    []corev1.Pod
	budgets []policyv1.PodDisruptionBudget
	denied  string
}

func (r evidenceReader) GetPDB(context.Context, string, string) (*policyv1.PodDisruptionBudget, error) {
	if r.denied == "target" {
		return nil, errors.New("forbidden")
	}
	return r.pdb.DeepCopy(), nil
}
func (r evidenceReader) GetWorkload(context.Context, string, string, string) (map[string]any, error) {
	return nil, nil
}
func (r evidenceReader) ListPods(context.Context, string) ([]corev1.Pod, error) {
	if r.denied == "pods" {
		return nil, errors.New("forbidden")
	}
	return r.pods, nil
}
func (r evidenceReader) ListPDBs(context.Context, string) ([]policyv1.PodDisruptionBudget, error) {
	if r.denied == "budgets" {
		return nil, errors.New("forbidden")
	}
	return r.budgets, nil
}
func (r evidenceReader) ListHPAs(context.Context, string) ([]autoscalingv2.HorizontalPodAutoscaler, error) {
	if r.denied == "hpas" {
		return nil, errors.New("forbidden")
	}
	return nil, nil
}
func (r evidenceReader) ResolveOwner(context.Context, corev1.Pod) (kube.WorkloadOwner, error) {
	return kube.WorkloadOwner{Kind: "Deployment", Name: "backend", UID: "owner-id"}, nil
}
func TestPDBReadErrorsRemainUnavailable(t *testing.T) {
	for _, kind := range []string{"pods", "budgets", "hpas", "target"} {
		t.Run(kind, func(t *testing.T) {
			r := evidenceReader{pdb: examplePDB(), denied: kind}
			s, err := Collect(context.Background(), r, "team", "protect", nil)
			if kind == "target" {
				if err == nil {
					t.Fatal("lost target error")
				}
				return
			}
			if err != nil || len(s.Issues) == 0 || !hasCode(Diagnose(s), "PDBEvidenceUnavailable") {
				t.Fatalf("lost unavailable evidence: %+v %v", s, err)
			}
		})
	}
	r := evidenceReader{pdb: examplePDB(), pods: []corev1.Pod{examplePod(), examplePod()}}
	s, err := Collect(context.Background(), r, "team", "protect", nil)
	if err != nil || len(s.Issues) == 0 {
		t.Fatal("duplicate Pod inventory accepted")
	}
	r.pods[1].UID = types.UID("")
	s, _ = Collect(context.Background(), r, "team", "protect", nil)
	if len(s.Issues) == 0 {
		t.Fatal("missing UID accepted")
	}
}
