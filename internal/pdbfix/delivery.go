package pdbfix

import (
	"context"
	"errors"
	"fmt"
	policyv1 "k8s.io/api/policy/v1"
	"sync"
)

type Outcome struct {
	Status   string   `json:"status" yaml:"status"`
	Scope    string   `json:"scope" yaml:"scope"`
	Summary  string   `json:"summary" yaml:"summary"`
	Warnings []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}
type Applied struct {
	mu               sync.Mutex
	plan             Plan
	original, actual *policyv1.PodDisruptionBudget
	binding          [32]byte
	restoring        bool
}

func (a *Applied) seal() [32]byte {
	if a == nil {
		return [32]byte{}
	}
	return hash(struct {
		Original, Actual any
		Plan             [32]byte
	}{pdbIntent(a.original), pdbIntent(a.actual), a.plan.proof.planBinding})
}
func (a *Applied) check() error {
	if a == nil || a.original == nil || a.actual == nil || a.plan.proof == nil || a.seal() != a.binding {
		return errors.New("confirmed PDB apply handle required")
	}
	return nil
}
func Apply(parent context.Context, r Runtime, p Plan, receipt *Receipt) (*Applied, Outcome, error) {
	ctx, cancel := operationContext(parent)
	defer cancel()
	out := Outcome{Status: "unchanged", Scope: "unverified", Summary: "No confirmed PDB write."}
	if e := p.check(); e != nil {
		return nil, out, e
	}
	if p.proof.sourceManaged {
		return nil, out, errors.New("source-managed PDB cannot be changed directly")
	}
	if e := receipt.consume(p); e != nil {
		return nil, out, e
	}
	s, e := p.fresh(ctx, r, p.proof.original)
	if e != nil {
		return nil, out, e
	}
	raw, e := correctionPatch(s.PDB, p.proof.candidate, p.ChangedFields)
	if e != nil {
		return nil, out, e
	}
	actual, e := r.PatchPDB(ctx, p.Namespace, s.PDB.Name, raw, false)
	if e != nil {
		return recoverWrite(ctx, r, p, s.PDB, e)
	}
	out = Outcome{Status: "outcome-uncertain", Scope: "unverified", Summary: "A PDB write was requested; the response does not establish the approved result. Inspect the live policy. No automatic retry or rollback."}
	if actual == nil || actual.ResourceVersion == "" || actual.Generation <= s.PDB.Generation || hash(admissionIntent(actual)) != hash(admissionIntent(p.proof.candidate)) {
		return nil, out, errors.New("PDB write response differs from approved candidate or lacks updated identity")
	}
	a := &Applied{plan: p, original: s.PDB.DeepCopy(), actual: actual.DeepCopy()}
	a.binding = a.seal()
	return a, Outcome{Status: "applied", Scope: "policy-written", Summary: "Approved PDB correction applied; controller convergence has not yet been verified."}, nil
}
func recoverWrite(ctx context.Context, r Runtime, p Plan, before *policyv1.PodDisruptionBudget, writeErr error) (*Applied, Outcome, error) {
	out := Outcome{Status: "outcome-uncertain", Scope: "unverified", Summary: "PDB write response was lost; inspect the policy. No automatic retry or rollback."}
	now, e := r.GetPDB(ctx, p.Namespace, before.Name)
	if e != nil || now == nil {
		return nil, out, fmt.Errorf("PDB write outcome unreadable: %w", writeErr)
	}
	if now.UID != before.UID {
		out.Status = "conflict"
		out.Summary = "PDB identity changed after an unsuccessful write response."
	} else if hash(admissionIntent(now)) == hash(admissionIntent(p.proof.candidate)) {
		out.Summary = "The approved candidate is observed, but the lost response leaves write authorship uncertain. No automatic retry or rollback."
	} else if hash(admissionIntent(now)) == hash(admissionIntent(before)) {
		out.Status = "unchanged"
		out.Summary = "The original policy is still observed after the unsuccessful write response."
	} else {
		out.Status = "conflict"
		out.Summary = "A conflicting policy is observed after the unsuccessful write response."
	}
	return nil, out, writeErr
}
