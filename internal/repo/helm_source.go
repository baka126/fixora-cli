package repo

import "strings"

// HelmSourceLocation describes where a rendered Kubernetes resource came from
// inside a Helm chart tree.
type HelmSourceLocation struct {
	Chart          string
	ChartPath      string
	OwningSubchart string
	TemplateFile   string
	Release        string
	Namespace      string
	ValuesFiles    []string
	Pinpointed     bool
	Notes          []string
}

// helmSourceMatches scans helm template output (renderedOutput) and returns
// the "# Source:" path of the first document whose kind and name match.
//
// Name matching rules (any one sufficient):
//  1. rendered name == name (exact)
//  2. rendered name == release+"-"+name
//  3. release != "" && rendered name has prefix release+"-" and suffix == name
//
// Kind comparison is case-insensitive. Pure stdlib; no I/O.
func helmSourceMatches(renderedOutput, kind, name, release string) (sourcePath string, ok bool) {
	const sourcePrefix = "# Source: "

	// Split into lines and collect per-document segments.
	lines := strings.Split(renderedOutput, "\n")

	var (
		currentSource string
		currentKind   string
		currentName   string
		inMetadata    bool
	)

	flush := func() (string, bool) {
		if currentSource == "" {
			return "", false
		}
		if !strings.EqualFold(currentKind, kind) {
			return "", false
		}
		if nameMatches(currentName, name, release) {
			return currentSource, true
		}
		return "", false
	}

	for _, line := range lines {
		if strings.HasPrefix(line, sourcePrefix) {
			// New document boundary — check the previous segment.
			if s, matched := flush(); matched {
				return s, true
			}
			currentSource = strings.TrimPrefix(line, sourcePrefix)
			currentKind = ""
			currentName = ""
			inMetadata = false
			continue
		}

		if currentSource == "" {
			continue
		}

		// Top-level key detection (no leading spaces).
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '-' {
			key := strings.TrimSuffix(strings.TrimSpace(line), ":")
			if key == "metadata" {
				inMetadata = true
			} else {
				inMetadata = false
			}
			// Capture top-level kind.
			if strings.HasPrefix(line, "kind:") {
				currentKind = strings.TrimSpace(strings.TrimPrefix(line, "kind:"))
			}
			continue
		}

		// Inside metadata block — look for "  name: <value>".
		if inMetadata && strings.HasPrefix(line, "  name:") {
			currentName = strings.TrimSpace(strings.TrimPrefix(line, "  name:"))
		}
	}

	// Check final segment.
	if s, matched := flush(); matched {
		return s, true
	}

	return "", false
}

// nameMatches applies the three-way matching rule.
func nameMatches(rendered, name, release string) bool {
	if rendered == name {
		return true
	}
	if release != "" {
		prefixed := release + "-" + name
		if rendered == prefixed {
			return true
		}
		// rendered has release- prefix and name as the remainder after it.
		if strings.HasPrefix(rendered, release+"-") {
			remainder := strings.TrimPrefix(rendered, release+"-")
			if remainder == name {
				return true
			}
		}
	}
	return false
}
