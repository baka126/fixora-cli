package analyzer

import (
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"k8s.io/apimachinery/pkg/api/resource"
)

func (a Analyzer) analyzePVCs(ctx *ScanContext) ([]Finding, error) {
	pvcs, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "pvc")
	if err != nil {
		return nil, err
	}
	events, _ := ctx.GetEvents()

	out := []Finding{}
	for _, pvc := range pvcs {
		namespace, name := objectNamespaceName(pvc)
		phase := strValue(nestedMap(pvc, "status")["phase"])
		spec := nestedMap(pvc, "spec")
		switch phase {
		case "Pending":
			event := latestObjectEvent(events, namespace, name)
			if event.Reason == "ProvisioningFailed" && strings.TrimSpace(event.Message) != "" {
				out = append(out, pvcFinding(pvc, "ProvisioningFailed", "high", "PVC provisioning failed.", event.Message))
			} else {
				out = append(out, pvcFinding(pvc, "Pending", "medium", "PVC is pending and has not bound to storage.", firstNonEmpty(event.Message, "phase=Pending")))
			}
		case "Lost":
			out = append(out, pvcFinding(pvc, "Lost", "high", "PVC is in Lost state.", "phase=Lost"))
		default:
			if request := pvcStorageRequest(spec); request != "" && storageRequestLessThanOneGi(request) {
				out = append(out, pvcFinding(pvc, "SmallStorageRequest", "low", "PVC requests less than 1Gi of storage.", "storage request="+request))
			}
			if strValue(spec["storageClassName"]) == "" && strValue(spec["volumeName"]) == "" {
				out = append(out, pvcFinding(pvc, "MissingStorageClass", "medium", "PVC has no StorageClass and is not bound to a specific volume.", "storageClassName and volumeName are empty"))
			}
		}
	}
	return out, nil
}

func pvcFinding(pvc map[string]any, status, severity, summary, evidence string) Finding {
	namespace, name := objectNamespaceName(pvc)
	return Finding{
		ID:           keyFor(namespace, "PersistentVolumeClaim/"+name+"/"+status),
		Namespace:    namespace,
		ResourceKind: "PersistentVolumeClaim",
		ResourceName: name,
		Status:       status,
		Severity:     severity,
		Category:     "storage",
		Summary:      summary,
		Evidence:     []Evidence{{Label: "PVC", Value: evidence}},
		GitOps:       gitOpsForObject(pvc),
		Recommendations: []Recommendation{{
			Title:         "Inspect PVC provisioning path",
			Description:   "Check StorageClass, provisioner events, access modes, zone constraints, quota, and volume binding mode before changing storage settings.",
			PatchType:     "pvc",
			SafeByDefault: false,
		}},
	}
}

func latestObjectEvent(events []kube.Event, namespace, name string) kube.Event {
	var latest kube.Event
	for _, event := range events {
		eventNS := firstNonEmpty(event.InvolvedObject.Namespace, event.Metadata.Namespace)
		if eventNS != namespace || event.InvolvedObject.Name != name {
			continue
		}
		if latest.LastTime == "" || event.LastTime >= latest.LastTime {
			latest = event
		}
	}
	return latest
}

func pvcStorageRequest(spec map[string]any) string {
	requests := nestedMap(nestedMap(spec, "resources"), "requests")
	return strValue(requests["storage"])
}

func storageRequestLessThanOneGi(value string) bool {
	quantity, err := resource.ParseQuantity(value)
	if err != nil {
		return false
	}
	return quantity.Cmp(resource.MustParse("1Gi")) < 0
}
