package repo

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

// FieldVerdict classifies one leaf field of an intended patch against the
// chart's rendered output. Class is one of "managed-divergent",
// "managed-match", or "unmanaged". RenderedValue/IntendedValue are empty for
// Secret-kind findings (values are never emitted).
type FieldVerdict struct {
	Path          string
	Class         string
	RenderedValue string
	IntendedValue string
}

// RenderValidation is the result of comparing an intended patch to the chart's
// rendered output. Failures degrade into Notes rather than errors.
type RenderValidation struct {
	Fields []FieldVerdict
	Notes  []string
}

// renderedDocFor returns the YAML body of the first rendered document whose
// kind and name match. It reuses the "# Source:" document-boundary and
// name-matching rules of helmSourceMatches, but captures the document text.
// Bare "---"/"..." separator lines are dropped so the body parses as a single
// YAML document.
func renderedDocFor(renderedOutput, kind, name, release string) (docText string, ok bool) {
	const sourcePrefix = "# Source: "
	lines := strings.Split(renderedOutput, "\n")

	var (
		haveSource bool
		curKind    string
		curName    string
		inMetadata bool
		body       strings.Builder
	)

	flush := func() (string, bool) {
		if !haveSource {
			return "", false
		}
		if strings.EqualFold(curKind, kind) && nameMatches(curName, name, release) {
			return body.String(), true
		}
		return "", false
	}

	for _, line := range lines {
		if strings.HasPrefix(line, sourcePrefix) {
			if s, matched := flush(); matched {
				return s, true
			}
			haveSource = true
			curKind = ""
			curName = ""
			inMetadata = false
			body.Reset()
			continue
		}
		if !haveSource {
			continue
		}
		if t := strings.TrimSpace(line); t == "---" || t == "..." {
			continue
		}
		body.WriteString(line)
		body.WriteByte('\n')

		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '-' {
			key := strings.TrimSuffix(strings.TrimSpace(line), ":")
			inMetadata = key == "metadata"
			if strings.HasPrefix(line, "kind:") {
				curKind = strings.TrimSpace(strings.TrimPrefix(line, "kind:"))
			}
			continue
		}
		if inMetadata && strings.HasPrefix(line, "  name:") {
			curName = strings.TrimSpace(strings.TrimPrefix(line, "  name:"))
		}
	}
	if s, matched := flush(); matched {
		return s, true
	}
	return "", false
}

// classifyPatch walks every leaf field in patch and classifies it against the
// rendered document. Nested maps are descended (dotted paths); scalars and
// lists are leaves (lists compared wholesale). Absent in rendered => unmanaged;
// present and equal => managed-match; present and different => managed-divergent.
func classifyPatch(patch, rendered map[string]any, kind string) []FieldVerdict {
	var verdicts []FieldVerdict
	var walk func(prefix string, p map[string]any, r any)
	walk = func(prefix string, p map[string]any, r any) {
		rMap, _ := r.(map[string]any)
		for k, pv := range p {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			var rv any
			var rHas bool
			if rMap != nil {
				rv, rHas = rMap[k]
			}
			if pvMap, ok := pv.(map[string]any); ok {
				walk(path, pvMap, rv)
				continue
			}
			verdicts = append(verdicts, classifyLeaf(path, pv, rv, rHas, kind))
		}
	}
	walk("", patch, rendered)
	return verdicts
}

func classifyLeaf(path string, pv, rv any, rHas bool, kind string) FieldVerdict {
	v := FieldVerdict{Path: path}
	switch {
	case !rHas:
		v.Class = "unmanaged"
	case normalize(pv) == normalize(rv):
		v.Class = "managed-match"
	default:
		v.Class = "managed-divergent"
	}
	if kind != "Secret" {
		v.IntendedValue = normalize(pv)
		if rHas {
			v.RenderedValue = normalize(rv)
		}
	}
	return v
}

// normalize renders a scalar or simple list into a stable, type-forgiving
// string so that e.g. the string "8080" and the int 8080 compare equal.
func normalize(v any) string {
	switch t := v.(type) {
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = normalize(e)
		}
		return "[" + strings.Join(parts, ",") + "]"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ValidateAgainstRender renders the chart at loc.ChartPath and classifies each
// leaf field of patchYAML against the rendered document for finding's
// kind/name. It never returns an error: every failure path degrades into Notes.
// The chart is rendered with its default values (not the live release values),
// so value comparisons are best-effort — that caveat is recorded in Notes
// whenever a value comparison was actually made.
func ValidateAgainstRender(loc HelmSourceLocation, finding analyzer.Finding, patchYAML string) RenderValidation {
	var rv RenderValidation

	if strings.TrimSpace(patchYAML) == "" {
		rv.Notes = append(rv.Notes, "no intended patch to validate against the rendered chart")
		return rv
	}
	if !loc.Pinpointed {
		rv.Notes = append(rv.Notes, "chart source not pinpointed; cannot render-validate")
		return rv
	}

	if strings.Contains(patchYAML, "\n---") {
		rv.Notes = append(rv.Notes, "intended patch is not a single YAML map; skipped render validation")
		return rv
	}

	var patch map[string]any
	if err := yaml.Unmarshal([]byte(patchYAML), &patch); err != nil || patch == nil {
		rv.Notes = append(rv.Notes, "intended patch is not a single YAML map; skipped render validation")
		return rv
	}

	rendered, err := renderChart(loc.ChartPath, loc.Release)
	if err != nil {
		rv.Notes = append(rv.Notes, "cannot render-validate: "+err.Error())
		return rv
	}

	docText, ok := renderedDocFor(rendered, finding.ResourceKind, finding.ResourceName, loc.Release)
	if !ok {
		rv.Notes = append(rv.Notes, "no rendered document matched "+finding.ResourceKind+"/"+finding.ResourceName)
		return rv
	}

	var doc map[string]any
	if err := yaml.Unmarshal([]byte(docText), &doc); err != nil {
		rv.Notes = append(rv.Notes, "rendered document did not parse as YAML; skipped render validation")
		return rv
	}

	rv.Fields = classifyPatch(patch, doc, finding.ResourceKind)
	for _, f := range rv.Fields {
		if f.Class != "unmanaged" {
			rv.Notes = append(rv.Notes, "rendered with the chart's default values, not the live release values; value comparisons are best-effort")
			break
		}
	}
	return rv
}
