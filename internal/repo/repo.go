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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
