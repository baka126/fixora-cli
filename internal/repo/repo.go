package repo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

type Mode struct {
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	Files          []string `json:"files"`
	HelmChart      string   `json:"helmChart,omitempty"`
	ValuesFiles    []string `json:"valuesFiles,omitempty"`
	Kustomization  string   `json:"kustomization,omitempty"`
	ValidationNote string   `json:"validationNote,omitempty"`
}

type SourcePatch struct {
	Path     string   `json:"path"`
	Mode     string   `json:"mode"`
	Actions  []string `json:"actions"`
	Warnings []string `json:"warnings,omitempty"`
}

func Detect(path string) (Mode, error) {
	if path == "" {
		path = "."
	}
	mode := Mode{Path: path, Type: "raw"}
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base := filepath.Base(p)
		if base == "Chart.yaml" {
			mode.Type = "helm"
			mode.HelmChart = p
		}
		if strings.HasPrefix(base, "values") && strings.HasSuffix(base, ".yaml") {
			mode.ValuesFiles = append(mode.ValuesFiles, p)
		}
		if base == "kustomization.yaml" || base == "kustomization.yml" {
			if mode.Type != "helm" {
				mode.Type = "kustomize"
			}
			mode.Kustomization = p
		}
		if strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".json") {
			mode.Files = append(mode.Files, p)
		}
		return nil
	})
	return mode, err
}

func Plan(ctx context.Context, repoPath string, finding analyzer.Finding, plan fix.Plan) (Mode, error) {
	mode, err := Detect(repoPath)
	if err != nil {
		return mode, err
	}
	switch mode.Type {
	case "helm":
		mode.ValidationNote = "Patch Helm values, then run helm template before kubectl dry-run validation."
	case "kustomize":
		mode.ValidationNote = "Generate a strategic merge or JSON6902 patch and reference it from kustomization.yaml."
	default:
		mode.ValidationNote = "Patch the matching raw manifest with kind/name/namespace identity checks."
	}
	_ = ctx
	_ = finding
	_ = plan
	return mode, nil
}

func Validate(ctx context.Context, mode Mode) error {
	switch mode.Type {
	case "helm":
		if _, err := exec.LookPath("helm"); err != nil {
			return fmt.Errorf("helm not found; cannot render chart")
		}
		cmd := exec.CommandContext(ctx, "helm", "template", mode.Path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("helm template failed: %s", strings.TrimSpace(string(out)))
		}
	case "kustomize":
		if _, err := exec.LookPath("kustomize"); err == nil {
			cmd := exec.CommandContext(ctx, "kustomize", "build", mode.Path)
			if out, runErr := cmd.CombinedOutput(); runErr != nil {
				return fmt.Errorf("kustomize build failed: %s", strings.TrimSpace(string(out)))
			}
			return nil
		}
		if _, err := exec.LookPath("kubectl"); err != nil {
			return fmt.Errorf("kustomize not found and kubectl fallback unavailable")
		}
		cmd := exec.CommandContext(ctx, "kubectl", "kustomize", mode.Path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("kubectl kustomize failed: %s", strings.TrimSpace(string(out)))
		}
	default:
		if len(mode.Files) == 0 {
			return fmt.Errorf("no manifest files found")
		}
		if _, err := exec.LookPath("kubectl"); err == nil {
			cmd := exec.CommandContext(ctx, "kubectl", "apply", "--dry-run=server", "-f", mode.Path)
			if out, runErr := cmd.CombinedOutput(); runErr != nil {
				return fmt.Errorf("kubectl server dry-run failed: %s", strings.TrimSpace(string(out)))
			}
		}
	}
	return nil
}

func PrepareBranch(ctx context.Context, repoPath, branch string, commit bool, message string) error {
	if branch == "" && !commit {
		return nil
	}
	if branch != "" {
		cmd := exec.CommandContext(ctx, "git", "checkout", "-B", branch)
		cmd.Dir = repoPath
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git checkout failed: %s", strings.TrimSpace(string(out)))
		}
	}
	if commit {
		add := exec.CommandContext(ctx, "git", "add", ".")
		add.Dir = repoPath
		if out, err := add.CombinedOutput(); err != nil {
			return fmt.Errorf("git add failed: %s", strings.TrimSpace(string(out)))
		}
		c := exec.CommandContext(ctx, "git", "commit", "-m", firstNonEmpty(message, "fixora: add remediation patch"))
		c.Dir = repoPath
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("git commit failed: %s", strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func WriteSourcePatch(repoPath, outFile string, finding analyzer.Finding, plan fix.Plan) (SourcePatch, error) {
	mode, err := Detect(repoPath)
	if err != nil {
		return SourcePatch{}, err
	}
	result := SourcePatch{Mode: mode.Type}
	switch mode.Type {
	case "helm":
		target := firstNonEmpty(outFile, firstValuesFile(mode), filepath.Join(repoPath, "values.yaml"))
		if !filepath.IsAbs(target) {
			target = filepath.Join(repoPath, target)
		}
		result.Path = target
		result.Actions = append(result.Actions, "appended fixoraSuggestedPatch to Helm values for operator review")
		result.Warnings = append(result.Warnings, "Helm values schemas vary; review and translate fixoraSuggestedPatch into chart-native keys before merge.")
		return result, appendYAMLBlock(target, "fixoraSuggestedPatch", plan.PatchYAML())
	case "kustomize":
		target := firstNonEmpty(outFile, "fixora-patch.yaml")
		if !filepath.IsAbs(target) {
			target = filepath.Join(repoPath, target)
		}
		result.Path = target
		result.Actions = append(result.Actions, "wrote strategic-merge patch for Kustomize review")
		if err := os.WriteFile(target, []byte(plan.PatchYAML()), 0o600); err != nil {
			return result, err
		}
		if mode.Kustomization != "" {
			if err := ensureKustomizePatch(mode.Kustomization, filepath.Base(target)); err != nil {
				result.Warnings = append(result.Warnings, "could not update kustomization: "+err.Error())
			} else {
				result.Actions = append(result.Actions, "referenced patch from kustomization")
			}
		}
		return result, nil
	default:
		if target := findRawManifest(mode.Files, finding); target != "" {
			result.Path = target
			result.Actions = append(result.Actions, "appended reviewed patch block beside matching raw manifest")
			result.Warnings = append(result.Warnings, "Raw manifest was not structurally edited; merge the reviewed patch block into the resource spec.")
			return result, appendYAMLDocument(target, plan.PatchYAML())
		}
		target := firstNonEmpty(outFile, "fixora-patch.yaml")
		if !filepath.IsAbs(target) {
			target = filepath.Join(repoPath, target)
		}
		result.Path = target
		result.Actions = append(result.Actions, "wrote standalone raw manifest patch")
		return result, os.WriteFile(target, []byte(plan.PatchYAML()), 0o600)
	}
}

func firstValuesFile(mode Mode) string {
	if len(mode.ValuesFiles) > 0 {
		return mode.ValuesFiles[0]
	}
	return ""
}

func appendYAMLDocument(path, patch string) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.Write(existing)
	if !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n---\n")
	b.WriteString(patch)
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func appendYAMLBlock(path, key, patch string) error {
	existing, _ := os.ReadFile(path)
	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(key)
	b.WriteString(": |\n")
	for _, line := range strings.Split(strings.TrimRight(patch, "\n"), "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func ensureKustomizePatch(kustomization, patchFile string) error {
	data, err := os.ReadFile(kustomization)
	if err != nil {
		return err
	}
	text := string(data)
	if strings.Contains(text, patchFile) {
		return nil
	}
	var b strings.Builder
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteString("\n")
	}
	if strings.Contains(text, "patchesStrategicMerge:") {
		b.WriteString("- ")
		b.WriteString(patchFile)
		b.WriteString("\n")
	} else {
		b.WriteString("patchesStrategicMerge:\n- ")
		b.WriteString(patchFile)
		b.WriteString("\n")
	}
	return os.WriteFile(kustomization, []byte(b.String()), 0o600)
}

func findRawManifest(files []string, finding analyzer.Finding) string {
	kindNeedle := "kind: " + normalizeKind(finding.ResourceKind)
	nameNeedle := "name: " + finding.ResourceName
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		text := string(data)
		if strings.Contains(text, kindNeedle) && strings.Contains(text, nameNeedle) {
			return file
		}
	}
	return ""
}

func normalizeKind(kind string) string {
	switch strings.ToLower(kind) {
	case "deploy", "deployment", "deployments":
		return "Deployment"
	case "statefulset", "statefulsets":
		return "StatefulSet"
	case "daemonset", "daemonsets":
		return "DaemonSet"
	case "pod", "pods":
		return "Pod"
	default:
		return kind
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
