package repo

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

// HelmSourceLocation describes where a rendered Kubernetes resource came from
// inside a Helm chart tree.
type HelmSourceLocation struct {
	Chart          string   `json:"chart"`
	ChartPath      string   `json:"chartPath"`
	OwningSubchart string   `json:"owningSubchart"`
	TemplateFile   string   `json:"templateFile"`
	Release        string   `json:"release"`
	Namespace      string   `json:"namespace"`
	ValuesFiles    []string `json:"valuesFiles"`
	Pinpointed     bool     `json:"pinpointed"`
	Notes          []string `json:"notes,omitempty"`
}

var helmTemplateTimeout = 30 * time.Second

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

// IdentifyHelmSource locates where a Helm-managed resource's fix belongs
// within the chart tree at repoPath. It always returns a valid location —
// failures during helm templating degrade gracefully via Pinpointed=false
// and Notes rather than returning an error (the only hard error is Detect).
func IdentifyHelmSource(repoPath string, finding analyzer.Finding) (HelmSourceLocation, error) {
	mode, err := Detect(repoPath)
	if err != nil {
		return HelmSourceLocation{}, err
	}

	var loc HelmSourceLocation

	// Step 1: chart path and name.
	// Detect walks the full tree so mode.HelmChart may end up pointing at a
	// subchart's Chart.yaml. Prefer the one that lives directly under repoPath.
	rootChart := rootChartYAML(repoPath, mode.HelmChart)
	loc.ChartPath = filepath.Dir(rootChart)
	loc.Chart = chartName(rootChart)

	// Step 2: values enumeration — start from mode.ValuesFiles, then walk
	// charts/*/ for values*.yaml; de-dup with stable order.
	loc.ValuesFiles = enumerateValuesFiles(mode.ValuesFiles, loc.ChartPath)

	// Step 3: release and namespace.
	loc.Release = finding.GitOps.HelmRelease
	loc.Namespace = finding.Namespace
	if loc.Release == "" {
		loc.Notes = append(loc.Notes, "no Helm release label on the resource; confirm it is Helm-managed")
	}

	// Step 4: pinpoint via helm template.
	helmPath, lookErr := exec.LookPath("helm")
	if lookErr != nil {
		loc.Notes = append(loc.Notes, "helm not found; cannot pinpoint template")
		return loc, nil
	}

	// Pass the release name when known so rendered names match the live resource.
	args := []string{"template"}
	if loc.Release != "" {
		args = append(args, loc.Release)
	}
	args = append(args, loc.ChartPath)
	renderCtx, cancel := context.WithTimeout(context.Background(), helmTemplateTimeout)
	defer cancel()
	cmd := exec.CommandContext(renderCtx, helmPath, args...)
	out, renderErr := cmd.CombinedOutput()
	if renderCtx.Err() == context.DeadlineExceeded {
		loc.Notes = append(loc.Notes, "helm template timed out; cannot pinpoint template")
		return loc, nil
	}
	if renderErr != nil {
		loc.Notes = append(loc.Notes, "helm template failed: "+strings.TrimSpace(string(out)))
		return loc, nil
	}

	path, ok := helmSourceMatches(string(out), finding.ResourceKind, finding.ResourceName, loc.Release)
	if !ok {
		loc.Notes = append(loc.Notes, "no rendered resource matched "+finding.ResourceKind+"/"+finding.ResourceName)
		return loc, nil
	}

	loc.TemplateFile = path
	loc.Pinpointed = true

	// Derive OwningSubchart from a charts/<sub>/ segment in path.
	if sub := subchartFromPath(path); sub != "" {
		loc.OwningSubchart = sub
	}

	return loc, nil
}

// rootChartYAML returns the shallowest Chart.yaml under repoPath.
// Detect's WalkDir may set mode.HelmChart to a subchart's Chart.yaml (because
// charts/ sorts before templates/ alphabetically). If repoPath/Chart.yaml
// exists we use that directly; otherwise we return the Detect result as-is.
func rootChartYAML(repoPath, fallback string) string {
	candidate := filepath.Join(repoPath, "Chart.yaml")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return fallback
}

// chartName reads the "name:" field from a Chart.yaml file.
// Returns "" if the file cannot be read or the field is absent.
func chartName(chartYAML string) string {
	f, err := os.Open(chartYAML)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "name:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		}
	}
	return ""
}

// enumerateValuesFiles combines mode-detected values files with any
// values*.yaml found directly under charts/*/ subdirectories.
// The result is de-duplicated with stable order.
func enumerateValuesFiles(modeFiles []string, chartPath string) []string {
	seen := make(map[string]bool, len(modeFiles))
	result := make([]string, 0, len(modeFiles)+4)
	for _, f := range modeFiles {
		if !seen[f] {
			seen[f] = true
			result = append(result, f)
		}
	}
	// Walk charts/*/ (one level deep) for values*.yaml.
	chartsDir := filepath.Join(chartPath, "charts")
	entries, err := os.ReadDir(chartsDir)
	if err != nil {
		return result
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subDir := filepath.Join(chartsDir, e.Name())
		subEntries, err := os.ReadDir(subDir)
		if err != nil {
			continue
		}
		for _, se := range subEntries {
			if se.IsDir() {
				continue
			}
			base := se.Name()
			if strings.HasPrefix(base, "values") && strings.HasSuffix(base, ".yaml") {
				full := filepath.Join(subDir, base)
				if !seen[full] {
					seen[full] = true
					result = append(result, full)
				}
			}
		}
	}
	return result
}

// subchartFromPath extracts the subchart name from a template path that
// contains a charts/<sub>/ segment, e.g. "myapp/charts/redis/templates/ss.yaml"
// returns "redis". Returns "" if no such segment is found.
func subchartFromPath(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, p := range parts {
		if p == "charts" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
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
			if remainder == name || strings.HasSuffix(remainder, "-"+name) {
				return true
			}
		}
	}
	return false
}
