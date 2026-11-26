package pdbfix

import (
	"context"
	"errors"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"
	"testing"
)

func TestReviewArgoLabelCannotAuthorizeDirectRepair(t *testing.T) {
	a, r := candidateFixture(t)
	r.pdb.Labels = map[string]string{"app.kubernetes.io/instance": "argo-application"}
	r.budgets = []policyv1.PodDisruptionBudget{*r.pdb.DeepCopy()}
	p, e := Prepare(context.Background(), r, a, "")
	if e != nil {
		t.Fatal(e)
	}
	if p.Status != "source-review-required" {
		t.Errorf("ambiguous source ownership authorized: %s", p.Status)
	}
	if rec, e := Verify(context.Background(), &runtimeFixture{candidateReader: r}, p); e == nil || rec != nil {
		t.Fatal("Argo tracking label got direct authority")
	}
}
func TestReviewReceiptValueCopyCannotRetryWrite(t *testing.T) {
	r, p, receipt := preparedRuntime(t)
	var copied Receipt
	reflect.ValueOf(&copied).Elem().Set(reflect.ValueOf(receipt).Elem())
	r.writeErr = errors.New("write transport failure")
	if _, _, e := Apply(context.Background(), r, p, receipt); e == nil {
		t.Fatal("expected transport error")
	}
	if _, _, e := Apply(context.Background(), r, p, &copied); e == nil {
		t.Fatal("copied receipt reused")
	}
	if r.writes != 1 {
		t.Fatalf("copied receipt made %d write attempts, want 1", r.writes)
	}
}
func TestReviewPublicPlanCannotChangeRollback(t *testing.T) {
	a, cr := candidateFixture(t)
	cr.pdb.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"obsolete": "broken"}}
	cr.pdb.Status.CurrentHealthy = 0
	cr.pdb.Status.ExpectedPods = 0
	cr.budgets = []policyv1.PodDisruptionBudget{*cr.pdb.DeepCopy()}
	original := cr.pdb.Spec.Selector.DeepCopy()
	r := &runtimeFixture{candidateReader: cr}
	p, e := Prepare(context.Background(), r, a, "")
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := Verify(context.Background(), r, p)
	if e != nil {
		t.Fatal(e)
	}
	ap, _, e := Apply(context.Background(), r, p, receipt)
	if e != nil {
		t.Fatal(e)
	}
	for i := range p.ChangedFields {
		p.ChangedFields[i] = "minAvailable"
	}
	out, e := Restore(context.Background(), r, ap)
	if e != nil {
		t.Fatal(out, e)
	}
	if !reflect.DeepEqual(r.pdb.Spec.Selector, original) {
		t.Fatalf("claimed %s without restoring original selector: %+v", out.Status, r.pdb.Spec.Selector)
	}
}
