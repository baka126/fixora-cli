package analyzer

import (
	"fmt"
)

func (a Analyzer) analyzeJobs(ctx *ScanContext) ([]Finding, error) {
	jobs, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "jobs")
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for _, job := range jobs {
		namespace, name := objectNamespaceName(job)
		spec := nestedMap(job, "spec")
		status := nestedMap(job, "status")

		if val, ok := spec["suspend"]; ok && boolValue(val) {
			out = append(out, Finding{
				ID:           keyFor(namespace, "Job/"+name+"/Suspended"),
				Namespace:    namespace,
				ResourceKind: "Job",
				ResourceName: name,
				Status:       "Suspended",
				Severity:     "low",
				Category:     "workload",
				Summary:      fmt.Sprintf("Job %s is suspended", name),
				Evidence: []Evidence{
					{Label: "Suspend", Value: "true"},
				},
				GitOps: gitOpsForObject(job),
				Recommendations: []Recommendation{{
					Title:         "Review job suspension",
					Description:   "If this is unintentional, set spec.suspend to false.",
					PatchType:     "job",
					SafeByDefault: false,
				}},
			})
		}

		failed := intValue(status["failed"])
		if failed > 0 {
			out = append(out, Finding{
				ID:           keyFor(namespace, "Job/"+name+"/Failed"),
				Namespace:    namespace,
				ResourceKind: "Job",
				ResourceName: name,
				Status:       "Failed",
				Severity:     "high",
				Category:     "workload",
				Summary:      fmt.Sprintf("Job %s has failed", name),
				Evidence: []Evidence{
					{Label: "Failed Pods", Value: fmt.Sprint(failed)},
				},
				GitOps: gitOpsForObject(job),
				Recommendations: []Recommendation{{
					Title:         "Inspect job logs",
					Description:   "Check the logs of the failed pods for errors.",
					PatchType:     "job",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}
