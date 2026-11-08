package pdbfix

import (
	"context"
	"errors"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sort"
)

type proof struct {
	authority           *Authority
	original, candidate *policyv1.PodDisruptionBudget
	snapshot            Snapshot
	target              Target
	sourceManaged       bool
	planBinding         [32]byte
}

func (p Plan) check() error {
	if p.proof == nil || hash(p) != p.proof.planBinding {
		return errors.New("PDB plan is unverified or altered")
	}
	return p.proof.authority.check()
}
func normalizeMeta(m metav1.ObjectMeta) metav1.ObjectMeta {
	m.ResourceVersion = ""
	m.ManagedFields = nil
	m.CreationTimestamp = metav1.Time{}
	return m
}
func pdbIntent(p *policyv1.PodDisruptionBudget) any {
	return struct {
		Meta metav1.ObjectMeta
		Spec policyv1.PodDisruptionBudgetSpec
	}{normalizeMeta(p.ObjectMeta), p.Spec}
}
func evidenceBinding(s Snapshot, a *Authority) [32]byte {
	ps := selectedPods(s, a)
	sort.Slice(ps, func(i, j int) bool { return string(ps[i].UID) < string(ps[j].UID) })
	var pods []any
	for _, p := range ps {
		pods = append(pods, struct {
			Meta  metav1.ObjectMeta
			Spec  any
			Ready bool
			Owner any
		}{normalizeMeta(p.ObjectMeta), p.Spec, ready(p), s.Owners[string(p.UID)]})
	}
	var budgets []any
	bs := append([]policyv1.PodDisruptionBudget{}, s.Budgets...)
	sort.Slice(bs, func(i, j int) bool { return string(bs[i].UID) < string(bs[j].UID) })
	for _, b := range bs {
		if b.UID != s.PDB.UID {
			budgets = append(budgets, pdbIntent(&b))
		}
	}
	w := map[string]any{}
	for k, v := range s.Workload {
		if k != "status" {
			w[k] = v
		}
	}
	if m, ok := w["metadata"].(map[string]any); ok {
		c := map[string]any{}
		for k, v := range m {
			if k != "resourceVersion" && k != "managedFields" && k != "creationTimestamp" {
				c[k] = v
			}
		}
		w["metadata"] = c
	}
	hs := append([]any{}, nil...)
	for _, h := range s.HPAs {
		hs = append(hs, struct {
			Meta metav1.ObjectMeta
			Spec any
		}{normalizeMeta(h.ObjectMeta), h.Spec})
	}
	sort.Slice(hs, func(i, j int) bool { return stringHash(hs[i]) < stringHash(hs[j]) })
	return hash(struct {
		Pods, Budgets, HPAs []any
		Workload            any
	}{pods, budgets, hs, w})
}
func stringHash(v any) string { h := hash(v); return string(h[:]) }
func (p Plan) fresh(ctx context.Context, r Reader, expected *policyv1.PodDisruptionBudget) (Snapshot, error) {
	if e := p.check(); e != nil {
		return Snapshot{}, e
	}
	pr := p.proof
	s, e := Collect(ctx, r, p.Namespace, pr.original.Name, &pr.target)
	if e != nil {
		return s, e
	}
	if hash(pdbIntent(s.PDB)) != hash(pdbIntent(expected)) {
		return s, errors.New("PDB identity, metadata or policy drifted")
	}
	if e = eligible(s, pr.authority, pr.candidate); e != nil {
		return s, e
	}
	if evidenceBinding(s, pr.authority) != evidenceBinding(pr.snapshot, pr.authority) {
		return s, errors.New("workload, Pod, HPA or overlapping-policy evidence changed")
	}
	return s, nil
}
