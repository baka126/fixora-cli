package fix

import "strings"

// WithTargetPolicy is applied after deterministic/AI concretization and again
// at delivery. Flags cannot make immutable or non-reconciling targets safe.
func WithTargetPolicy(p Plan) Plan {
	kind, _, _ := strings.Cut(p.Resource, "/")
	reason := ""
	switch strings.ToLower(kind) {
	case "deployment":
	case "statefulset", "daemonset", "cronjob", "pod":
		p.Guardrails = appendUnique(p.Guardrails, "controller-preflight-required")
		p.Guardrails = appendUnique(p.Guardrails, "shadow-required")
		if strings.EqualFold(kind, "Pod") && p.Strategy != "image" && p.Strategy != "fix-architecture" {
			reason = "Existing Pod template changes require source review and explicit recreation; Fixora will not recreate the Pod."
		}
		if strings.EqualFold(kind, "DaemonSet") && p.Strategy == "resources" {
			reason = "DaemonSet resource changes require fleet-wide capacity review; a single node or shadow Pod cannot establish fleet safety."
		}
		if strings.EqualFold(kind, "CronJob") {
			p.Warnings = appendUnique(p.Warnings, "This template change affects future Jobs only. Existing Jobs remain unchanged; verification of the next scheduled run is pending.")
		}
	case "replicaset":
		reason = "ReplicaSet template changes do not replace existing Pods; review the source and replacement procedure."
	case "job":
		reason = "Existing Job templates require source review and explicit replay/recreation; Fixora will not replay the Job."
	default:
		if p.CanApply {
			reason = "This resource has no automated workload delivery policy; review the source proposal."
		}
	}
	for _, g := range p.Guardrails {
		if g == "ownership-unverified" {
			reason = "Controlling ownership could not be verified; review the owner and source before delivery."
		}
	}
	if reason != "" {
		p.CanApply = false
		p.ApplyEligible = false
		p.Safe = false
		p.Risk = "review-only"
		p.BlockedReasons = appendUnique(p.BlockedReasons, reason)
		p.Guardrails = appendUnique(p.Guardrails, "controller-review-only")
	}
	return p
}

// WithControllerReview retains an inferred proposal when live update semantics
// require human source review. It does not imply AI generated the proposal.
func WithControllerReview(p Plan, reason string) Plan {
	p.CanApply, p.ApplyEligible, p.Safe = false, false, false
	p.Risk = "review-only"
	p.BlockedReasons = appendUnique(p.BlockedReasons, reason)
	p.Guardrails = appendUnique(p.Guardrails, "controller-review-only")
	return p
}
