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

	// Build a set of known StorageClass names (fetched once, tolerate errors).
	scNames := pvcStorageClassNames(ctx)

	// Build a map from PVC name (namespace/name) to every consuming pod/volume,
	// so VolumeAttachFailed can match mount failures to the correct claim.
	podsByPVC := pvcConsumingPods(ctx, a.opts.Namespace, a.opts.AllNS)

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
			} else if waitEvent, ok := objectEventWithReason(events, namespace, name, "WaitForFirstConsumer"); ok {
				// WaitForFirstConsumer is a normal Pending sub-state; report it
				// at low severity instead of the generic medium Pending.
				out = append(out, pvcFinding(pvc, "VolumeAwaitingConsumer", "low",
					"PVC is pending because its StorageClass uses WaitForFirstConsumer binding mode.",
					firstNonEmpty(waitEvent.Message, "WaitForFirstConsumer binding mode")))
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

		// StorageClassNotFound: if a StorageClass name is set, verify it exists.
		if sc := strValue(spec["storageClassName"]); sc != "" && scNames != nil && !scNames[sc] {
			out = append(out, pvcFinding(pvc, "StorageClassNotFound", "high",
				"PVC references a StorageClass that does not exist.",
				"storageClassName="+sc))
		}

		// VolumeResizePending: check status.conditions for resize-in-progress.
		if resizeCond := pvcResizeCondition(pvc); resizeCond != "" {
			out = append(out, pvcFinding(pvc, "VolumeResizePending", "medium",
				"PVC volume resize is in progress and waiting for completion.",
				"condition="+resizeCond))
		}

		// VolumeAttachFailed: scan events on pods that mount this PVC.
		if podName, reason, msg, ok := pvcAttachFailedEvent(events, namespace, name, podsByPVC); ok {
			out = append(out, pvcFinding(pvc, "VolumeAttachFailed", "high",
				"Volume attach or mount failed for a pod consuming this PVC.",
				"pod="+podName+" reason="+reason+" message="+msg))
		}
	}
	return out, nil
}

// pvcStorageClassNames returns a set of known StorageClass names.
// Returns nil if the list cannot be fetched (caller treats nil as
// "data unavailable -- skip the check"). An empty list is a valid empty set.
func pvcStorageClassNames(ctx *ScanContext) map[string]bool {
	// StorageClass is cluster-scoped — fetch cluster-wide regardless of the
	// scan namespace (matches node.go / storage.go).
	items, err := ctx.GetResourceItems("", true, "storageclasses")
	if err != nil {
		return nil
	}
	names := make(map[string]bool, len(items))
	for _, sc := range items {
		_, n := objectNamespaceName(sc)
		if n != "" {
			names[n] = true
		}
	}
	return names
}

type pvcConsumer struct {
	podName    string
	volumeName string
}

// pvcConsumingPods returns every pod/volume pair that mounts each PVC.
// Tolerates missing pod items.
func pvcConsumingPods(ctx *ScanContext, namespace string, allNS bool) map[string][]pvcConsumer {
	pods, err := ctx.GetResourceItems(namespace, allNS, "pods")
	if err != nil {
		return nil
	}
	result := make(map[string][]pvcConsumer)
	for _, pod := range pods {
		podNS, podName := objectNamespaceName(pod)
		spec := nestedMap(pod, "spec")
		for _, v := range nestedSlice(spec, "volumes") {
			vol, ok := v.(map[string]any)
			if !ok {
				continue
			}
			pvcRef, ok := vol["persistentVolumeClaim"].(map[string]any)
			if !ok {
				continue
			}
			claim := strValue(pvcRef["claimName"])
			if claim != "" {
				result[keyFor(podNS, claim)] = append(result[keyFor(podNS, claim)], pvcConsumer{
					podName:    podName,
					volumeName: strValue(vol["name"]),
				})
			}
		}
	}
	return result
}

// pvcResizeCondition returns the first active resize condition type on the PVC,
// or "" if none found. Checks FileSystemResizePending and Resizing.
func pvcResizeCondition(pvc map[string]any) string {
	status := nestedMap(pvc, "status")
	conditions, _ := status["conditions"].([]any)
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		t := strValue(cond["type"])
		s := strValue(cond["status"])
		if s == "True" && (t == "FileSystemResizePending" || t == "Resizing") {
			return t
		}
	}
	return ""
}

// pvcAttachFailedEvent scans events for FailedAttachVolume or FailedMount
// reasons on pods that consume the given PVC. Returns the pod name, reason,
// message, and true when found.
func pvcAttachFailedEvent(events []kube.Event, namespace, pvcName string, podsByPVC map[string][]pvcConsumer) (podName, reason, msg string, found bool) {
	if podsByPVC == nil {
		return "", "", "", false
	}
	consumers := podsByPVC[keyFor(namespace, pvcName)]
	if len(consumers) == 0 {
		return "", "", "", false
	}
	consumerByPod := make(map[string][]string, len(consumers))
	for _, consumer := range consumers {
		if consumer.podName == "" || consumer.volumeName == "" {
			continue
		}
		consumerByPod[consumer.podName] = append(consumerByPod[consumer.podName], consumer.volumeName)
	}
	for _, ev := range events {
		evNS := firstNonEmpty(ev.InvolvedObject.Namespace, ev.Metadata.Namespace)
		volumeNames := consumerByPod[ev.InvolvedObject.Name]
		if evNS != namespace || len(volumeNames) == 0 {
			continue
		}
		if (ev.Reason == "FailedAttachVolume" || ev.Reason == "FailedMount") && eventReferencesAnyVolume(ev.Message, volumeNames) {
			return ev.InvolvedObject.Name, ev.Reason, ev.Message, true
		}
	}
	return "", "", "", false
}

func eventReferencesAnyVolume(message string, volumeNames []string) bool {
	for _, volumeName := range volumeNames {
		if volumeName != "" && strings.Contains(message, volumeName) {
			return true
		}
	}
	return false
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

func objectEventWithReason(events []kube.Event, namespace, name, reason string) (kube.Event, bool) {
	var latest kube.Event
	found := false
	for _, event := range events {
		eventNS := firstNonEmpty(event.InvolvedObject.Namespace, event.Metadata.Namespace)
		if eventNS != namespace || event.InvolvedObject.Name != name || event.Reason != reason {
			continue
		}
		if !found || event.LastTime >= latest.LastTime {
			latest = event
			found = true
		}
	}
	return latest, found
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
