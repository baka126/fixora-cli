package redact

import "regexp"

type rule struct {
	pattern     *regexp.Regexp
	replacement string
}

var rules = []rule{
	{
		pattern:     regexp.MustCompile(`(?i)((?:https?|ftp|tcp|udp)://)[^:\s]+:[^@\s]+(@[^\s/]+)`),
		replacement: "${1}[REDACTED]${2}",
	},
	{
		pattern:     regexp.MustCompile(`(?i)("?(?:token|password|passwd|secret|api[_-]?key|authorization)"?\s*[:=]\s*['"]?)(?:(?:Basic|Bearer)\s+)?[a-zA-Z0-9~_=\.\-\+/]+`),
		replacement: "${1}[REDACTED]",
	},
	{
		pattern:     regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
		replacement: "[REDACTED]",
	},
	{
		pattern:     regexp.MustCompile(`(?i)bearer\s+[a-zA-Z0-9\._~+/=\-]+`),
		replacement: "Bearer [REDACTED]",
	},
	{
		pattern:     regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`),
		replacement: "[REDACTED]",
	},
	{
		pattern:     regexp.MustCompile(`(?i)\bAKIA[0-9A-Z]{16}\b`),
		replacement: "[REDACTED]",
	},
	{
		pattern:     regexp.MustCompile(`(?s)-----BEGIN (?:RSA |DSA |EC |OPENSSH |)PRIVATE KEY-----.*?-----END (?:RSA |DSA |EC |OPENSSH |)PRIVATE KEY-----`),
		replacement: "[REDACTED]",
	},
	{
		pattern:     regexp.MustCompile(`(?i)((?:https?|ftp|tcp|udp)://)[^:\s]+:[^@\s]+(@[^\s/]+)`),
		replacement: "${1}[REDACTED]${2}",
	},
}

func Text(value string) string {
	out := value
	for _, r := range rules {
		out = r.pattern.ReplaceAllString(out, r.replacement)
	}
	return out
}
