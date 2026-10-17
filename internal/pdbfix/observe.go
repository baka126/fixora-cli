package pdbfix

import (
	"context"
	"errors"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"
)

func reconciled(p *policyv1.PodDisruptionBudget) (bool, error) {
	if p.Status.ObservedGeneration != p.Generation {
		return false, nil
	}
	for _, c := range p.Status.Conditions {
		if c.Type == "DisruptionAllowed" {
			if c.Reason == "SyncFailed" {
				return false, errors.New("PDB controller reconciliation failed")
			}
			if c.Status == metav1.ConditionTrue && c.Reason == "SufficientPods" || c.Status == metav1.ConditionFalse && c.Reason == "InsufficientPods" {
				return true, nil
			}
		}
	}
	return false, nil
}
func waitTick(ctx context.Context) error {
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func Observe(parent context.Context, r Runtime, p Plan, a *Applied) (Outcome, error) {
	ctx, cancel := operationContext(parent)
	defer cancel()
	out := Outcome{Status: "observation-failed", Scope: "policy-written", Summary: "PDB write was confirmed; live policy convergence is not established. No automatic rollback."}
	if e := a.check(); e != nil {
		return out, e
	}
	if p.proof != a.plan.proof || hash(p) != hash(a.plan) {
		return out, errors.New("apply handle does not match PDB plan")
	}
	for {
		if e := ctx.Err(); e != nil {
			return out, e
		}
		if e := p.check(); e != nil {
			return out, e
		}
		s, e := Collect(ctx, r, p.Namespace, a.actual.Name, &p.proof.target)
		if e != nil {
			return out, e
		}
		if hash(pdbIntent(s.PDB)) != hash(pdbIntent(a.actual)) {
			return out, errors.New("PDB changed after apply")
		}
		if e = eligibility(s, p.proof.authority, p.proof.candidate, false); e != nil {
			return out, e
		}
		if evidenceBinding(s, p.proof.authority) != evidenceBinding(p.proof.snapshot, p.proof.authority) {
			return out, errors.New("workload, Pod or policy evidence drifted after apply")
		}
		ok, e := reconciled(s.PDB)
		if e != nil {
			return out, e
		}
		if ok {
			n, _, e := stableWorkload(s, p.proof.authority)
			if e != nil {
				return out, e
			}
			effect, e := EvaluateBudget(s.PDB.Spec, n, n)
			if e != nil {
				return out, e
			}
			st := s.PDB.Status
			if st.ExpectedPods == n && st.CurrentHealthy == n && st.DesiredHealthy == effect.RequiredHealthy && st.DisruptionsAllowed == effect.AllowedDisruptions {
				return Outcome{Status: "policy-converged", Scope: "current-generation-policy", Summary: "PDB status agrees with approved availability and healthy selected replicas at this observation. Eviction/drain success and future health are not tested."}, nil
			}
		}
		if e = waitTick(ctx); e != nil {
			return out, e
		}
	}
}
