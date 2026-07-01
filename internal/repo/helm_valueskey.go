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

// ValuesKeySuggestion is a best-effort mapping of one managed-divergent field
// to the chart values key(s) that control it. Candidates and Note carry only
// values KEY NAMES, never values-file values.
type ValuesKeySuggestion struct {
	FieldPath  string
	Confidence string // "pinpointed" | "likely" | "uncertain" | "unmapped"
	Candidates []string
	Note       string
}

// SuggestValuesKeys maps each managed-divergent field in rv to candidate values
// keys, reading the owning template once. Offline, read-only; never errors.
func SuggestValuesKeys(loc HelmSourceLocation, rv RenderValidation) []ValuesKeySuggestion {
	var templateText string
	if loc.TemplateFile != "" {
		if data, err := os.ReadFile(templateDiskPath(loc.ChartPath, loc.TemplateFile)); err == nil {
			templateText = string(data)
		}
	}
	allRefs := valuesRefsInTemplate(templateText)

	var out []ValuesKeySuggestion
	for _, fv := range rv.Fields {
		if fv.Class != "managed-divergent" {
			continue
		}
		out = append(out, suggestForField(fv, templateText, allRefs, loc))
	}
	return out
}

func suggestForField(fv FieldVerdict, templateText string, allRefs []string, loc HelmSourceLocation) ValuesKeySuggestion {
	s := ValuesKeySuggestion{FieldPath: fv.Path}

	leaf := fv.Path
	if i := strings.LastIndexByte(fv.Path, '.'); i >= 0 {
		leaf = fv.Path[i+1:]
	}
	lineRefs, _ := templateLineForField(templateText, leaf)

	switch {
	case len(lineRefs) == 1:
		s.Confidence = "pinpointed"
		s.Candidates = lineRefs
		return s
	case len(lineRefs) > 1:
		if m := valueMatched(lineRefs, fv.RenderedValue, loc.ValuesFiles); len(m) == 1 {
			s.Confidence = "likely"
			s.Candidates = m
		} else {
			s.Confidence = "uncertain"
			s.Candidates = lineRefs
		}
		return s
	}

	// No usable line refs: fall back to value-match over all template refs.
	if m := valueMatched(allRefs, fv.RenderedValue, loc.ValuesFiles); len(m) == 1 {
		s.Confidence = "likely"
		s.Candidates = m
		return s
	} else if len(m) > 1 {
		s.Confidence = "uncertain"
		s.Candidates = m
		return s
	}
	if len(allRefs) > 0 {
		s.Confidence = "uncertain"
		s.Candidates = allRefs
		return s
	}
	s.Confidence = "unmapped"
	s.Note = "field " + fv.Path + ": could not map to a values key; edit template " + loc.TemplateFile
	if len(loc.ValuesFiles) > 0 {
		s.Note += " or values files " + strings.Join(loc.ValuesFiles, ", ")
	}
	return s
}

// valueMatched returns the subset of keys whose values-file value equals
// renderedValue. Returns nothing when renderedValue is empty (Secret findings),
// so no values-file value is ever read into a Secret suggestion.
func valueMatched(keys []string, renderedValue string, valuesFiles []string) []string {
	if renderedValue == "" {
		return nil
	}
	var out []string
	for _, k := range keys {
		if v, ok := valuesFileLookup(valuesFiles, k); ok && v == renderedValue {
			out = append(out, k)
		}
	}
	return out
}
