package analyzer

import (
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"strings"
)

// Explain the probe and observed response without claiming an unproven
// application defect. Pod events cannot identify one of several failing
// containers, so ambiguous cases retain the general diagnosis.
func probeFailureDiagnosis(pod kube.Pod, status string, events []kube.Event) string {
	probeKey, prefix := "readinessProbe", "Readiness probe failed:"
	if status == "CrashLoopBackOff" {
		probeKey, prefix = "livenessProbe", "Liveness probe failed:"
	} else if status != "ProbeFailure" {
		return ""
	}
	candidates := []kube.Container{}
	for _, c := range pod.Spec.Containers {
		if (probeKey == "readinessProbe" && len(c.ReadinessProbe) > 0) || (probeKey == "livenessProbe" && len(c.LivenessProbe) > 0) {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) != 1 {
		return ""
	}
	c := candidates[0]
	unready := false
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == c.Name && !cs.Ready {
			unready = true
		}
	}
	if !unready {
		return ""
	}
	var observed, latest string
	killed := false
	for _, e := range events {
		if strings.Contains(e.Message, "Container "+c.Name+" failed liveness probe") {
			killed = true
		}
		if strings.HasPrefix(e.Message, prefix) && (observed == "" || e.LastTime == "" || e.LastTime >= latest) {
			observed = e.Message
			latest = e.LastTime
		}
	}
	if observed == "" || (probeKey == "livenessProbe" && !killed) {
		return ""
	}
	probe := c.ReadinessProbe
	if probeKey == "livenessProbe" {
		probe = c.LivenessProbe
	}
	handler, ok := probe["httpGet"].(map[string]any)
	if !ok || handler["port"] == nil {
		return ""
	}
	path, _ := handler["path"].(string)
	if path == "" {
		path = "/"
	}
	effect := "This keeps the container unready."
	if probeKey == "livenessProbe" {
		effect = "Kubernetes reports restarting this container after liveness failures."
	}
	return fmt.Sprintf("Container %s's %s targets HTTP %s on port %v. Kubernetes reports: %s. %s", c.Name, probeKey, path, handler["port"], observed, effect)
}
