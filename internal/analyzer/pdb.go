package analyzer

import (
	"context"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/pdbfix"
)

func (a Analyzer) analyzePDBs(ctx *ScanContext) ([]Finding, error) {
	pdbs, e := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "pdb")
	if e != nil {
		return nil, e
	}
	var out []Finding
	for _, p := range pdbs {
		ns, name := objectNamespaceName(p)
		s, e := pdbfix.Collect(ctx, pdbEvidenceReader{ctx.Reader}, ns, name, nil)
		if e != nil {
			out = append(out, Finding{ID: keyFor(ns, "PodDisruptionBudget/"+name+"/PDBEvidenceUnavailable"), Namespace: ns, ResourceKind: "PodDisruptionBudget", ResourceName: name, Status: "PDBEvidenceUnavailable", Severity: "medium", Category: "policy", Summary: "The PDB could not be read for diagnosis.", Evidence: []Evidence{{Label: "PDB read", Value: e.Error()}}})
			continue
		}
		out = append(out, pdbSnapshotFindings(ctx, s)...)
	}
	return out, nil
}
func pdbSnapshotFindings(ctx *ScanContext, s pdbfix.Snapshot) []Finding {
	ns, name := s.PDB.Namespace, s.PDB.Name
	var out []Finding
	var obj map[string]any
	decodePDBObject(s.PDB, &obj)
	for _, d := range pdbfix.Diagnose(s) {
		f := Finding{ID: keyFor(ns, "PodDisruptionBudget/"+name+"/"+d.Code), Namespace: ns, ResourceKind: "PodDisruptionBudget", ResourceName: name, ResourceUID: string(s.PDB.UID), Status: d.Code, Severity: d.Severity, Category: "policy", Summary: d.Cause, GitOps: gitOpsForObject(obj), Evidence: []Evidence{{Label: "PDB classification", Value: d.Code}}, Recommendations: []Recommendation{{Title: "Review disruption protection", Description: d.Next, PatchType: "pdb", SafeByDefault: false}}}
		if s.PDB.Status.ObservedGeneration == s.PDB.Generation {
			st := s.PDB.Status
			f.Evidence = append(f.Evidence, Evidence{Label: "currentHealthy", Value: fmt.Sprint(st.CurrentHealthy)}, Evidence{Label: "desiredHealthy", Value: fmt.Sprint(st.DesiredHealthy)}, Evidence{Label: "disruptionsAllowed", Value: fmt.Sprint(st.DisruptionsAllowed)}, Evidence{Label: "expectedPods", Value: fmt.Sprint(st.ExpectedPods)})
		}
		for _, i := range s.Issues {
			f.Evidence = append(f.Evidence, Evidence{Label: i.Code + " " + i.Resource, Value: i.Detail})
		}
		if events, e := ctx.GetEvents(); e == nil {
			for _, ev := range events {
				if ev.InvolvedObject.Kind == "PodDisruptionBudget" && ev.InvolvedObject.Name == name && ev.Metadata.Namespace == ns {
					f.Evidence = append(f.Evidence, Evidence{Label: "Event " + ev.Reason, Value: ev.Message})
				}
			}
		}
		out = append(out, f)
	}
	return out
}
func (a Analyzer) pdbResourceFinding(ctx context.Context, name, ns string) (Finding, error) {
	s, e := pdbfix.Collect(ctx, pdbEvidenceReader{a.k}, ns, name, nil)
	if e != nil {
		return Finding{}, e
	}
	fs := pdbSnapshotFindings(NewScanContext(ctx, a.k, a.opts), s)
	if len(fs) == 0 {
		return Finding{ID: keyFor(ns, "PodDisruptionBudget/"+name+"/Healthy"), Namespace: ns, ResourceKind: "PodDisruptionBudget", ResourceName: name, ResourceUID: string(s.PDB.UID), Status: "Healthy", Severity: "info", Category: "policy", Summary: "Current PDB evidence shows no disruption blocker."}, nil
	}
	rank := func(severity string) int {
		switch severity {
		case "critical":
			return 4
		case "high":
			return 3
		case "medium":
			return 2
		default:
			return 1
		}
	}
	best := fs[0]
	for _, f := range fs[1:] {
		if rank(f.Severity) > rank(best.Severity) {
			best = f
		}
	}
	for _, f := range fs {
		if f.Status != best.Status {
			best.Evidence = append(best.Evidence, Evidence{Label: "Additional PDB finding " + f.Status, Value: f.Summary})
		}
	}
	return best, nil
}
