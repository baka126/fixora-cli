package analyzer

import "strings"

// classifyReadError reports whether a Kubernetes read error indicates the read
// was denied by RBAC (Forbidden/Unauthorized) rather than a genuine absence or
// transport failure. Single source of truth for RBAC-denial detection.
func classifyReadError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "forbidden") || strings.Contains(msg, "unauthorized")
}

// rbacAwareSkip builds a SkippedCheck for a failed read, labeling RBAC-denied
// reads consistently so absence-of-findings is never mistaken for health.
func rbacAwareSkip(name string, err error) SkippedCheck {
	if classifyReadError(err) {
		return SkippedCheck{
			Name:        name,
			Reason:      "read denied by RBAC — grant read-diagnostics access: " + err.Error(),
			RBACBlocked: true,
		}
	}
	return SkippedCheck{Name: name, Reason: err.Error()}
}
