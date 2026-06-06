package analyzer

import (
	"fmt"
)

func (a Analyzer) analyzeCronJobs(ctx *ScanContext) ([]Finding, error) {
	cronjobs, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "cronjobs")
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for _, cronjob := range cronjobs {
		namespace, name := objectNamespaceName(cronjob)
		spec := nestedMap(cronjob, "spec")
		
		if val, ok := spec["suspend"]; ok && boolValue(val) {
			out = append(out, Finding{
				ID:           keyFor(namespace, "CronJob/"+name+"/Suspended"),
				Namespace:    namespace,
				ResourceKind: "CronJob",
				ResourceName: name,
				Status:       "Suspended",
				Severity:     "medium",
				Category:     "workload",
				Summary:      fmt.Sprintf("CronJob %s is suspended", name),
				Evidence: []Evidence{
					{Label: "Suspend", Value: "true"},
				},
				GitOps: gitOpsForObject(cronjob),
				Recommendations: []Recommendation{{
					Title:         "Review cronjob suspension",
					Description:   "If this is unintentional, set spec.suspend to false.",
					PatchType:     "cronjob",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}
