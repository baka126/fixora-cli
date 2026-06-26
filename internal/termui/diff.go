package termui

import "strings"

const (
	diffReset = "\033[0m"
	diffRed   = "\033[31m"
	diffGreen = "\033[32m"
	diffCyan  = "\033[36m"
	diffBold  = "\033[1m"
)

func ColorDiff(diff string, noColor bool) string {
	if noColor || strings.TrimSpace(diff) == "" {
		return diff
	}
	lines := strings.SplitAfter(wrapDiffForTerminal(diff, wideTextWidth), "\n")
	var b strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSuffix(line, "\n")
		suffix := ""
		if strings.HasSuffix(line, "\n") {
			suffix = "\n"
		}
		switch {
		case strings.HasPrefix(trimmed, "@@"):
			b.WriteString(diffCyan + diffBold + trimmed + diffReset + suffix)
		case strings.HasPrefix(trimmed, "+") && !strings.HasPrefix(trimmed, "+++"):
			b.WriteString(diffGreen + trimmed + diffReset + suffix)
		case strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "---"):
			b.WriteString(diffRed + trimmed + diffReset + suffix)
		default:
			b.WriteString(line)
		}
	}
	return b.String()
}

// DisplayDiff prepares a diff for a terminal before optionally adding color.
// It is intentionally presentation-only; callers retain the original patch.
func DisplayDiff(diff string, noColor bool) string {
	return ColorDiff(wrapDiffForTerminal(diff, wideTextWidth), noColor)
}

// wrapDiffForTerminal only changes the visual representation of a diff. The
// complete, unwrapped YAML remains in the patch file used for editing, shadow
// verification, and delivery.
func wrapDiffForTerminal(diff string, width int) string {
	if width < 32 || strings.TrimSpace(diff) == "" {
		return diff
	}
	var b strings.Builder
	for _, line := range strings.SplitAfter(diff, "\n") {
		hasNewline := strings.HasSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\n")
		continuation := false
		for {
			available := width
			if continuation {
				b.WriteString("  ")
				available -= 2
			}
			if len(line) <= available {
				b.WriteString(line)
				break
			}
			b.WriteString(line[:available-1])
			b.WriteString("\\\n")
			line = line[available-1:]
			continuation = true
		}
		if hasNewline {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
