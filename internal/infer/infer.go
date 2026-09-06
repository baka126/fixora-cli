package infer

import (
	"context"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
)

// kubeReader is an alias so tests can pass nil without importing kube.
type kubeReader = kube.Reader

// Result is one inferrer's proposal plus the provenance shown to the user.
type Result struct {
	Options   fix.ConcreteOptions
	Guardrail string // guardrail token added to the plan
	Warning   string // surfaced in the plan and reviewed before shadow
}

// Inferrer derives concrete patch values for one strategy.
type Inferrer interface {
	Name() string
	Handles(plan fix.Plan) bool
	Infer(ctx context.Context, r kubeReader, f analyzer.Finding, p fix.Plan) (Result, bool, error)
}

// Concrete runs the first registered inferrer that handles the plan. It
// returns the original plan and false unless the concretized result comes back
// ApplyEligible — there is no half-concretized intermediate state.
func Concrete(ctx context.Context, r kubeReader, f analyzer.Finding, p fix.Plan) (fix.Plan, bool) {
	return concreteWith(ctx, r, f, p, registered)
}

func concreteWith(ctx context.Context, r kubeReader, f analyzer.Finding, p fix.Plan, set []Inferrer) (fix.Plan, bool) {
	for _, in := range set {
		if !in.Handles(p) {
			continue
		}
		result, ok, err := in.Infer(ctx, r, f, p)
		if err != nil || !ok {
			continue
		}
		candidate := fix.Concretize(p, result.Options)
		if !candidate.ApplyEligible {
			continue
		}
		if result.Guardrail != "" {
			candidate.Guardrails = append(candidate.Guardrails, result.Guardrail)
		}
		if result.Warning != "" {
			candidate.Warnings = append(candidate.Warnings, result.Warning)
		}
		return candidate, true
	}
	return p, false
}
