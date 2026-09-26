package infer

import (
	"context"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

type stubInferrer struct {
	name    string
	handles bool
	result  Result
	ok      bool
}

func (s stubInferrer) Name() string            { return s.name }
func (s stubInferrer) Handles(_ fix.Plan) bool { return s.handles }
func (s stubInferrer) Infer(_ context.Context, _ kubeReader, _ analyzer.Finding, _ fix.Plan) (Result, bool, error) {
	return s.result, s.ok, nil
}

func TestConcreteReturnsOriginalWhenNoInferrerHandles(t *testing.T) {
	plan := fix.Plan{Strategy: "image", PatchTemplate: "image: TODO_PINNED_MULTI_ARCH_IMAGE"}
	got, ok := concreteWith(context.Background(), nil, analyzer.Finding{}, plan, nil)
	if ok {
		t.Fatal("no inferrer registered, want ok=false")
	}
	if got.PatchTemplate != plan.PatchTemplate {
		t.Fatal("plan must be returned unchanged")
	}
}

func TestConcreteReturnsOriginalWhenInferrerDeclines(t *testing.T) {
	plan := fix.Plan{Strategy: "image", PatchTemplate: "image: TODO_PINNED_MULTI_ARCH_IMAGE"}
	set := []Inferrer{stubInferrer{name: "image", handles: true, ok: false}}
	got, ok := concreteWith(context.Background(), nil, analyzer.Finding{}, plan, set)
	if ok || got.PatchTemplate != plan.PatchTemplate {
		t.Fatal("declining inferrer must leave the plan untouched")
	}
}

func TestConcreteReturnsOriginalWhenResultNotApplyEligible(t *testing.T) {
	// A "runtime" plan is review-only in validateConcretePatch, so even a
	// fully substituted patch must not be adopted.
	plan := fix.BuildPlan(analyzer.Finding{
		ResourceKind: "Deployment", ResourceName: "d", Namespace: "n", Status: "CrashLoopBackOff",
	})
	set := []Inferrer{stubInferrer{
		name: "runtime", handles: true, ok: true,
		result: Result{Options: fix.ConcreteOptions{Container: "app"}},
	}}
	got, ok := concreteWith(context.Background(), nil, analyzer.Finding{}, plan, set)
	if ok {
		t.Fatal("non-apply-eligible result must not be adopted")
	}
	if got.PatchTemplate != plan.PatchTemplate {
		t.Fatal("plan must be returned unchanged")
	}
}
