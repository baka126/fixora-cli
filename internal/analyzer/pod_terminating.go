package analyzer

import (
	"strconv"
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

// analyzePodsTerminating flags pods stuck in Terminating past their grace
// period and attributes the blocking cause. Review-only.
func (a Analyzer) analyzePodsTerminating(ctx *ScanContext) ([]Finding, error) {
	pods, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "pods")
	if err != nil {
		return nil, err
	}
	now := time.Now()

	var (
		findings  []Finding
		events    []kube.Event
		nodeReady map[string]bool
		loaded    bool
	)
	for _, pod := range pods {
		meta := nestedMap(pod, "metadata")
		age, ok := deletionAge(strValue(meta["deletionTimestamp"]), now)
		if !ok {
			continue
		}
		grace := intValue(meta["deletionGracePeriodSeconds"])
		if !isStuckTerminating(age, grace) {
			continue
		}
		if !loaded {
			events, _ = ctx.GetEvents()
			nodeReady = readyNodes(ctx)
			loaded = true
		}
		causes, finalizerBlocked := terminatingCauses(pod, events, nodeReady)
		severity := "medium"
		if finalizerBlocked {
			severity = "high"
		}
		findings = append(findings, podTerminatingFinding(pod, severity, age, grace, causes))
	}
	return findings, nil
}

// readyNodes maps node name to Ready-condition-is-True, read untyped so no
// kube type change is required. Lookup failures degrade to an empty map.
func readyNodes(ctx *ScanContext) map[string]bool {
	out := map[string]bool{}
	nodes, err := ctx.GetResourceItems("", true, "nodes")
	if err != nil {
		return out
	}
	for _, n := range nodes {
		_, name := objectNamespaceName(n)
		if name == "" {
			continue
		}
		ready, hasReady := false, false
		for _, c := range nestedSlice(nestedMap(n, "status"), "conditions") {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if strValue(cm["type"]) == "Ready" {
				ready = strValue(cm["status"]) == "True"
				hasReady = true
			}
		}
		// Only record nodes with an explicit Ready condition. A node lacking one
		// is "unknown", not "unreachable" — leave it absent so terminatingCauses
		// does not attribute a stuck pod to it (known && !ready stays false).
		if hasReady {
			out[name] = ready
		}
	}
	return out
}

func podTerminatingFinding(pod map[string]any, severity string, age time.Duration, grace int, causes []string) Finding {
	namespace, name := objectNamespaceName(pod)
	meta := nestedMap(pod, "metadata")
	evidence := []Evidence{
		{Label: "Deletion requested", Value: strValue(meta["deletionTimestamp"])},
		{Label: "Terminating for", Value: age.Round(time.Second).String()},
		{Label: "Grace period", Value: strconv.Itoa(gracePeriodOrDefault(grace)) + "s"},
	}
	if len(causes) == 0 {
		evidence = append(evidence, Evidence{Label: "Likely cause", Value: "no finalizer, preStop, failed-detach event, or node problem detected; kubelet may not have confirmed deletion"})
	}
	for _, c := range causes {
		evidence = append(evidence, Evidence{Label: "Likely cause", Value: c})
	}
	return Finding{
		ID:           keyFor(namespace, "Pod/"+name+"/PodStuckTerminating"),
		Namespace:    namespace,
		ResourceKind: "Pod",
		ResourceName: name,
		PodName:      name,
		Status:       "PodStuckTerminating",
		Severity:     severity,
		Category:     "lifecycle",
		Summary:      "Pod has been Terminating longer than its grace period and has not been removed.",
		GitOps:       gitOpsForObject(pod),
		Evidence:     evidence,
		Recommendations: []Recommendation{{
			Title:         "Investigate the termination blocker",
			Description:   "Identify what is holding the pod: pending finalizers, a slow or hanging preStop hook, a failing volume detach/unmount, or an unreachable node. Resolve the underlying controller, finalizer, or node issue. Force deletion (kubectl delete --grace-period=0 --force) or removing finalizers is a destructive last resort that can orphan resources or corrupt data — do it only after confirming the workload is safe to drop.",
			PatchType:     "lifecycle",
			SafeByDefault: false,
		}},
	}
}
