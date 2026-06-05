package redact

import (
	"bytes"
	"math"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

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
	{
		pattern:     regexp.MustCompile(`(?i)\b(?:postgres|postgresql|mysql|mongodb|redis)://[^\s'"]+`),
		replacement: "[REDACTED_CONNECTION_STRING]",
	},
	{
		pattern:     regexp.MustCompile(`(?i)\bjdbc:[^\s'"]*(?:user(?:name)?|password)=([^;&\s'"]+)`),
		replacement: "[REDACTED_JDBC_URL]",
	},
}

func Text(value string) string {
	out := value
	for _, r := range rules {
		out = r.pattern.ReplaceAllString(out, r.replacement)
	}
	out = redactHighEntropyTokens(out)
	return out
}

func KubernetesText(value string) string {
	structured, ok := structuredKubernetesText(value)
	if !ok {
		return Text(value)
	}
	return Text(structured)
}

func structuredKubernetesText(value string) (string, bool) {
	decoder := yaml.NewDecoder(strings.NewReader(value))
	var docs []any
	for {
		var doc any
		err := decoder.Decode(&doc)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return "", false
		}
		if doc == nil {
			continue
		}
		if !isStructured(doc) {
			return "", false
		}
		redactObject(doc, nil)
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		return "", false
	}
	var b bytes.Buffer
	encoder := yaml.NewEncoder(&b)
	encoder.SetIndent(2)
	for i, doc := range docs {
		if i > 0 {
			b.WriteString("---\n")
		}
		if err := encoder.Encode(doc); err != nil {
			_ = encoder.Close()
			return "", false
		}
	}
	if err := encoder.Close(); err != nil {
		return "", false
	}
	return b.String(), true
}

func redactObject(value any, path []string) {
	switch v := value.(type) {
	case map[string]any:
		if strings.EqualFold(stringValue(v["kind"]), "secret") {
			redactMapValue(v, "data")
			redactMapValue(v, "stringData")
		}
		for key, child := range v {
			lower := strings.ToLower(key)
			switch {
			case isKubeconfigSecretField(lower):
				v[key] = "[REDACTED]"
				continue
			case lower == "auth-provider":
				if m, ok := child.(map[string]any); ok {
					redactMapValue(m, "config")
					v[key] = m
					continue
				}
			case lower == "exec":
				if m, ok := child.(map[string]any); ok {
					redactExecEnv(m)
					v[key] = m
					continue
				}
			case lower == "env":
				v[key] = redactEnvList(child)
				continue
			}
			redactObject(child, append(path, lower))
			if s, ok := v[key].(string); ok && secretLikeValue(s) {
				v[key] = "[REDACTED]"
			}
		}
	case []any:
		for i, child := range v {
			redactObject(child, path)
			if s, ok := v[i].(string); ok && secretLikeValue(s) {
				v[i] = "[REDACTED]"
			}
		}
	}
}

func redactMapValue(m map[string]any, key string) {
	if val, ok := m[key]; ok {
		if mapVal, isMap := val.(map[string]any); isMap {
			for k := range mapVal {
				mapVal[k] = "[REDACTED]"
			}
		} else {
			m[key] = "[REDACTED]"
		}
	}
}

func redactExecEnv(m map[string]any) {
	env, ok := m["env"]
	if !ok {
		return
	}
	m["env"] = redactEnvList(env)
}

func redactEnvList(value any) any {
	items, ok := value.([]any)
	if !ok {
		return value
	}
	for _, item := range items {
		env, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.ToUpper(stringValue(env["name"]))
		if _, ok := env["value"]; ok {
			if secretLikeEnvName(name) || secretLikeValue(stringValue(env["value"])) {
				env["value"] = "[REDACTED]"
			}
		}
	}
	return items
}

func isStructured(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}

func isKubeconfigSecretField(key string) bool {
	switch key {
	case "client-certificate-data", "client-key-data", "certificate-authority-data", "token", "username", "password":
		return true
	default:
		return false
	}
}

func secretLikeEnvName(name string) bool {
	for _, marker := range []string{"PASSWORD", "TOKEN", "SECRET", "API_KEY", "ACCESS_KEY", "PRIVATE_KEY", "CONNECTION_STRING", "DATABASE_URL", "DB_URL", "REDIS_URL", "MONGO_URL", "POSTGRES_URL"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

func secretLikeValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return looksLikeConnectionString(value) || looksHighEntropy(value)
}

func looksLikeConnectionString(value string) bool {
	return regexp.MustCompile(`(?i)\b(?:postgres|postgresql|mysql|mongodb|redis)://`).MatchString(value) ||
		regexp.MustCompile(`(?i)\bjdbc:[^\s'"]*(?:user(?:name)?|password)=`).MatchString(value)
}

func redactHighEntropyTokens(value string) string {
	return regexp.MustCompile(`[A-Za-z0-9_+/=-]{32,}`).ReplaceAllStringFunc(value, func(token string) string {
		if looksHighEntropy(token) {
			return "[REDACTED]"
		}
		return token
	})
}

func looksHighEntropy(value string) bool {
	value = strings.Trim(value, "\"'")
	if len(value) < 32 {
		return false
	}
	hasLetter, hasDigit := false, false
	counts := map[rune]int{}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '_' || r == '-' || r == '+' || r == '/' || r == '=':
		default:
			return false
		}
		counts[r]++
	}
	if !hasLetter || !hasDigit {
		return false
	}
	var entropy float64
	total := float64(len(value))
	for _, count := range counts {
		p := float64(count) / total
		entropy -= p * math.Log2(p)
	}
	return entropy >= 3.8
}

func stringValue(value any) string {
	s, _ := value.(string)
	return s
}
