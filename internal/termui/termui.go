package termui

import (
	"fmt"
	"io"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

type Options struct {
	Wide    bool
	NoColor bool
}

func Findings(w io.Writer, findings []analyzer.Finding, opts Options) {
	fmt.Fprintf(w, "Fixora findings: %d\n\n", len(findings))
	if len(findings) == 0 {
		fmt.Fprintln(w, "No active findings matched the current filters.")
		return
	}
	fmt.Fprintf(w, "%-10s %-14s %-32s %-18s %s\n", "SEVERITY", "NAMESPACE", "RESOURCE", "STATUS", "SUMMARY")
	for _, f := range findings {
		resource := f.ResourceKind + "/" + f.ResourceName
		if f.PodName != "" && opts.Wide {
			resource += " pod/" + f.PodName
		}
		fmt.Fprintf(w, "%-10s %-14s %-32s %-18s %s\n",
			color(f.Severity, f.Severity, opts.NoColor),
			trim(f.Namespace, 14),
			trim(resource, 32),
			trim(f.Status, 18),
			trim(f.Summary, ternary(opts.Wide, 180, 72)),
		)
	}
}

func Why(w io.Writer, f analyzer.Finding, p fix.Plan, proof bool, opts Options) {
	fmt.Fprintf(w, "Fixora why: %s/%s\n", f.ResourceKind, f.ResourceName)
	fmt.Fprintf(w, "Namespace: %s\nStatus: %s\nSeverity: %s\nConfidence: %d%%\nRisk: %s\n\n", f.Namespace, f.Status, f.Severity, p.Confidence, p.Risk)
	fmt.Fprintf(w, "Likely cause\n  %s\n\n", f.Summary)
	if len(f.Recommendations) > 0 {
		fmt.Fprintf(w, "Recommended next step\n  %s: %s\n\n", f.Recommendations[0].Title, f.Recommendations[0].Description)
	}
	if len(p.BlockedReasons) > 0 {
		fmt.Fprintln(w, "Why no direct auto-fix")
		for _, reason := range p.BlockedReasons {
			fmt.Fprintf(w, "  - %s\n", reason)
		}
		fmt.Fprintln(w)
	}
	if p.RollbackCommand != "" {
		fmt.Fprintf(w, "Rollback hint\n  %s\n\n", p.RollbackCommand)
	}
	if proof {
		fmt.Fprintln(w, "Proof")
		for _, ev := range f.Evidence {
			fmt.Fprintf(w, "  - %s: %s\n", ev.Label, trim(ev.Value, 180))
		}
		for _, log := range f.Logs {
			fmt.Fprintf(w, "  - %s log: %s\n", log.Source, trim(log.Text, 220))
		}
	}
}

func Plan(w io.Writer, p fix.Plan, opts Options) {
	fmt.Fprintf(w, "Fixora plan for %s\n", p.Resource)
	fmt.Fprintf(w, "Strategy: %s | Confidence: %d%% | Risk: %s | Apply: %t\n\n", p.Strategy, p.Confidence, p.Risk, p.CanApply)
	for _, step := range p.Steps {
		fmt.Fprintf(w, "+ %s\n", step)
	}
	for _, reason := range p.BlockedReasons {
		fmt.Fprintf(w, "x %s\n", reason)
	}
	for _, warning := range p.Warnings {
		fmt.Fprintf(w, "! %s\n", warning)
	}
	if p.RollbackCommand != "" {
		fmt.Fprintf(w, "\nRollback: %s\n", p.RollbackCommand)
	}
}

func color(severity, value string, noColor bool) string {
	if noColor {
		return value
	}
	switch strings.ToLower(severity) {
	case "critical", "high":
		return "\033[31m" + value + "\033[0m"
	case "medium":
		return "\033[33m" + value + "\033[0m"
	case "low":
		return "\033[36m" + value + "\033[0m"
	default:
		return value
	}
}

func trim(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	if limit < 4 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func ternary(cond bool, yes, no int) int {
	if cond {
		return yes
	}
	return no
}
