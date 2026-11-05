// Package infer fills fix.Plan patch placeholders from evidence already
// gathered on an analyzer.Finding, plus targeted cluster reads. It exists so
// `fix` can produce an applyable patch with no AI provider reachable.
//
// Every inferrer declines rather than guesses, and no inferred plan is adopted
// unless fix.Concretize returns it ApplyEligible. Callers force shadow
// verification on for any inferred plan.
package infer

import (
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

// ContainerName returns the failing container from the "Container image
// <name>: <image>" evidence the pod analyzer attaches.
func ContainerName(f analyzer.Finding) string {
	const prefix = "container image "
	for _, evidence := range f.Evidence {
		if strings.HasPrefix(strings.ToLower(evidence.Label), prefix) {
			return strings.TrimSpace(evidence.Label[len(prefix):])
		}
	}
	return ""
}

// EvidenceValue returns the value of the first evidence whose label starts
// with labelPrefix, or "".
func EvidenceValue(f analyzer.Finding, labelPrefix string) string {
	for _, evidence := range f.Evidence {
		if strings.HasPrefix(evidence.Label, labelPrefix) {
			return evidence.Value
		}
	}
	return ""
}
