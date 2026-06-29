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
	Path       string              `json:"path"`
	Mode       string              `json:"mode"`
	Actions    []string            `json:"actions"`
	Warnings   []string            `json:"warnings,omitempty"`
	Preview    string              `json:"preview,omitempty"`
	HelmSource *HelmSourceLocation `json:"helmSource,omitempty"`
}

type PullRequest struct {
	URL      string   `json:"url,omitempty"`
	Branch   string   `json:"branch"`
	Base     string   `json:"base,omitempty"`
	Title    string   `json:"title"`
	Platform string   `json:"platform,omitempty"`
	Pushed   bool     `json:"pushed"`
	Opened   bool     `json:"opened"`
	Warnings []string `json:"warnings,omitempty"`
}

type ChangeSummary struct {
	Branch   string
	Files    []string
	Stat     string
	Diff     string
	Remote   string
	Provider string
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
	return prepareBranch(ctx, repoPath, branch, commit, message, nil)
}

func PrepareBranchFiles(ctx context.Context, repoPath, branch string, commit bool, message string, paths []string) error {
	if commit {
		if err := ensureNoUnrelatedChanges(ctx, repoPath, paths); err != nil {
			return err
		}
	}
	return prepareBranch(ctx, repoPath, branch, commit, message, paths)
}

func prepareBranch(ctx context.Context, repoPath, branch string, commit bool, message string, paths []string) error {
	if branch == "" && !commit {
		return nil
	}
	if branch != "" {
		exists := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
		exists.Dir = repoPath
		if err := exists.Run(); err == nil {
			return fmt.Errorf("branch %q already exists; choose a new remediation branch to avoid overwriting local work", branch)
		}
		cmd := exec.CommandContext(ctx, "git", "checkout", "-b", branch)
		cmd.Dir = repoPath
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git branch creation failed: %s", strings.TrimSpace(string(out)))
		}
	}
	if commit {
		addArgs := []string{"add", "."}
		if len(paths) > 0 {
			addArgs = []string{"add", "--"}
			for _, path := range paths {
				addArgs = append(addArgs, repoRelativePath(repoPath, path))
			}
		}
		add := exec.CommandContext(ctx, "git", addArgs...)
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

func repoRelativePath(repoPath, path string) string {
	if path == "" {
		return "."
	}
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return path
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(absRepo, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

func OpenPullRequest(ctx context.Context, repoPath, branch, base, title, body string, push bool) (PullRequest, error) {
	result := PullRequest{Branch: branch, Base: base, Title: firstNonEmpty(title, "fixora: verified remediation")}
	if branch == "" {
		out, err := gitOutput(ctx, repoPath, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return result, err
		}
		result.Branch = strings.TrimSpace(out)
	}
	if push {
		cmd := exec.CommandContext(ctx, "git", "push", "-u", "origin", result.Branch)
		cmd.Dir = repoPath
		if out, err := cmd.CombinedOutput(); err != nil {
			return result, fmt.Errorf("git push failed: %s", strings.TrimSpace(string(out)))
		}
		result.Pushed = true
	}
	if _, err := exec.LookPath("gh"); err == nil {
		return openGitHubPullRequest(ctx, repoPath, result, body)
	}
	if _, err := exec.LookPath("glab"); err == nil {
		return openGitLabMergeRequest(ctx, repoPath, result, body)
	}
	result.Warnings = append(result.Warnings, "gh/glab CLI not found; branch is ready but review request was not opened")
	return result, nil
}

func openGitHubPullRequest(ctx context.Context, repoPath string, result PullRequest, body string) (PullRequest, error) {
	args := []string{"pr", "create", "--title", result.Title, "--body", firstNonEmpty(body, "Verified by Fixora shadow clone.")}
	if result.Base != "" {
		args = append(args, "--base", result.Base)
	}
	if result.Branch != "" {
		args = append(args, "--head", result.Branch)
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return result, fmt.Errorf("gh pr create failed: %s", strings.TrimSpace(string(out)))
	}
	result.URL = strings.TrimSpace(string(out))
	result.Platform = "github"
	result.Opened = true
	return result, nil
}

func openGitLabMergeRequest(ctx context.Context, repoPath string, result PullRequest, body string) (PullRequest, error) {
	args := []string{"mr", "create", "--title", result.Title, "--description", firstNonEmpty(body, "Verified by Fixora shadow clone.")}
	if result.Base != "" {
		args = append(args, "--target-branch", result.Base)
	}
	if result.Branch != "" {
		args = append(args, "--source-branch", result.Branch)
	}
	cmd := exec.CommandContext(ctx, "glab", args...)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return result, fmt.Errorf("glab mr create failed: %s", strings.TrimSpace(string(out)))
	}
	result.URL = strings.TrimSpace(string(out))
	result.Platform = "gitlab"
	result.Opened = true
	return result, nil
}

func gitOutput(ctx context.Context, repoPath string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func SummarizeChange(ctx context.Context, repoPath, branch string, paths []string) ChangeSummary {
	s := ChangeSummary{Branch: branch, Files: make([]string, 0, len(paths)), Provider: detectProvider(ctx, repoPath)}
	for _, path := range paths {
		s.Files = append(s.Files, repoRelativePath(repoPath, path))
	}
	diffArgs := append([]string{"diff", "--stat", "--"}, s.Files...)
	if out, err := gitOutput(ctx, repoPath, diffArgs...); err == nil {
		s.Stat = strings.TrimSpace(out)
	}
	if out, err := gitOutput(ctx, repoPath, "remote", "get-url", "origin"); err == nil {
		s.Remote = strings.TrimSpace(out)
	}
	return s
}

func SummarizePreview(ctx context.Context, repoPath, branch string, patch SourcePatch) ChangeSummary {
	s := SummarizeChange(ctx, repoPath, branch, []string{patch.Path})
	s.Diff = patch.Preview
	if s.Stat == "" {
		s.Stat = strings.Join(patch.Actions, "; ")
	}
	return s
}

func ensureNoUnrelatedChanges(ctx context.Context, repoPath string, paths []string) error {
	out, err := gitOutput(ctx, repoPath, "status", "--porcelain")
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, path := range paths {
		allowed[repoRelativePath(repoPath, path)] = true
	}
	var unrelated []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r\n")
		if line == "" || len(line) < 4 {
			continue
		}
		file := strings.TrimSpace(line[3:])
		file = strings.Trim(file, "\"")
		if !allowed[file] {
			unrelated = append(unrelated, file)
		}
	}
	if len(unrelated) > 0 {
		return fmt.Errorf("unrelated uncommitted changes present: %s", strings.Join(unrelated, ", "))
	}
	return nil
}

func detectProvider(ctx context.Context, repoPath string) string {
	if _, err := exec.LookPath("gh"); err == nil {
		return "GitHub PR"
	}
	if _, err := exec.LookPath("glab"); err == nil {
		return "GitLab MR"
	}
	return "branch push only"
}

func WriteSourcePatch(repoPath, outFile string, finding analyzer.Finding, plan fix.Plan) (SourcePatch, error) {
	result, err := PreviewSourcePatch(repoPath, outFile, finding, plan)
	if err != nil {
		return SourcePatch{}, err
	}
	switch result.Mode {
	case "helm":
		return result, nil
	case "kustomize":
		patchData := plan.PatchYAML()
		if len(patchData) > 0 && !strings.HasSuffix(patchData, "\n") {
			patchData += "\n"
		}
		if err := os.WriteFile(result.Path, []byte(patchData), 0o600); err != nil {
			return result, err
		}
		mode, _ := Detect(repoPath)
		if mode.Kustomization != "" {
			if err := ensureKustomizePatch(mode.Kustomization, filepath.Base(result.Path)); err != nil {
				result.Warnings = append(result.Warnings, "could not update kustomization: "+err.Error())
			} else {
				result.Actions = append(result.Actions, "referenced patch from kustomization")
			}
		}
		return result, nil
	default:
		if strings.Contains(strings.Join(result.Actions, " "), "appended reviewed patch block") {
			return result, appendYAMLDocument(result.Path, plan.PatchYAML())
		}
		patchData := plan.PatchYAML()
		if len(patchData) > 0 && !strings.HasSuffix(patchData, "\n") {
			patchData += "\n"
		}
		return result, os.WriteFile(result.Path, []byte(patchData), 0o600)
	}
}

func PreviewSourcePatch(repoPath, outFile string, finding analyzer.Finding, plan fix.Plan) (SourcePatch, error) {
	mode, err := Detect(repoPath)
	if err != nil {
		return SourcePatch{}, err
	}
	result := SourcePatch{Mode: mode.Type, Preview: plan.PatchYAML()}
	switch mode.Type {
	case "helm":
		loc, _ := IdentifyHelmSource(repoPath, finding)
		result.HelmSource = &loc
		firstValues := ""
		if len(loc.ValuesFiles) > 0 {
			firstValues = loc.ValuesFiles[0]
		}
		target := firstNonEmpty(outFile, firstValues, filepath.Join(repoPath, "values.yaml"))
		if !filepath.IsAbs(target) {
			target = filepath.Join(repoPath, target)
		}
		result.Path = target
		result.Actions = append(result.Actions, "identified Helm source location for operator review")
		if loc.Pinpointed {
			owner := loc.OwningSubchart
			if owner == "" {
				owner = loc.Chart
			}
			result.Actions = append(result.Actions, "resource rendered by "+owner+"/"+loc.TemplateFile)
		}
		if len(loc.ValuesFiles) > 0 {
			result.Warnings = append(result.Warnings, "candidate values files: "+strings.Join(loc.ValuesFiles, ", "))
		}
		result.Warnings = append(result.Warnings, "translate the intended change into the chart's values schema")
		result.Warnings = append(result.Warnings, "verify with helm template and server dry-run before merge")
		for _, note := range loc.Notes {
			result.Warnings = append(result.Warnings, note)
		}
		return result, nil
	case "kustomize":
		target := firstNonEmpty(outFile, "fixora-patch.yaml")
		if !filepath.IsAbs(target) {
			target = filepath.Join(repoPath, target)
		}
		result.Path = target
		result.Actions = append(result.Actions, "wrote strategic-merge patch for Kustomize review")
		return result, nil
	default:
		if target := findRawManifest(mode.Files, finding); target != "" {
			result.Path = target
			result.Actions = append(result.Actions, "appended reviewed patch block beside matching raw manifest")
			result.Warnings = append(result.Warnings, "Raw manifest was not structurally edited; merge the reviewed patch block into the resource spec.")
			return result, nil
		}
		target := firstNonEmpty(outFile, "fixora-patch.yaml")
		if !filepath.IsAbs(target) {
			target = filepath.Join(repoPath, target)
		}
		result.Path = target
		result.Actions = append(result.Actions, "wrote standalone raw manifest patch")
		return result, nil
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
