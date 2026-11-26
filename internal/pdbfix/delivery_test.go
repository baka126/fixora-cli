package pdbfix

import (
	"context"
	"errors"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"testing"
	"time"
)

func preparedRuntime(t *testing.T) (*runtimeFixture, Plan, *Receipt) {
	t.Helper()
	a, cr := candidateFixture(t)
	r := &runtimeFixture{candidateReader: cr}
	p, e := Prepare(context.Background(), r, a, "")
	if e != nil {
		t.Fatal(e)
	}
	v, e := Verify(context.Background(), r, p)
	if e != nil {
		t.Fatal(e)
	}
	return r, p, v
}
func TestPDBApplyAndObserve(t *testing.T) {
	r, p, v := preparedRuntime(t)
	ap, out, e := Apply(context.Background(), r, p, v)
	if e != nil || ap == nil || out.Status != "applied" || r.writes != 1 {
		t.Fatalf("apply %v %+v", e, out)
	}
	out, e = Observe(context.Background(), r, p, ap)
	if e != nil || out.Status != "policy-converged" {
		t.Fatalf("observe %v %+v", e, out)
	}
	if _, _, e = Apply(context.Background(), r, p, v); e == nil {
		t.Fatal("replayed apply")
	}
	if r.writes != 1 {
		t.Fatal("receipt replay wrote")
	}
}
func TestPDBLostResponseAndPartialOutcomes(t *testing.T) {
	r, p, v := preparedRuntime(t)
	r.lose = true
	ap, out, e := Apply(context.Background(), r, p, v)
	if e == nil || ap != nil || out.Status != "outcome-uncertain" || r.writes != 1 || r.pdb.Spec.MinAvailable.IntVal != 2 {
		t.Fatalf("lost write misreported %+v %v", out, e)
	}
	r, p, v = preparedRuntime(t)
	r.writeErr = errors.New("transport failure")
	_, out, e = Apply(context.Background(), r, p, v)
	if e == nil || out.Status != "unchanged" || r.writes != 1 {
		t.Fatal(out, e)
	}
	r, p, v = preparedRuntime(t)
	ap, _, e = Apply(context.Background(), r, p, v)
	if e != nil {
		t.Fatal(e)
	}
	r.pdb.Status.ObservedGeneration = 0
	r.budgets = []policyv1.PodDisruptionBudget{*r.pdb.DeepCopy()}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	out, e = Observe(ctx, r, p, ap)
	if !errors.Is(e, context.DeadlineExceeded) || out.Status != "observation-failed" {
		t.Fatal(out, e)
	}
}
func TestPDBRestoreFieldGuards(t *testing.T) {
	r, p, v := preparedRuntime(t)
	ap, _, e := Apply(context.Background(), r, p, v)
	if e != nil {
		t.Fatal(e)
	}
	r.pdb.Annotations = map[string]string{"unrelated": "keep"}
	r.pdb.Spec.UnhealthyPodEvictionPolicy = func() *policyv1.UnhealthyPodEvictionPolicyType { v := policyv1.IfHealthyBudget; return &v }()
	out, e := Restore(context.Background(), r, ap)
	if e != nil || out.Status != "restored" || r.pdb.Spec.MinAvailable.IntVal != 3 || r.pdb.Annotations["unrelated"] != "keep" || r.pdb.Spec.UnhealthyPodEvictionPolicy == nil {
		t.Fatalf("inverse failed %+v %v", out, e)
	}
	for _, mode := range []string{"edited field", "uid", "ownership", "rewrite"} {
		t.Run(mode, func(t *testing.T) {
			r, p, v := preparedRuntime(t)
			ap, _, e := Apply(context.Background(), r, p, v)
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "edited field":
				x := intstr.FromInt32(1)
				r.pdb.Spec.MinAvailable = &x
			case "uid":
				r.pdb.UID = "replacement"
			case "ownership":
				r.pdb.OwnerReferences = []metav1.OwnerReference{{Kind: "Operator", Name: "new", UID: "new"}}
			case "rewrite":
				r.rewrite = true
			}
			out, e := Restore(context.Background(), r, ap)
			if e == nil || out.Status == "restored" {
				t.Fatal("conflicting rollback accepted", out, e)
			}
		})
	}
}
