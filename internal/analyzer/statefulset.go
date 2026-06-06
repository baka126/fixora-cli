package analyzer

import (
	"fmt"
)

func (a Analyzer) analyzeStatefulSets(ctx *ScanContext) ([]Finding, error) {
	statefulsets, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "statefulsets")
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for _, sts := range statefulsets {
		namespace, name := objectNamespaceName(sts)
		spec := nestedMap(sts, "spec")
		status := nestedMap(sts, "status")

		specReplicas := 1
		if val, ok := spec["replicas"]; ok {
			specReplicas = intValue(val)
		}

		availableReplicas := intValue(status["availableReplicas"])

		if specReplicas != availableReplicas {
			summary := fmt.Sprintf("StatefulSet %s/%s has %d replicas in spec but %d are available", namespace, name, specReplicas, availableReplicas)

			out = append(out, Finding{
				ID:           keyFor(namespace, "StatefulSet/"+name+"/ReplicasMismatch"),
				Namespace:    namespace,
				ResourceKind: "StatefulSet",
				ResourceName: name,
				Status:       "ReplicasMismatch",
				Severity:     "high",
				Category:     "workload",
				Summary:      summary,
				Evidence: []Evidence{
					{Label: "Spec Replicas", Value: fmt.Sprint(specReplicas)},
					{Label: "Available Replicas", Value: fmt.Sprint(availableReplicas)},
				},
				GitOps: gitOpsForObject(sts),
				Recommendations: []Recommendation{{
					Title:         "Inspect statefulset pods",
					Description:   "Check the pod status and persistent volume claim bindings.",
					PatchType:     "statefulset",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}
