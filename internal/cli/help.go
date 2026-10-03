package cli

import (
	"fmt"
	"io"
	"strings"
)

// printCommandHelp keeps the common tasks short while leaving the full
// reference available through help --advanced.
func printCommandHelp(w io.Writer, command string) bool {
	var help string
	switch command {
	case "scan", "incidents":
		help = `Usage: kubectl fixora scan [-A | -n namespace] [flags]

List current workload failures. Logs and typed Kubernetes reads are included.

Examples:
  kubectl fixora scan -A
  kubectl fixora scan -n prod -o json

Useful flags: --context, --selector, --no-ai, --filter
`
	case "why":
		help = `Usage: kubectl fixora why <kind/name> [-n namespace] [flags]

Explain the likely cause, supporting evidence, and a safe next step.
Omit the resource in a terminal to select an incident interactively.

Examples:
  kubectl fixora why deployment/api -n prod
  kubectl fixora why service/api -n prod --proof

Useful flags: --context, --ai, --proof, -o json
`
	case "fix", "repair":
		help = `Usage: kubectl fixora fix <kind/name> [-n namespace] [flags]

Walk through the cause, proposed change, shadow verification, and delivery.
Omit the resource in a terminal to select an incident interactively.

Delivery:
  --delivery patch    Save a local patch (default)
  --delivery cluster  Apply after validation and confirmation
  --delivery pr       Open a PR/MR from --repo

Examples:
  kubectl fixora fix deployment/api -n prod --preview
  kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/acme/api:v1.2.3
  kubectl fixora fix deployment/api -n prod --repo ./charts/api --delivery pr --yes

Useful flags: --context, --ai, --no-ai, --yes, --quick, --edit-patch, -o json
Fix enables AI when configured; --no-ai disables it. --quick skips shadow
verification. Use --yes for scripted delivery.
`
	case "coordinate", "fix-set":
		help = `Usage: kubectl fixora coordinate <kind/name>... [-n namespace] [flags]

Validate an ordered set of fixes, then confirm delivery together.

Examples:
  kubectl fixora coordinate deployment/api configmap/api-config -n prod
  kubectl fixora coordinate --from deployment/api -n prod

Useful flags: --context, --yes, --from
`
	case "doctor":
		help = `Usage: kubectl fixora doctor [-A | -n namespace] [flags]

Check access and capabilities needed for incident analysis.

Examples:
  kubectl fixora doctor -A
  kubectl fixora ai doctor

Useful flags: --context, -o json
`
	case "ai":
		help = `Usage: kubectl fixora ai doctor

Check the configured AI provider without scanning the cluster.
`
	case "debug":
		help = `Usage: kubectl fixora debug <tool> [resource] [flags]

Tools: trace, graph, storage, rbac, dns, security, node-pressure,
       changes, readiness, rollback

Examples:
  kubectl fixora debug trace service/api -n prod
  kubectl fixora debug storage -A
`
	case "source":
		help = `Usage: kubectl fixora source <tool> [path] [flags]

Tools: repo, validate, lint, preflight, policy-check

Examples:
  kubectl fixora source validate ./charts/api
  kubectl fixora source preflight -f manifests/deployment.yaml
`
	case "ui", "cluster", "dashboard":
		help = `Usage: kubectl fixora ui [-A | -n namespace] [flags]
       kubectl fixora cluster [flags]

Open a compact incident dashboard or the full-screen cluster dashboard.

Examples:
  kubectl fixora ui -A
  kubectl fixora cluster
`
	default:
		if suggestCommand(command) == command {
			printAdvancedHelp(w)
			return true
		}
		return false
	}
	fmt.Fprint(w, help)
	return true
}

var suggestedCommands = []string{
	"scan", "why", "fix", "coordinate", "ui", "cluster", "doctor",
	"debug", "source", "ai", "auth", "config", "version", "status",
	"filters", "integrations", "serve", "health", "watch", "report",
	"bundle", "predict", "cost", "graph", "trace", "storage", "rbac",
	"dns", "security", "node-pressure", "repo", "validate", "changes",
	"preflight", "policy-check", "lint", "profiles", "cache", "memory",
}

func suggestCommand(input string) string {
	input = strings.ToLower(strings.TrimSpace(input))
	best, distance := "", 3
	for _, candidate := range suggestedCommands {
		d := commandDistance(input, candidate)
		if d < distance {
			best, distance = candidate, d
		}
	}
	if distance <= 2 {
		return best
	}
	return ""
}

func commandDistance(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == len(b) {
		for i := 0; i+1 < len(a); i++ {
			if a[:i]+a[i+1:i+2]+a[i:i+1]+a[i+2:] == b {
				return 1
			}
		}
	}
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			current[j] = min(current[j-1]+1, previous[j]+1, previous[j-1]+cost)
		}
		previous = current
	}
	return previous[len(b)]
}
