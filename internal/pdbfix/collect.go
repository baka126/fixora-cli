package pdbfix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func Collect(ctx context.Context, r Reader, ns, name string, target *Target) (Snapshot, error) {
	s := Snapshot{Owners: map[string]kube.WorkloadOwner{}}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	if r == nil {
		return s, errors.New("PDB reader required")
	}
	p, err := r.GetPDB(ctx, ns, name)
	if err != nil {
		return s, fmt.Errorf("read PDB: %w", err)
	}
	if p == nil || p.Name != name || p.Namespace != ns || p.UID == "" || p.ResourceVersion == "" {
		return s, errors.New("PDB identity unavailable")
	}
	s.PDB = p.DeepCopy()
	issue := func(code, res string, e error) { s.Issues = append(s.Issues, EvidenceIssue{code, res, e.Error()}) }
	if target != nil {
		w, e := r.GetWorkload(ctx, ns, target.Kind, target.Name)
		if e != nil || w == nil {
			if e == nil {
				e = errors.New("missing workload")
			}
			issue("workload-unreadable", target.Kind+"/"+target.Name, e)
		} else {
			b, e := json.Marshal(w)
			if e == nil {
				e = json.Unmarshal(b, &s.Workload)
			}
			if e != nil {
				issue("workload-invalid", target.Name, e)
			}
		}
	}
	pods, e := r.ListPods(ctx, ns)
	if e != nil {
		issue("pods-unreadable", ns, e)
	} else {
		seen := map[string]bool{}
		names := map[string]bool{}
		for _, pod := range pods {
			if pod.Namespace != ns || pod.UID == "" || pod.Name == "" || seen[string(pod.UID)] || names[pod.Name] {
				issue("pods-invalid", ns, errors.New("duplicate or invalid Pod identity"))
				continue
			}
			seen[string(pod.UID)] = true
			names[pod.Name] = true
			s.Pods = append(s.Pods, *pod.DeepCopy())
			if selected(s.PDB.Spec.Selector, pod.Labels) || target != nil {
				o, e := r.ResolveOwner(ctx, pod)
				if e != nil {
					if selected(s.PDB.Spec.Selector, pod.Labels) || workloadSelects(s.Workload, pod.Labels) {
						issue("owner-unreadable", pod.Name, e)
					}
				} else {
					s.Owners[string(pod.UID)] = o
				}
			}
		}
	}
	budgets, e := r.ListPDBs(ctx, ns)
	if e != nil {
		issue("budgets-unreadable", ns, e)
	} else {
		seen := map[string]bool{}
		names := map[string]bool{}
		for _, b := range budgets {
			if b.Namespace != ns || b.UID == "" || b.Name == "" || seen[string(b.UID)] || names[b.Name] {
				issue("budgets-invalid", ns, errors.New("duplicate or invalid PDB identity"))
				continue
			}
			seen[string(b.UID)] = true
			names[b.Name] = true
			if _, e := selector(b.Spec.Selector); e != nil {
				issue("selector-invalid", b.Name, e)
			}
			s.Budgets = append(s.Budgets, *b.DeepCopy())
		}
	}
	hpas, e := r.ListHPAs(ctx, ns)
	if e != nil {
		issue("hpas-unreadable", ns, e)
	} else {
		seen := map[string]bool{}
		for _, h := range hpas {
			if h.Namespace != ns || h.Name == "" || h.UID == "" || seen[h.Name] {
				issue("hpas-invalid", ns, errors.New("duplicate or invalid HPA identity"))
				continue
			}
			seen[h.Name] = true
			s.HPAs = append(s.HPAs, *h.DeepCopy())
		}
	}
	if _, e := selector(s.PDB.Spec.Selector); e != nil {
		issue("selector-invalid", name, e)
	}
	return s, nil
}
func selector(s *metav1.LabelSelector) (labels.Selector, error) {
	return metav1.LabelSelectorAsSelector(s)
}
func selected(s *metav1.LabelSelector, l map[string]string) bool {
	sel, e := selector(s)
	return e == nil && sel.Matches(labels.Set(l))
}
func workloadSelects(w map[string]any, l map[string]string) bool {
	if w == nil {
		return false
	}
	spec, _ := w["spec"].(map[string]any)
	b, e := json.Marshal(spec["selector"])
	if e != nil {
		return false
	}
	var s metav1.LabelSelector
	if json.Unmarshal(b, &s) != nil {
		return false
	}
	return selected(&s, l)
}
