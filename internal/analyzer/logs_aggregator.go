package analyzer

import (
	"regexp"
	"strings"
)

var timestampRegex = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})\s*`)

func AggregateLogs(logs string) string {
	if logs == "" {
		return ""
	}

	lines := strings.Split(logs, "\n")
	seen := make(map[string]bool)
	var priorityLogs []string
	var otherLogs []string

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Strip timestamp
		cleanLine := timestampRegex.ReplaceAllString(line, "")
		cleanLine = strings.TrimSpace(cleanLine)

		if seen[cleanLine] {
			continue
		}
		seen[cleanLine] = true

		if strings.Contains(cleanLine, "panic:") || strings.Contains(cleanLine, "Exception") || strings.Contains(cleanLine, "goroutine") {
			priorityLogs = append(priorityLogs, cleanLine)
		} else {
			otherLogs = append(otherLogs, cleanLine)
		}
	}

	var aggregated []string
	aggregated = append(aggregated, priorityLogs...)
	aggregated = append(aggregated, otherLogs...)

	return strings.Join(aggregated, "\n")
}
