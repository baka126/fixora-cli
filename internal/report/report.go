package report

import (
	"fmt"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

func Markdown(f analyzer.Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Fixora Local Diagnostic Report\n\n")
	fmt.Fprintf(&b, "- Generated: %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "- Resource: `%s/%s`\n", f.ResourceKind, f.ResourceName)
	fmt.Fprintf(&b, "- Namespace: `%s`\n", f.Namespace)
	if f.PodName != "" {
		fmt.Fprintf(&b, "- Pod: `%s`\n", f.PodName)
	}
	fmt.Fprintf(&b, "- Status: `%s`\n", f.Status)
	fmt.Fprintf(&b, "- Severity: `%s`\n", f.Severity)
	fmt.Fprintf(&b, "- Category: `%s`\n\n", f.Category)

	fmt.Fprintf(&b, "## Summary\n\n%s\n\n", f.Summary)
	if len(f.Evidence) > 0 {
		fmt.Fprintf(&b, "## Evidence\n\n")
		for _, ev := range f.Evidence {
			fmt.Fprintf(&b, "- **%s:** %s\n", ev.Label, trimLong(ev.Value, 500))
		}
		fmt.Fprintln(&b)
	}
	if len(f.Logs) > 0 {
		fmt.Fprintf(&b, "## Logs\n\n")
		for _, log := range f.Logs {
			fmt.Fprintf(&b, "### %s\n\n```text\n%s\n```\n\n", log.Source, trimLong(log.Text, 2000))
		}
	}
	if len(f.OwnerChain) > 0 {
		fmt.Fprintf(&b, "## Owner Chain\n\n")
		for _, owner := range f.OwnerChain {
			fmt.Fprintf(&b, "- `%s`\n", owner)
		}
		fmt.Fprintln(&b)
	}
	if hasGitOps(f.GitOps) {
		fmt.Fprintf(&b, "## GitOps Hints\n\n")
		writeField(&b, "Managed by", f.GitOps.ManagedBy)
		writeField(&b, "Helm release", f.GitOps.HelmRelease)
		writeField(&b, "Helm chart", f.GitOps.HelmChart)
		writeField(&b, "Argo hint", f.GitOps.ArgoHint)
		writeField(&b, "Flux hint", f.GitOps.FluxHint)
		writeField(&b, "Target advice", f.GitOps.TargetAdvice)
		fmt.Fprintln(&b)
	}
	if len(f.Recommendations) > 0 {
		fmt.Fprintf(&b, "## Recommendations\n\n")
		for _, rec := range f.Recommendations {
			fmt.Fprintf(&b, "- **%s:** %s", rec.Title, rec.Description)
			if rec.PatchType != "" {
				fmt.Fprintf(&b, " (`%s`)", rec.PatchType)
			}
			fmt.Fprintln(&b)
		}
		fmt.Fprintln(&b)
	}
	if f.AI != nil {
		fmt.Fprintf(&b, "## AI Analysis\n\n")
		writeField(&b, "Summary", f.AI.Summary)
		writeField(&b, "Root cause", f.AI.RootCause)
		writeField(&b, "Recommended fix", f.AI.RecommendedFix)
		if len(f.AI.Commands) > 0 {
			fmt.Fprintf(&b, "\nSuggested commands:\n")
			for _, cmd := range f.AI.Commands {
				fmt.Fprintf(&b, "- `%s`\n", cmd)
			}
		}
		if len(f.AI.Warnings) > 0 {
			fmt.Fprintf(&b, "\nWarnings:\n")
			for _, warning := range f.AI.Warnings {
				fmt.Fprintf(&b, "- %s\n", warning)
			}
		}
	}
	return b.String()
}

func writeField(b *strings.Builder, label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	fmt.Fprintf(b, "- **%s:** %s\n", label, value)
}

func hasGitOps(h analyzer.GitOpsHints) bool {
	return h.ManagedBy != "" || h.HelmRelease != "" || h.HelmChart != "" || h.FluxHint != "" || h.ArgoHint != "" || h.TargetAdvice != ""
}

func trimLong(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n... truncated ..."
}
