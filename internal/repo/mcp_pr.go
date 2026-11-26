package repo

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// MCPPRPreview is a read-only summary of the checked-out branch that the MCP
// create-pr tool is prepared to publish. Remote URLs and push URLs are kept as
// normalized identities, never as raw strings that might contain credentials.
type MCPPRPreview struct {
	Repository string   `json:"-"`
	Branch     string   `json:"branch"`
	Base       string   `json:"base"`
	BaseCommit string   `json:"baseCommit"`
	HeadCommit string   `json:"headCommit"`
	Ahead      int      `json:"commitsAhead"`
	Commits    []string `json:"commits,omitempty"`
	Remote     string   `json:"remote"`
	Provider   string   `json:"provider"`
	Title      string   `json:"title"`
	remoteID   string
	pushID     string
	remoteURL  string
	pushURL    string
	remoteHost string
	repoPath   string
}

type prRemote struct {
	provider string
	host     string
	path     string
	identity string
	display  string
	rawURL   string
}

// ValidateMCPPRRepo resolves repoPath to a local Git worktree root. It does
// not allow an empty path to silently select the process working directory.
func ValidateMCPPRRepo(ctx context.Context, repoPath string) (string, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return "", fmt.Errorf("MCP PR repository path is required")
	}
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("resolve MCP PR repository path: %w", err)
	}
	absPath, err = filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("inspect MCP PR repository path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("inspect MCP PR repository path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("MCP PR repository path must be a directory")
	}
	root, err := gitReadOnlyOutput(ctx, absPath, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("MCP PR path is not a Git worktree")
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("Git returned an empty MCP PR repository root")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve Git worktree root: %w", err)
	}
	return filepath.Clean(root), nil
}

// PreviewMCPPR performs local-only checks for an existing branch. It does not
// fetch, push, stage, commit, or write repository files.
func PreviewMCPPR(ctx context.Context, repoPath, base, title string) (MCPPRPreview, error) {
	root, err := ValidateMCPPRRepo(ctx, repoPath)
	if err != nil {
		return MCPPRPreview{}, err
	}
	configuredRoot, err := filepath.Abs(repoPath)
	if err != nil {
		return MCPPRPreview{}, fmt.Errorf("resolve configured repository path")
	}
	configuredRoot, err = filepath.EvalSymlinks(configuredRoot)
	if err != nil || filepath.Clean(configuredRoot) != root {
		return MCPPRPreview{}, fmt.Errorf("configured repository root changed; restart MCP with the intended repository")
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return MCPPRPreview{}, fmt.Errorf("base branch is required")
	}
	if _, err := gitReadOnlyOutput(ctx, root, "check-ref-format", "--branch", base); err != nil {
		return MCPPRPreview{}, fmt.Errorf("base must be a valid local branch name")
	}
	status, err := gitReadOnlyOutput(ctx, root, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return MCPPRPreview{}, fmt.Errorf("inspect repository status")
	}
	if strings.TrimSpace(status) != "" {
		return MCPPRPreview{}, fmt.Errorf("repository has uncommitted or untracked files; commit or remove them before opening a PR")
	}
	branch, err := gitReadOnlyOutput(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) == "" {
		return MCPPRPreview{}, fmt.Errorf("repository is in detached HEAD; check out the prepared repair branch first")
	}
	branch = strings.TrimSpace(branch)
	if _, err := gitReadOnlyOutput(ctx, root, "check-ref-format", "--branch", branch); err != nil {
		return MCPPRPreview{}, fmt.Errorf("current branch name is invalid")
	}
	if branch == base {
		return MCPPRPreview{}, fmt.Errorf("current branch is the base branch; check out the prepared repair branch first")
	}
	remote, pushRemote, err := inspectPRRemote(ctx, root)
	if err != nil {
		return MCPPRPreview{}, err
	}
	baseRef := "refs/remotes/origin/" + base
	if _, err := gitReadOnlyOutput(ctx, root, "rev-parse", "--verify", baseRef+"^{commit}"); err != nil {
		baseRef = "refs/heads/" + base
		if _, err := gitReadOnlyOutput(ctx, root, "rev-parse", "--verify", baseRef+"^{commit}"); err != nil {
			return MCPPRPreview{}, fmt.Errorf("base branch %q is not available locally or as origin/%s; fetch it before previewing", base, base)
		}
	}
	if _, err := gitReadOnlyOutput(ctx, root, "merge-base", baseRef, "HEAD"); err != nil {
		return MCPPRPreview{}, fmt.Errorf("current branch and base branch have no common history")
	}
	baseCommit, err := gitReadOnlyOutput(ctx, root, "rev-parse", "--verify", baseRef+"^{commit}")
	if err != nil {
		return MCPPRPreview{}, fmt.Errorf("read resolved base commit")
	}
	baseCommit = strings.TrimSpace(baseCommit)
	head, err := gitReadOnlyOutput(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return MCPPRPreview{}, fmt.Errorf("read current branch commit")
	}
	head = strings.TrimSpace(head)
	if !validCommitID(baseCommit) || !validCommitID(head) {
		return MCPPRPreview{}, fmt.Errorf("Git returned an invalid commit identity")
	}
	aheadText, err := gitReadOnlyOutput(ctx, root, "rev-list", "--count", baseCommit+".."+head)
	if err != nil {
		return MCPPRPreview{}, fmt.Errorf("compare branch with base")
	}
	ahead, err := strconv.Atoi(strings.TrimSpace(aheadText))
	if err != nil || ahead < 1 {
		return MCPPRPreview{}, fmt.Errorf("current branch has no commits ahead of base %q", base)
	}
	commitsText, err := gitReadOnlyOutput(ctx, root, "log", "--format=%s", "--max-count=10", baseCommit+".."+head)
	if err != nil {
		return MCPPRPreview{}, fmt.Errorf("summarize branch commits")
	}
	commits := make([]string, 0, 10)
	for _, line := range strings.Split(strings.TrimSpace(commitsText), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			commits = append(commits, line)
		}
	}
	if title = strings.TrimSpace(title); title == "" {
		title = "fixora: review prepared branch " + branch
	}
	return MCPPRPreview{
		Repository: root,
		Branch:     branch,
		Base:       base,
		BaseCommit: baseCommit,
		HeadCommit: head,
		Ahead:      ahead,
		Commits:    commits,
		Remote:     remote.display,
		Provider:   remote.provider,
		Title:      title,
		remoteID:   remote.identity,
		pushID:     pushRemote.identity,
		remoteURL:  remote.rawURL,
		pushURL:    pushRemote.rawURL,
		remoteHost: remote.host,
		repoPath:   remote.path,
	}, nil
}

// CreateMCPPR rechecks the exact preview, verifies the remote base is still the
// approved commit, pushes that immutable commit ID, and creates the request
// against the explicit repository via the provider API. It never stages or
// commits files and never asks a provider CLI to push a mutable local branch.
func CreateMCPPR(ctx context.Context, repoPath string, preview MCPPRPreview, body string) (PullRequest, error) {
	current, err := PreviewMCPPR(ctx, repoPath, preview.Base, preview.Title)
	if err != nil {
		return PullRequest{Branch: preview.Branch, Base: preview.Base, Title: preview.Title}, err
	}
	if current.Repository != preview.Repository || current.Branch != preview.Branch || current.Base != preview.Base || current.BaseCommit != preview.BaseCommit ||
		current.HeadCommit != preview.HeadCommit || current.Ahead != preview.Ahead || current.remoteID != preview.remoteID || current.pushID != preview.pushID ||
		current.remoteURL != preview.remoteURL || current.pushURL != preview.pushURL ||
		current.Provider != preview.Provider || current.remoteHost != preview.remoteHost || current.repoPath != preview.repoPath {
		return PullRequest{Branch: current.Branch, Base: current.Base, Title: current.Title}, fmt.Errorf("repository, branch, base, or remote changed after preview; run create-pr again to review the new state")
	}
	remoteBase, err := gitReadOnlyOutputWithEnv(ctx, current.Repository, gitConfigOverrideEnv(os.Environ(), "remote.origin.url", current.remoteURL), "ls-remote", "--refs", "origin", "refs/heads/"+current.Base)
	if err != nil {
		return PullRequest{Branch: current.Branch, Base: current.Base, Title: current.Title}, fmt.Errorf("could not verify the current remote base; no branch was pushed")
	}
	fields := strings.Fields(remoteBase)
	if len(fields) < 2 || !validCommitID(strings.TrimSpace(fields[0])) || strings.TrimSpace(fields[1]) != "refs/heads/"+current.Base || strings.TrimSpace(fields[0]) != current.BaseCommit {
		return PullRequest{Branch: current.Branch, Base: current.Base, Title: current.Title}, fmt.Errorf("origin/%s changed since preview; fetch and review a new preview before confirming", current.Base)
	}
	if current.Provider == "github" {
		if _, err := exec.LookPath("gh"); err != nil {
			return PullRequest{Branch: current.Branch, Base: current.Base, Title: current.Title}, fmt.Errorf("gh is required for this GitHub remote; no branch was pushed")
		}
	} else if current.Provider == "gitlab" {
		if _, err := exec.LookPath("glab"); err != nil {
			return PullRequest{Branch: current.Branch, Base: current.Base, Title: current.Title}, fmt.Errorf("glab is required for this GitLab remote; no branch was pushed")
		}
	} else {
		return PullRequest{Branch: current.Branch, Base: current.Base, Title: current.Title}, fmt.Errorf("remote provider is unsupported; no branch was pushed")
	}
	pushSpec := current.HeadCommit + ":refs/heads/" + current.Branch
	push := exec.CommandContext(ctx, "git", "push", "--porcelain", "origin", pushSpec)
	push.Dir = current.Repository
	push.Stdout = io.Discard
	push.Stderr = io.Discard
	push.Env = gitConfigOverrideEnv(os.Environ(), "remote.origin.pushurl", current.pushURL)
	if err := push.Run(); err != nil {
		return PullRequest{Branch: current.Branch, Base: current.Base, Title: current.Title}, fmt.Errorf("branch push failed; no review request was opened; check the configured remote and credentials")
	}
	result := PullRequest{Branch: current.Branch, Base: current.Base, Title: current.Title, Pushed: true}
	if current.Provider == "github" {
		return openGitHubPullRequest(ctx, current.Repository, result, body, current.remoteHost, current.repoPath)
	}
	return openGitLabMergeRequest(ctx, current.Repository, result, body, current.remoteHost, current.repoPath)
}

func inspectPRRemote(ctx context.Context, root string) (prRemote, prRemote, error) {
	fetchURL, err := gitReadOnlyOutput(ctx, root, "remote", "get-url", "origin")
	if err != nil {
		return prRemote{}, prRemote{}, fmt.Errorf("repository has no configured origin remote")
	}
	fetchURL = strings.TrimSpace(fetchURL)
	remote, err := parsePRRemote(fetchURL)
	if err != nil {
		return prRemote{}, prRemote{}, fmt.Errorf("origin remote is not a recognized GitHub or GitLab repository")
	}
	pushURLs, err := gitReadOnlyOutput(ctx, root, "remote", "get-url", "--push", "--all", "origin")
	if err != nil {
		return prRemote{}, prRemote{}, fmt.Errorf("could not inspect origin push destination")
	}
	lines := strings.Split(strings.TrimSpace(pushURLs), "\n")
	if len(lines) != 1 || strings.TrimSpace(lines[0]) == "" {
		return prRemote{}, prRemote{}, fmt.Errorf("origin must have exactly one push destination")
	}
	pushRemote, err := parsePRRemote(strings.TrimSpace(lines[0]))
	if err != nil || pushRemote.identity != remote.identity {
		return prRemote{}, prRemote{}, fmt.Errorf("origin push destination does not match the reviewed repository")
	}
	return remote, pushRemote, nil
}

func parsePRRemote(raw string) (prRemote, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "?#\n\r") {
		return prRemote{}, fmt.Errorf("invalid remote")
	}
	host, repositoryPath := "", ""
	parsed, parseErr := url.Parse(raw)
	if parseErr == nil && parsed.Hostname() != "" {
		host = strings.ToLower(parsed.Hostname())
		repositoryPath = strings.Trim(parsed.EscapedPath(), "/")
		decoded, err := url.PathUnescape(repositoryPath)
		if err != nil {
			return prRemote{}, fmt.Errorf("invalid remote path")
		}
		repositoryPath = decoded
	} else if at := strings.Index(raw, "@"); at >= 0 {
		colon := strings.Index(raw[at+1:], ":")
		if colon < 0 {
			return prRemote{}, fmt.Errorf("invalid scp remote")
		}
		host = strings.ToLower(raw[at+1 : at+1+colon])
		repositoryPath = strings.Trim(raw[at+2+colon:], "/")
	} else {
		return prRemote{}, fmt.Errorf("unsupported remote syntax")
	}
	repositoryPath = strings.TrimSuffix(repositoryPath, ".git")
	parts := strings.Split(repositoryPath, "/")
	if host == "" || len(parts) < 2 {
		return prRemote{}, fmt.Errorf("invalid repository identity")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return prRemote{}, fmt.Errorf("invalid repository path")
		}
	}
	provider := ""
	if host == "github.com" || strings.HasPrefix(host, "github.") || strings.Contains(host, ".github.") {
		if len(parts) != 2 {
			return prRemote{}, fmt.Errorf("GitHub remote must be owner/repository")
		}
		provider = "github"
	} else if host == "gitlab.com" || strings.HasPrefix(host, "gitlab.") || strings.Contains(host, ".gitlab.") {
		provider = "gitlab"
	} else {
		return prRemote{}, fmt.Errorf("unsupported provider host")
	}
	identity := host + "/" + repositoryPath
	return prRemote{provider: provider, host: host, path: repositoryPath, identity: identity, display: identity, rawURL: raw}, nil
}

func gitReadOnlyOutput(ctx context.Context, repoPath string, args ...string) (string, error) {
	return gitReadOnlyOutputWithEnv(ctx, repoPath, nil, args...)
}

func gitReadOnlyOutputWithEnv(ctx context.Context, repoPath string, environment []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	if environment == nil {
		environment = os.Environ()
	}
	cmd.Env = withoutEnv(environment, "GIT_OPTIONAL_LOCKS")
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func withoutEnv(env []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env))
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func gitConfigOverrideEnv(env []string, key, value string) []string {
	filtered := make([]string, 0, len(env)+3)
	for _, item := range env {
		if strings.HasPrefix(item, "GIT_CONFIG_COUNT=") || strings.HasPrefix(item, "GIT_CONFIG_KEY_") || strings.HasPrefix(item, "GIT_CONFIG_VALUE_") {
			continue
		}
		filtered = append(filtered, item)
	}
	return append(filtered, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0="+key, "GIT_CONFIG_VALUE_0="+value)
}

func validCommitID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}
