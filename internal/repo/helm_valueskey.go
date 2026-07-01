package repo

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var valuesRefRe = regexp.MustCompile(`\.Values\.([A-Za-z0-9_.]+)`)

// templateDiskPath maps a "# Source:" path (e.g. "myapp/templates/deploy.yaml"
// or "myapp/charts/redis/templates/ss.yaml") to a file on disk by stripping
// the leading chart-name segment and joining the remainder to chartPath.
func templateDiskPath(chartPath, sourcePath string) string {
	slashed := filepath.ToSlash(sourcePath)
	if i := strings.IndexByte(slashed, '/'); i >= 0 {
		return filepath.Join(chartPath, filepath.FromSlash(slashed[i+1:]))
	}
	return filepath.Join(chartPath, sourcePath)
}

// valuesRefsInTemplate returns the dotted .Values.X keys referenced in the
// template text, de-duplicated in first-appearance order.
func valuesRefsInTemplate(templateText string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range valuesRefRe.FindAllStringSubmatch(templateText, -1) {
		key := strings.TrimRight(m[1], ".")
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

// templateLineForField returns the .Values refs on the first template line
// whose (trimmed) YAML key equals leafKey. found is true when such a line
// exists even if it carries no refs (a statically-set field).
func templateLineForField(templateText, leafKey string) (refs []string, found bool) {
	for _, line := range strings.Split(templateText, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, leafKey+":") {
			return valuesRefsInTemplate(line), true
		}
	}
	return nil, false
}

// valuesFileLookup walks the dotted key across each values file in order and
// returns the first scalar value found, normalized to a string.
func valuesFileLookup(valuesFiles []string, dottedKey string) (string, bool) {
	parts := strings.Split(dottedKey, ".")
	for _, vf := range valuesFiles {
		data, err := os.ReadFile(vf)
		if err != nil {
			continue
		}
		var m map[string]any
		if err := yaml.Unmarshal(data, &m); err != nil {
			continue
		}
		var cur any = m
		ok := true
		for _, p := range parts {
			cm, isMap := cur.(map[string]any)
			if !isMap {
				ok = false
				break
			}
			cur, ok = cm[p]
			if !ok {
				break
			}
		}
		if ok {
			if _, isMap := cur.(map[string]any); !isMap {
				return normalize(cur), true
			}
		}
	}
	return "", false
}
