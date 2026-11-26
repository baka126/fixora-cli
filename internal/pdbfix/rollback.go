package pdbfix

import (
	"context"
	"errors"
	policyv1 "k8s.io/api/policy/v1"
)

func Restore(parent context.Context, r Runtime, a *Applied) (Outcome, error) {
	ctx, cancel := operationContext(parent)
	defer cancel()
	out := Outcome{Status: "conflict", Scope: "unverified", Summary: "Original PDB policy was not confirmed restored."}
	if e := a.check(); e != nil {
		return out, e
	}
	a.mu.Lock()
	if a.restoring {
		a.mu.Unlock()
		return out, errors.New("PDB rollback already attempted")
	}
	a.restoring = true
	a.mu.Unlock()
	current, e := r.GetPDB(ctx, a.actual.Namespace, a.actual.Name)
	if e != nil {
		return out, e
	}
	if current == nil || current.UID != a.actual.UID || current.Name != a.actual.Name || current.Namespace != a.actual.Namespace || current.DeletionTimestamp != nil || sourceOwned(current) {
		return out, errors.New("rollback target replaced, deleting or source-owned")
	}
	for _, f := range a.plan.ChangedFields {
		if hash(specValue(current, f)) != hash(specValue(a.actual, f)) {
			return out, errors.New("another actor changed a field required for rollback")
		}
	}
	desired := current.DeepCopy()
	for _, f := range a.plan.ChangedFields {
		switch f {
		case "selector":
			desired.Spec.Selector = a.original.Spec.Selector.DeepCopy()
		case "minAvailable":
			desired.Spec.MinAvailable = a.original.Spec.MinAvailable
		case "maxUnavailable":
			desired.Spec.MaxUnavailable = a.original.Spec.MaxUnavailable
		}
	}
	raw, e := correctionPatch(current, desired, a.plan.ChangedFields)
	if e != nil {
		return out, e
	}
	actual, e := r.PatchPDB(ctx, current.Namespace, current.Name, raw, false)
	out = Outcome{Status: "restore-observation-failed", Scope: "restore-requested", Summary: "Original PDB policy restoration was requested; inspect the policy. No automatic retry."}
	if e != nil {
		return out, e
	}
	if actual == nil || actual.ResourceVersion == "" || actual.Generation <= current.Generation || hash(admissionIntent(actual)) != hash(admissionIntent(desired)) {
		return out, errors.New("rollback response differs from field-only inverse")
	}
	return observeRestored(ctx, r, actual)
}
func observeRestored(ctx context.Context, r Runtime, expected *policyv1.PodDisruptionBudget) (Outcome, error) {
	out := Outcome{Status: "restore-observation-failed", Scope: "original-fields-written", Summary: "Original PDB fields were written; reconciliation is not established."}
	for {
		p, e := r.GetPDB(ctx, expected.Namespace, expected.Name)
		if e != nil {
			return out, e
		}
		if p == nil || hash(pdbIntent(p)) != hash(pdbIntent(expected)) {
			return out, errors.New("PDB drifted during restore observation")
		}
		ok, e := reconciled(p)
		if e != nil {
			return out, e
		}
		if ok {
			return Outcome{Status: "restored", Scope: "original-policy-reconciled", Summary: "Original policy fields restored and reconciled. This does not certify the original policy meets the repair's approved availability limits."}, nil
		}
		if e = waitTick(ctx); e != nil {
			return out, e
		}
	}
}
