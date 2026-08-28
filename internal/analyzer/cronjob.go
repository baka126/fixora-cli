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
		status := nestedMap(cronjob, "status")

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

		policy := strValue(spec["concurrencyPolicy"])
		if policy == "" {
			policy = "Allow"
		}
		active := nestedSlice(status, "active")
		if policy == "Allow" && len(active) > 1 {
			out = append(out, Finding{
				ID:           keyFor(namespace, "CronJob/"+name+"/CronJobOverlap"),
				Namespace:    namespace,
				ResourceKind: "CronJob",
				ResourceName: name,
				Status:       "CronJobOverlap",
				Severity:     "medium",
				Category:     "workload",
				Summary:      fmt.Sprintf("CronJob %s has %d concurrent active runs", name, len(active)),
				Evidence: []Evidence{
					{Label: "Active Runs", Value: fmt.Sprint(len(active))},
					{Label: "ConcurrencyPolicy", Value: policy},
				},
				GitOps: gitOpsForObject(cronjob),
				Recommendations: []Recommendation{{
					Title:         "Set concurrencyPolicy to Forbid or Replace",
					Description:   "Multiple concurrent runs may cause resource contention or duplicate work. Consider setting spec.concurrencyPolicy to Forbid or Replace.",
					PatchType:     "cronjob",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}
