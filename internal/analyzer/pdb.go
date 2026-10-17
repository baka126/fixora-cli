package analyzer

import (
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/pdbfix"
)

func (a Analyzer) analyzePDBs(ctx *ScanContext) ([]Finding, error) {
	pdbs, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "pdb")
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, p := range pdbs {
		ns, name := objectNamespaceName(p)
		s, err := pdbfix.Collect(ctx, pdbEvidenceReader{ctx.Reader}, ns, name, nil)
		if err != nil {
			out = append(out, Finding{ID: keyFor(ns, "PodDisruptionBudget/"+name+"/PDBEvidenceUnavailable"), Namespace: ns, ResourceKind: "PodDisruptionBudget", ResourceName: name, Status: "PDBEvidenceUnavailable", Severity: "medium", Category: "policy", Summary: "The PDB could not be read for diagnosis.", Evidence: []Evidence{{Label: "PDB read", Value: err.Error()}}})
			continue
		}
		for _, d := range pdbfix.Diagnose(s) {
			f := Finding{ID: keyFor(ns, "PodDisruptionBudget/"+name+"/"+d.Code), Namespace: ns, ResourceKind: "PodDisruptionBudget", ResourceName: name, ResourceUID: string(s.PDB.UID), Status: d.Code, Severity: d.Severity, Category: "policy", Summary: d.Cause, GitOps: gitOpsForObject(p), Evidence: []Evidence{{Label: "PDB classification", Value: d.Code}}, Recommendations: []Recommendation{{Title: "Review disruption protection", Description: d.Next, PatchType: "pdb", SafeByDefault: false}}}
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
	}
	return out, nil
}
