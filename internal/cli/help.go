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
	case "ask":
		help = `Usage: kubectl fixora ask <request> [-n namespace] [--continue] [--select number]
       kubectl fixora ask --session ID <follow-up>
       kubectl fixora ask sessions list | delete <ID>

Describe an active Kubernetes issue in natural language. Fixora resolves the
request to live analyzer findings, then answers with fresh, redacted incident
evidence. Sessions can be resumed across commands. Any repair request only
opens the existing guarded fix preview; Ask never applies or delivers a repair.

Examples:
  kubectl fixora ask "why is the API crashing?" -n payments
  kubectl fixora ask --session a1b2c3d4e5f6 "what changed since last time?"
  kubectl fixora ask "preview a fix for the API" -n payments
  kubectl fixora ask sessions list
  kubectl fixora ask sessions delete a1b2c3d4e5f6

Session summaries are stored locally with private file permissions, scoped to
the current cluster, and expire after 30 days. Each AI-backed turn sends the
current redacted incident evidence and bounded conversation summaries to the
configured provider. Session listing never prints conversation text.
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
  kubectl fixora fix pvc/data -n prod --pvc-policy ./pvc-repair.yaml --preview

Useful flags: --context, --ai, --no-ai, --yes, --quick, --edit-patch, --startup-baseline, -o json
Startup baselines must match the Deployment and image; restoration always runs shadow verification.
Fix enables AI when configured; --no-ai disables it. AI candidates and startup/cache
repairs require shadow verification, including with --quick. Other quick repairs
skip shadow verification. Use --yes for scripted delivery.
PVC repairs require an exact local --pvc-policy. Expansion is non-rollbackable and remains
pending until the provisioner reports capacity; Fixora never mounts a production PVC in a shadow.
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
	case "cleanup":
		help = `Usage: kubectl fixora cleanup [--context name] [--yes] [-o json]

Preview isolated Fixora shadow run namespaces. NetworkPolicy witness namespaces
and other user namespaces are excluded. Add --yes to request UID-guarded deletion.

Examples:
  kubectl fixora cleanup
  kubectl fixora cleanup --context staging --yes
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
	"preflight", "policy-check", "lint", "profiles", "cache", "memory", "cleanup", "ask",
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
