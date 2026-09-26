package analyzer

import (
	"fmt"
)

func (a Analyzer) analyzeDeployments(ctx *ScanContext) ([]Finding, error) {
	deployments, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "deployments")
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for _, deployment := range deployments {
		namespace, name := objectNamespaceName(deployment)
		spec := nestedMap(deployment, "spec")
		status := nestedMap(deployment, "status")

		specReplicas := 1
		if val, ok := spec["replicas"]; ok {
			specReplicas = intValue(val)
		}

		readyReplicas := intValue(status["readyReplicas"])
		replicas := intValue(status["replicas"])

		if generation := intValue(nestedMap(deployment, "metadata")["generation"]); generation > 0 && intValue(status["observedGeneration"]) < generation {
			continue
		}
		if deploymentConditionTrue(status, "Progressing") && deploymentConditionTrue(status, "Available") {
			continue
		}
		if specReplicas != readyReplicas {
			var summary string
			if replicas > specReplicas {
				summary = fmt.Sprintf("Deployment %s/%s has %d replicas in spec but %d replicas in status because status field is not updated yet after scaling and %d replicas are available with status running", namespace, name, specReplicas, replicas, readyReplicas)
			} else {
				summary = fmt.Sprintf("Deployment %s/%s has %d replicas but %d are available with status running", namespace, name, specReplicas, readyReplicas)
			}

			out = append(out, Finding{
				ID:           keyFor(namespace, "Deployment/"+name+"/ReplicasMismatch"),
				Namespace:    namespace,
				ResourceKind: "Deployment",
				ResourceName: name,
				Status:       "ReplicasMismatch",
				Severity:     "high",
				Category:     "workload",
				Summary:      summary,
				Evidence: []Evidence{
					{Label: "Spec Replicas", Value: fmt.Sprint(specReplicas)},
					{Label: "Status Replicas", Value: fmt.Sprint(replicas)},
					{Label: "Ready Replicas", Value: fmt.Sprint(readyReplicas)},
				},
				GitOps: gitOpsForObject(deployment),
				Recommendations: []Recommendation{{
					Title:         "Inspect deployment pods",
					Description:   "Check the pod status for CrashLoopBackOff or ImagePullBackOff, and verify node capacity.",
					PatchType:     "deployment",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}

func deploymentConditionTrue(status map[string]any, conditionType string) bool {
	for _, raw := range nestedSlice(status, "conditions") {
		condition, ok := raw.(map[string]any)
		if ok && strValue(condition["type"]) == conditionType && strValue(condition["status"]) == "True" {
			return true
		}
	}
	return false
}
