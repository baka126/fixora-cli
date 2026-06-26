package termui

import (
	"fmt"
	"io"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

const (
	compactTextWidth = 88
	wideTextWidth    = 108
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
	fmt.Fprintln(w, "Likely cause")
	writeWrapped(w, "  ", f.Summary, textWidth(opts))
	fmt.Fprintln(w)
	if len(f.Recommendations) > 0 {
		fmt.Fprintln(w, "Recommended next step")
		writeWrapped(w, "  ", f.Recommendations[0].Title+": "+f.Recommendations[0].Description, textWidth(opts))
		fmt.Fprintln(w)
	}
	if len(p.BlockedReasons) > 0 {
		fmt.Fprintln(w, "Why no direct auto-fix")
		for _, reason := range p.BlockedReasons {
			writeWrapped(w, "  - ", reason, textWidth(opts))
		}
		fmt.Fprintln(w)
	}
	if p.RollbackCommand != "" {
		fmt.Fprintln(w, "Rollback hint")
		writeWrapped(w, "  ", p.RollbackCommand, textWidth(opts))
		fmt.Fprintln(w)
	}
	if proof {
		fmt.Fprintln(w, "Proof")
		for _, ev := range f.Evidence {
			writeWrapped(w, "  - ", ev.Label+": "+trim(ev.Value, 180), textWidth(opts))
		}
		for _, log := range f.Logs {
			writeWrapped(w, "  - ", log.Source+" log: "+trim(log.Text, 220), textWidth(opts))
		}
	}
}

func Plan(w io.Writer, p fix.Plan, opts Options) {
	fmt.Fprintf(w, "Fixora plan for %s\n", p.Resource)
	fmt.Fprintf(w, "Strategy: %s | Confidence: %d%% | Risk: %s | Apply: %t\n\n", p.Strategy, p.Confidence, p.Risk, p.CanApply)
	for _, step := range p.Steps {
		writeWrapped(w, "+ ", step, textWidth(opts))
	}
	for _, reason := range p.BlockedReasons {
		writeWrapped(w, "x ", reason, textWidth(opts))
	}
	for _, warning := range p.Warnings {
		writeWrapped(w, "! ", warning, textWidth(opts))
	}
	if p.RollbackCommand != "" {
		fmt.Fprintln(w, "\nRollback:")
		writeWrapped(w, "  ", p.RollbackCommand, textWidth(opts))
	}
}

func textWidth(opts Options) int {
	if opts.Wide {
		return wideTextWidth
	}
	return compactTextWidth
}

// writeWrapped keeps human-facing incident output readable in terminals that do
// not reliably soft-wrap long lines. It intentionally does not alter data used
// for patch validation, shadow deployment, or delivery.
func writeWrapped(w io.Writer, prefix, value string, width int) {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		fmt.Fprintln(w, prefix)
		return
	}
	if width <= len(prefix)+8 {
		width = len(prefix) + 8
	}
	continuation := strings.Repeat(" ", len(prefix))
	linePrefix := prefix
	lineLen := len(linePrefix)
	for _, word := range strings.Fields(value) {
		wordLen := len(word)
		separator := 0
		if lineLen > len(linePrefix) {
			separator = 1
		}
		if lineLen+separator+wordLen > width && lineLen > len(linePrefix) {
			fmt.Fprintln(w)
			linePrefix = continuation
			lineLen = len(linePrefix)
			separator = 0
		}
		if lineLen == len(linePrefix) {
			fmt.Fprint(w, linePrefix)
		} else if separator == 1 {
			fmt.Fprint(w, " ")
		}
		lineLen += separator
		for len(word) > width-lineLen {
			available := width - lineLen
			fmt.Fprint(w, word[:available])
			word = word[available:]
			fmt.Fprintln(w)
			linePrefix = continuation
			lineLen = len(linePrefix)
			fmt.Fprint(w, linePrefix)
		}
		fmt.Fprint(w, word)
		lineLen += len(word)
	}
	fmt.Fprintln(w)
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
