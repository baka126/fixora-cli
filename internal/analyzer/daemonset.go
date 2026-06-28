package analyzer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func (a Analyzer) analyzeDaemonSets(ctx *ScanContext) ([]Finding, error) {
	daemonsets, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "daemonsets")
	if err != nil {
		return nil, err
	}
	nodes, err := ctx.GetNodes()
	if err != nil {
		nodes = nil // degrade gracefully; taint/arch checks simply won't fire
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

		if nodes != nil {
			podSpec := nestedMap(nestedMap(nestedMap(ds, "spec"), "template"), "spec")
			tolerations := nestedSlice(podSpec, "tolerations")
			nodeSelector := nestedMap(podSpec, "nodeSelector")

			out = append(out, checkUnderScheduled(namespace, name, ds, desiredNumberScheduled, nodes, tolerations)...)
			out = append(out, checkFleetHeterogeneous(namespace, name, ds, nodes, nodeSelector)...)
		}
	}
	return out, nil
}

// taintTolerated reports whether any toleration in the list covers the given taint.
// ponytail: covers key/effect/Exists wildcard; omits value-operator nuance (Equal/NotExists edge cases).
func taintTolerated(tolerations []any, taintKey, taintEffect string) bool {
	for _, t := range tolerations {
		tol, ok := t.(map[string]any)
		if !ok {
			continue
		}
		key := strValue(tol["key"])
		effect := strValue(tol["effect"])
		operator := strValue(tol["operator"])

		// Empty key with Exists operator = wildcard toleration (tolerates everything).
		if key == "" && operator == "Exists" {
			return true
		}
		if key != taintKey {
			continue
		}
		// Effect empty in toleration = matches all effects.
		effectMatch := effect == "" || effect == taintEffect
		if !effectMatch {
			continue
		}
		// Exists operator matches any value for the key.
		if operator == "Exists" || operator == "" || operator == "Equal" {
			return true
		}
	}
	return false
}

// checkUnderScheduled emits DaemonSetUnderScheduled when untolerated NoSchedule/NoExecute
// taints on schedulable nodes explain a gap between desired and schedulable count.
func checkUnderScheduled(namespace, name string, ds map[string]any, desired int, nodes []kube.Node, tolerations []any) []Finding {
	schedulable := 0
	var excludingTaintKeys []string
	seen := map[string]bool{}
	for _, n := range nodes {
		if n.Metadata.Labels["node.kubernetes.io/unschedulable"] == "true" {
			continue
		}
		schedulable++
		for _, taint := range n.Spec.Taints {
			effect := strValue(taint["effect"])
			if effect != "NoSchedule" && effect != "NoExecute" {
				continue
			}
			key := strValue(taint["key"])
			if taintTolerated(tolerations, key, effect) {
				continue
			}
			if !seen[key] {
				seen[key] = true
				excludingTaintKeys = append(excludingTaintKeys, key)
			}
		}
	}
	if desired >= schedulable || len(excludingTaintKeys) == 0 {
		return nil
	}
	sort.Strings(excludingTaintKeys)
	summary := fmt.Sprintf("DaemonSet %s/%s scheduled on %d of %d schedulable nodes; untolerated taint(s): %s",
		namespace, name, desired, schedulable, strings.Join(excludingTaintKeys, ", "))
	return []Finding{{
		ID:           keyFor(namespace, "DaemonSet/"+name+"/DaemonSetUnderScheduled"),
		Namespace:    namespace,
		ResourceKind: "DaemonSet",
		ResourceName: name,
		Status:       "DaemonSetUnderScheduled",
		Severity:     "medium",
		Category:     "scheduling",
		Summary:      summary,
		Evidence: []Evidence{
			{Label: "Desired scheduled", Value: fmt.Sprint(desired)},
			{Label: "Schedulable nodes", Value: fmt.Sprint(schedulable)},
			{Label: "Excluding taint keys", Value: strings.Join(excludingTaintKeys, ", ")},
		},
		GitOps: gitOpsForObject(ds),
		Recommendations: []Recommendation{{
			Title:         "Add tolerations or review taint policy",
			Description:   "Add tolerations for the listed taint keys in the DaemonSet pod template, or verify the taint policy is intentional.",
			PatchType:     "daemonset",
			SafeByDefault: false,
		}},
	}}
}

// checkFleetHeterogeneous emits DaemonSetFleetHeterogeneous when nodes span more than one
// kubernetes.io/arch or kubernetes.io/os value and the DS pod template does not pin arch/os.
func checkFleetHeterogeneous(namespace, name string, ds map[string]any, nodes []kube.Node, nodeSelector map[string]any) []Finding {
	archs := map[string]bool{}
	oses := map[string]bool{}
	for _, n := range nodes {
		if a := n.Metadata.Labels["kubernetes.io/arch"]; a != "" {
			archs[a] = true
		}
		if o := n.Metadata.Labels["kubernetes.io/os"]; o != "" {
			oses[o] = true
		}
	}
	_, archPinned := nodeSelector["kubernetes.io/arch"]
	_, osPinned := nodeSelector["kubernetes.io/os"]

	heterogArch := len(archs) > 1 && !archPinned
	heterogOS := len(oses) > 1 && !osPinned
	if !heterogArch && !heterogOS {
		return nil
	}

	var vals []string
	if heterogArch {
		sorted := sortedKeys(archs)
		vals = append(vals, "arch: "+strings.Join(sorted, "/"))
	}
	if heterogOS {
		sorted := sortedKeys(oses)
		vals = append(vals, "os: "+strings.Join(sorted, "/"))
	}
	valStr := strings.Join(vals, "; ")
	summary := fmt.Sprintf("DaemonSet %s/%s runs on a heterogeneous fleet (%s) without a nodeSelector pin", namespace, name, valStr)
	return []Finding{{
		ID:           keyFor(namespace, "DaemonSet/"+name+"/DaemonSetFleetHeterogeneous"),
		Namespace:    namespace,
		ResourceKind: "DaemonSet",
		ResourceName: name,
		Status:       "DaemonSetFleetHeterogeneous",
		Severity:     "low",
		Category:     "scheduling",
		Summary:      summary,
		Evidence: []Evidence{
			{Label: "Fleet variation", Value: valStr},
		},
		GitOps: gitOpsForObject(ds),
		Recommendations: []Recommendation{{
			Title:         "Pin nodeSelector or build multi-arch image",
			Description:   "Add kubernetes.io/arch (and/or kubernetes.io/os) to the DaemonSet nodeSelector, or ensure the container image supports all architectures in the fleet.",
			PatchType:     "daemonset",
			SafeByDefault: false,
		}},
	}}
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
