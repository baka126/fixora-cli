package pdbfix

import (
	"context"
	"encoding/json"
	"errors"
	policyv1 "k8s.io/api/policy/v1"
	"sync"
)

type Receipt struct {
	mu      sync.Mutex
	proof   *proof
	binding [32]byte
	used    bool
}

func (r *Receipt) consume(p Plan) error {
	if r == nil {
		return errors.New("PDB preflight receipt required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := p.check(); e != nil {
		return e
	}
	if r.used || r.proof == nil || r.proof != p.proof || r.binding != p.proof.planBinding {
		return errors.New("PDB receipt invalid or already consumed")
	}
	r.used = true
	return nil
}

type patchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func correctionPatch(current, desired *policyv1.PodDisruptionBudget, fields []string) ([]byte, error) {
	ops := []patchOp{{Op: "test", Path: "/metadata/uid", Value: string(current.UID)}, {Op: "test", Path: "/metadata/resourceVersion", Value: current.ResourceVersion}}
	for _, f := range fields {
		v := specValue(desired, f)
		if v == nil {
			if specValue(current, f) != nil {
				ops = append(ops, patchOp{Op: "remove", Path: "/spec/" + f})
			}
		} else {
			ops = append(ops, patchOp{Op: "add", Path: "/spec/" + f, Value: v})
		}
	}
	return json.Marshal(ops)
}
func admissionIntent(p *policyv1.PodDisruptionBudget) any {
	c := p.DeepCopy()
	c.Generation = 0
	return pdbIntent(c)
}
func Verify(parent context.Context, r Runtime, p Plan) (*Receipt, error) {
	ctx, cancel := operationContext(parent)
	defer cancel()
	if e := p.check(); e != nil {
		return nil, e
	}
	if p.proof.sourceManaged {
		return nil, errors.New("source-managed PDB requires reviewed source delivery")
	}
	s, e := p.fresh(ctx, r, p.proof.original)
	if e != nil {
		return nil, e
	}
	raw, e := correctionPatch(s.PDB, p.proof.candidate, p.ChangedFields)
	if e != nil {
		return nil, e
	}
	admitted, e := r.PatchPDB(ctx, p.Namespace, s.PDB.Name, raw, true)
	if e != nil {
		return nil, e
	}
	if admitted == nil || hash(admissionIntent(admitted)) != hash(admissionIntent(p.proof.candidate)) {
		return nil, errors.New("PDB admission changed the approved intent or metadata")
	}
	if _, e = p.fresh(ctx, r, p.proof.original); e != nil {
		return nil, e
	}
	return &Receipt{proof: p.proof, binding: p.proof.planBinding}, nil
}
