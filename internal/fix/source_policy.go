package fix

const SourceOwnershipReviewReason = "GitOps-managed workload requires source-level review and delivery."

// SourceReviewAllowed distinguishes source ownership from other policy blockers.
// It grants no runtime authority; source certification and explicit consent remain required.
func SourceReviewAllowed(p Plan) bool {
	if p.ApplyEligible || p.Recovered || p.Confidence < 90 || p.Confidence > 100 || len(p.BlockedReasons) == 0 {
		return false
	}
	for _, reason := range p.BlockedReasons {
		if reason != SourceOwnershipReviewReason && reason != "GitOps-managed workload requires source-level patching." {
			return false
		}
	}
	for _, g := range p.Guardrails {
		if g == "ownership-unverified" || g == "controller-review-only" {
			return false
		}
	}
	return true
}
