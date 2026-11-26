package analyzer

import (
	"fmt"
	"sort"
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

	// Read related inventories once. Unavailable reads are carried into evidence
	// rather than being reported as absent resources.
	scItems, scErr := ctx.GetResourceItems("", true, "storageclasses")
	scNames := pvcStorageClassNameSet(scItems, scErr)
	pvItems, pvErr := ctx.GetResourceItems("", true, "pv")

	// Build a map from PVC name (namespace/name) to every consuming pod/volume,
	// so VolumeAttachFailed can match mount failures to the correct claim.
	podItems, podErr := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "pods")
	podsByPVC := pvcConsumersFromItems(podItems)

	out := []Finding{}
	for _, pvc := range pvcs {
		findingStart := len(out)
		namespace, name := objectNamespaceName(pvc)
		phase := strValue(nestedMap(pvc, "status")["phase"])
		spec := nestedMap(pvc, "spec")
		className, hasClassField := spec["storageClassName"]

		switch phase {
		case "Pending":
			event := latestObjectEvent(events, namespace, name)
			if hasClassField && strValue(className) == "" && strValue(spec["volumeName"]) == "" {
				out = append(out, pvcFinding(pvc, "MissingStorageClass", "medium",
					"PVC explicitly opts out of StorageClasses; this claim cannot be assigned a class in place.",
					"storageClassName=\"\" is immutable; create a replacement claim with the intended StorageClass"))
			} else if event.Reason == "ProvisioningFailed" && strings.TrimSpace(event.Message) != "" {
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
		if len(out) > findingStart {
			evidence := pvcDiagnosticEvidence(pvc, scItems, scErr, pvItems, pvErr, podsByPVC, podErr)
			for i := findingStart; i < len(out); i++ {
				out[i].Evidence = append(out[i].Evidence, evidence...)
			}
		}
	}
	return out, nil
}

func pvcStorageClassNameSet(items []map[string]any, err error) map[string]bool {
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
	podUID     string
	volumeName string
	phase      string
	node       string
	ready      string
	owner      string
}

func pvcConsumersFromItems(pods []map[string]any) map[string][]pvcConsumer {
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
				meta := nestedMap(pod, "metadata")
				status := nestedMap(pod, "status")
				result[keyFor(podNS, claim)] = append(result[keyFor(podNS, claim)], pvcConsumer{
					podName:    podName,
					podUID:     strValue(meta["uid"]),
					volumeName: strValue(vol["name"]),
					phase:      strValue(status["phase"]),
					node:       strValue(nestedMap(pod, "spec")["nodeName"]),
					ready:      podReadyState(status),
					owner:      podOwnerDescription(meta),
				})
			}
		}
	}
	return result
}

func pvcDiagnosticEvidence(pvc map[string]any, scItems []map[string]any, scErr error, pvItems []map[string]any, pvErr error, podsByPVC map[string][]pvcConsumer, podErr error) []Evidence {
	meta := nestedMap(pvc, "metadata")
	spec := nestedMap(pvc, "spec")
	status := nestedMap(pvc, "status")
	phase := firstNonEmpty(strValue(status["phase"]), "unknown")
	out := []Evidence{{Label: "PVC identity", Value: fmt.Sprintf("uid=%s resourceVersion=%s phase=%s", strValue(meta["uid"]), strValue(meta["resourceVersion"]), phase)}}
	requested := pvcStorageRequest(spec)
	observed := strValue(nestedMap(status, "capacity")["storage"])
	out = append(out, Evidence{Label: "Requested and observed capacity", Value: fmt.Sprintf("requested=%s observed=%s", firstNonEmpty(requested, "unknown"), firstNonEmpty(observed, "unknown"))})
	access := stringValues(spec["accessModes"])
	volumeMode := firstNonEmpty(strValue(spec["volumeMode"]), "Filesystem (default)")
	out = append(out, Evidence{Label: "Access and volume mode", Value: fmt.Sprintf("accessModes=%s volumeMode=%s", firstNonEmpty(access, "unknown"), volumeMode)})

	className := strValue(spec["storageClassName"])
	if scErr != nil {
		out = append(out, Evidence{Label: "StorageClass inventory", Value: "unavailable; class absence is not established"})
	} else if className == "" {
		out = append(out, Evidence{Label: "StorageClass", Value: "unset"})
	} else if class, ok := findNamedItem(scItems, className); ok {
		cm := nestedMap(class, "metadata")
		expansion := "unknown"
		if value, ok := class["allowVolumeExpansion"].(bool); ok {
			expansion = fmt.Sprint(value)
		}
		out = append(out, Evidence{Label: "StorageClass", Value: fmt.Sprintf("%s uid=%s resourceVersion=%s provisioner=%s bindingMode=%s expansion=%s", className, strValue(cm["uid"]), strValue(cm["resourceVersion"]), firstNonEmpty(strValue(class["provisioner"]), "unknown"), firstNonEmpty(strValue(class["volumeBindingMode"]), "Immediate (default)"), expansion)})
	} else {
		out = append(out, Evidence{Label: "StorageClass", Value: className + " not found in readable inventory"})
	}

	pvName := strValue(spec["volumeName"])
	if pvErr != nil {
		out = append(out, Evidence{Label: "PersistentVolume inventory unavailable", Value: "PV absence or claim binding cannot be established"})
	} else if pvName == "" {
		out = append(out, Evidence{Label: "PersistentVolume", Value: "claim has no bound PV name"})
	} else if pv, ok := findNamedItem(pvItems, pvName); ok {
		pm := nestedMap(pv, "metadata")
		ps := nestedMap(pv, "spec")
		claim := nestedMap(ps, "claimRef")
		value := fmt.Sprintf("%s uid=%s resourceVersion=%s phase=%s storageClass=%s reclaimPolicy=%s claim=%s/%s uid=%s", pvName, strValue(pm["uid"]), strValue(pm["resourceVersion"]), firstNonEmpty(strValue(nestedMap(pv, "status")["phase"]), "unknown"), firstNonEmpty(strValue(ps["storageClassName"]), "none"), firstNonEmpty(strValue(ps["persistentVolumeReclaimPolicy"]), "unknown"), firstNonEmpty(strValue(claim["namespace"]), "?"), firstNonEmpty(strValue(claim["name"]), "?"), strValue(claim["uid"]))
		if topology := pvTopology(ps); topology != "" {
			value += " topology=" + topology
		}
		out = append(out, Evidence{Label: "PersistentVolume", Value: value})
	} else {
		out = append(out, Evidence{Label: "PersistentVolume", Value: pvName + " not found in readable inventory"})
	}

	if podErr != nil {
		out = append(out, Evidence{Label: "Consumer inventory unavailable", Value: "consumer Pod absence is not established"})
	} else {
		consumers := podsByPVC[keyFor(strValue(meta["namespace"]), strValue(meta["name"]))]
		if len(consumers) == 0 {
			out = append(out, Evidence{Label: "Consumers", Value: "none found in readable Pod inventory"})
		} else {
			values := make([]string, 0, len(consumers))
			for _, c := range consumers {
				values = append(values, fmt.Sprintf("%s uid=%s phase=%s ready=%s node=%s owner=%s", c.podName, firstNonEmpty(c.podUID, "unknown"), firstNonEmpty(c.phase, "unknown"), c.ready, firstNonEmpty(c.node, "unassigned"), firstNonEmpty(c.owner, "unknown")))
			}
			sort.Strings(values)
			out = append(out, Evidence{Label: "Consumers", Value: strings.Join(values, "; ")})
		}
	}
	return out
}

func findNamedItem(items []map[string]any, name string) (map[string]any, bool) {
	for _, item := range items {
		_, itemName := objectNamespaceName(item)
		if itemName == name {
			return item, true
		}
	}
	return nil, false
}

func stringValues(value any) string {
	items, _ := value.([]any)
	values := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			values = append(values, s)
		}
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

func podReadyState(status map[string]any) string {
	for _, raw := range nestedSlice(status, "conditions") {
		condition, ok := raw.(map[string]any)
		if ok && strValue(condition["type"]) == "Ready" {
			return strings.ToLower(strValue(condition["status"]))
		}
	}
	return "unknown"
}

func podOwnerDescription(meta map[string]any) string {
	owners := nestedSlice(meta, "ownerReferences")
	if len(owners) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(owners))
	for _, raw := range owners {
		owner, ok := raw.(map[string]any)
		if ok {
			parts = append(parts, strValue(owner["kind"])+"/"+strValue(owner["name"]))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func pvTopology(spec map[string]any) string {
	terms := nestedSlice(nestedMap(nestedMap(spec, "nodeAffinity"), "required"), "nodeSelectorTerms")
	values := []string{}
	for _, raw := range terms {
		term, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for _, exprRaw := range nestedSlice(term, "matchExpressions") {
			expr, ok := exprRaw.(map[string]any)
			if !ok {
				continue
			}
			key := strValue(expr["key"])
			if strings.Contains(key, "zone") || strings.Contains(key, "topology") {
				values = append(values, key+"="+stringValues(expr["values"]))
			}
		}
	}
	sort.Strings(values)
	return strings.Join(values, ",")
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
	metadata := nestedMap(pvc, "metadata")
	return Finding{
		ResourceUID:  strValue(metadata["uid"]),
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
