package analyzer

import (
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func (a Analyzer) analyzeHPATargets(ctx *ScanContext) ([]Finding, error) {
	hpas, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "hpa")
	if err != nil {
		return nil, err
	}
	out := []Finding{}
	for _, hpa := range hpas {
		namespace, name := objectNamespaceName(hpa)
		spec := nestedMap(hpa, "spec")
		target := nestedMap(spec, "scaleTargetRef")
		targetKind, targetName := strValue(target["kind"]), strValue(target["name"])
		if targetKind == "" || targetName == "" {
			out = append(out, Finding{
				ID:           keyFor(namespace, "HPA/"+name+"/MissingTargetRef"),
				Namespace:    namespace,
				ResourceKind: "HorizontalPodAutoscaler",
				ResourceName: name,
				Status:       "MissingScaleTargetRef",
				Severity:     "high",
				Category:     "autoscaling",
				Summary:      "HPA does not have a complete scaleTargetRef.",
				Evidence:     []Evidence{{Label: "scaleTargetRef", Value: compactMap(target)}},
				GitOps:       gitOpsForObject(hpa),
				Recommendations: []Recommendation{{
					Title:         "Set a valid scale target",
					Description:   "Point the HPA at an existing scalable workload such as Deployment, StatefulSet, or ReplicaSet.",
					PatchType:     "hpa",
					SafeByDefault: false,
				}},
			})
			continue
		}
		targetResource := strings.ToLower(targetKind) + "/" + targetName
		targetObj, targetErr := ctx.GetResource(namespace, targetResource)
		if targetErr != nil {
			missing := hpaTargetNotFound(targetErr, targetKind, targetName)
			status, severity, summary := "ScaleTargetUnreadable", "medium", "HPA scale target could not be verified because its read failed."
			title, description := "Resolve scale target read error", "Check RBAC and API availability before concluding the HPA target is missing."
			if missing {
				status, severity, summary = "MissingScaleTarget", "high", "HPA references a scale target that does not exist."
				title, description = "Fix or restore the autoscale target", "Confirm the target kind, name, namespace, and API availability before the HPA is allowed to drive scaling decisions."
			}
			out = append(out, Finding{
				ID:           keyFor(namespace, "HPA/"+name+"/"+status+"/"+targetKind+"/"+targetName),
				Namespace:    namespace,
				ResourceKind: "HorizontalPodAutoscaler",
				ResourceName: name,
				Status:       status,
				Severity:     severity,
				Category:     "autoscaling",
				Summary:      summary,
				Evidence: []Evidence{
					{Label: "Target", Value: targetKind + "/" + targetName},
					{Label: "Error", Value: targetErr.Error()},
				},
				GitOps: gitOpsForObject(hpa),
				Recommendations: []Recommendation{{
					Title:         title,
					Description:   description,
					PatchType:     "hpa",
					SafeByDefault: false,
				}},
			})
			continue
		}
		for _, metric := range hpaResourceMetricNames(spec) {
			missing := containersMissingResourceRequest(targetObj, metric)
			if len(missing) == 0 {
				continue
			}
			out = append(out, Finding{
				ID:           keyFor(namespace, "HPA/"+name+"/MissingRequests/"+metric),
				Namespace:    namespace,
				ResourceKind: "HorizontalPodAutoscaler",
				ResourceName: name,
				Status:       "MissingResourceRequests",
				Severity:     "medium",
				Category:     "autoscaling",
				Summary:      "HPA uses a resource metric but target containers are missing matching resource requests.",
				Evidence: []Evidence{
					{Label: "Metric", Value: metric},
					{Label: "Containers", Value: strings.Join(missing, ", ")},
				},
				GitOps: gitOpsForObject(hpa),
				Recommendations: []Recommendation{{
					Title:         "Add resource requests before relying on HPA",
					Description:   "Set realistic container requests in the workload source so utilization-based scaling has stable inputs.",
					PatchType:     "resources",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}

func hpaTargetNotFound(err error, kind, name string) bool {
	if apierrors.IsNotFound(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "(notfound)") &&
		strings.Contains(message, strings.ToLower(kind)) &&
		strings.Contains(message, `"`+strings.ToLower(name)+`" not found`)
}
