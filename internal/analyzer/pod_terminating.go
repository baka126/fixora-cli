package analyzer

import (
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

const terminatingGraceBuffer = 30 * time.Second

// deletionAge returns how long ago the pod's deletion was requested. ok is
// false when the timestamp is empty or not RFC3339.
func deletionAge(deletionTS string, now time.Time) (time.Duration, bool) {
	if strings.TrimSpace(deletionTS) == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, deletionTS)
	if err != nil {
		return 0, false
	}
	return now.Sub(t), true
}

// gracePeriodOrDefault returns the effective grace period, defaulting to 30s.
func gracePeriodOrDefault(gracePeriodSeconds int) int {
	if gracePeriodSeconds <= 0 {
		return 30
	}
	return gracePeriodSeconds
}

// isStuckTerminating reports whether a terminating pod has outlived its grace
// period plus a buffer (so a pod mid-graceful-shutdown is not flagged).
func isStuckTerminating(age time.Duration, gracePeriodSeconds int) bool {
	limit := time.Duration(gracePeriodOrDefault(gracePeriodSeconds))*time.Second + terminatingGraceBuffer
	return age > limit
}

// terminatingCauses attributes a stuck-Terminating pod to any detectable
// blockers. finalizerBlocked is true when finalizers are present (the pod will
// not self-resolve).
func terminatingCauses(pod map[string]any, events []kube.Event, nodeReady map[string]bool) (causes []string, finalizerBlocked bool) {
	meta := nestedMap(pod, "metadata")
	spec := nestedMap(pod, "spec")

	// finalizers
	var finalizers []string
	for _, f := range nestedSlice(meta, "finalizers") {
		if s, ok := f.(string); ok && s != "" {
			finalizers = append(finalizers, s)
		}
	}
	if len(finalizers) > 0 {
		causes = append(causes, "blocked by finalizers: "+strings.Join(finalizers, ", "))
		finalizerBlocked = true
	}

	// preStop hook on any container
	for _, c := range nestedSlice(spec, "containers") {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if len(nestedMap(nestedMap(cm, "lifecycle"), "preStop")) > 0 {
			name := strValue(cm["name"])
			causes = append(causes, "container "+name+" has a preStop hook that may be delaying termination")
			break
		}
	}

	// volume detach/unmount failing (event-based)
	namespace, name := objectNamespaceName(pod)
	for _, e := range events {
		eventNS := firstNonEmpty(e.InvolvedObject.Namespace, e.Metadata.Namespace)
		if eventNS != namespace || e.InvolvedObject.Name != name {
			continue
		}
		if strings.Contains(e.Reason, "FailedDetach") || strings.Contains(e.Reason, "FailedUnMount") || strings.Contains(e.Reason, "FailedUnmount") {
			causes = append(causes, "volume detach/unmount failing: "+strings.TrimSpace(e.Reason+" "+e.Message))
			break
		}
	}

	// node unreachable
	if nodeName := strValue(spec["nodeName"]); nodeName != "" {
		if ready, known := nodeReady[nodeName]; known && !ready {
			causes = append(causes, "node "+nodeName+" is NotReady/unreachable")
		}
	}

	return causes, finalizerBlocked
}
