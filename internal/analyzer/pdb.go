package analyzer

import (
	"fmt"
)

func (a Analyzer) analyzePDBs(ctx *ScanContext) ([]Finding, error) {
	pdbs, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "pdb")
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for _, pdb := range pdbs {
		namespace, name := objectNamespaceName(pdb)
		status := nestedMap(pdb, "status")
		current, desired := intValue(status["currentHealthy"]), intValue(status["desiredHealthy"])
		disruptions := intValue(status["disruptionsAllowed"])
		expected := intValue(status["expectedPods"])
		if expected == 0 || current < desired || disruptions == 0 && desired > 0 {
			severity := "medium"
			if current < desired {
				severity = "high"
			}
			out = append(out, Finding{
				ID:           keyFor(namespace, "PodDisruptionBudget/"+name+"/Unavailable"),
				Namespace:    namespace,
				ResourceKind: "PodDisruptionBudget",
				ResourceName: name,
				Status:       "DisruptionsBlocked",
				Severity:     severity,
				Category:     "policy",
				Summary:      "PodDisruptionBudget may block voluntary disruption or does not match healthy pods.",
				Evidence: []Evidence{
					{Label: "currentHealthy", Value: fmt.Sprint(current)},
					{Label: "desiredHealthy", Value: fmt.Sprint(desired)},
					{Label: "disruptionsAllowed", Value: fmt.Sprint(disruptions)},
					{Label: "expectedPods", Value: fmt.Sprint(expected)},
				},
				GitOps: gitOpsForObject(pdb),
				Recommendations: []Recommendation{{
					Title:         "Check PDB selector and workload health",
					Description:   "Confirm the PDB selector matches the intended pods, then fix unavailable replicas before relaxing disruption policy.",
					PatchType:     "pdb",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}
