package redact

import "regexp"

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(token|password|passwd|secret|api[_-]?key|authorization)\s*[:=]\s*['"]?[^'"\s]+`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
}

func Text(value string) string {
	out := value
	for _, pattern := range patterns {
		out = pattern.ReplaceAllString(out, "[REDACTED]")
	}
	return out
}
