package analyzer

import (
	"fmt"
)

func (a Analyzer) analyzeDaemonSets(ctx *ScanContext) ([]Finding, error) {
	daemonsets, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "daemonsets")
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for _, ds := range daemonsets {
		namespace, name := objectNamespaceName(ds)
		status := nestedMap(ds, "status")

		numberReady := intValue(status["numberReady"])
		desiredNumberScheduled := intValue(status["desiredNumberScheduled"])

		if numberReady < desiredNumberScheduled {
			summary := fmt.Sprintf("DaemonSet %s/%s has %d ready replicas out of %d scheduled", namespace, name, numberReady, desiredNumberScheduled)

			out = append(out, Finding{
				ID:           keyFor(namespace, "DaemonSet/"+name+"/NotReady"),
				Namespace:    namespace,
				ResourceKind: "DaemonSet",
				ResourceName: name,
				Status:       "NotReady",
				Severity:     "high",
				Category:     "workload",
				Summary:      summary,
				Evidence: []Evidence{
					{Label: "Ready", Value: fmt.Sprint(numberReady)},
					{Label: "Desired", Value: fmt.Sprint(desiredNumberScheduled)},
				},
				GitOps: gitOpsForObject(ds),
				Recommendations: []Recommendation{{
					Title:         "Inspect daemonset pods",
					Description:   "Check the pod status for node affinity issues, taints, or CrashLoopBackOff.",
					PatchType:     "daemonset",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}
