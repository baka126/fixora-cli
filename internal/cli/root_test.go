package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
)

func TestLintAcceptsFilenameFlag(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	dir := t.TempDir()
	manifest := filepath.Join(dir, "deployment.yaml")
	err := os.WriteFile(manifest, []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
      - name: api
        image: ghcr.io/acme/api:latest
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"lint", "-f", manifest}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "latest tag") {
		t.Fatalf("expected lint output to mention latest tag, got %s", stdout.String())
	}
}

func TestParseProductionBounds(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	opts, rest, err := parseFlags([]string{"--timeout", "30s", "--log-tail", "20", "--max-logs-bytes", "4096", "-f", "app.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no rest args, got %#v", rest)
	}
	if opts.timeout != 30*time.Second {
		t.Fatalf("expected timeout 30s, got %s", opts.timeout)
	}
	if opts.logTail != 20 {
		t.Fatalf("expected log tail 20, got %d", opts.logTail)
	}
	if opts.maxLogBytes != 4096 {
		t.Fatalf("expected max log bytes 4096, got %d", opts.maxLogBytes)
	}
	if len(opts.lintFiles) != 1 || opts.lintFiles[0] != "app.yaml" {
		t.Fatalf("expected lint file app.yaml, got %#v", opts.lintFiles)
	}
}

func TestHelpIsIncidentFocusedByDefault(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("help failed: code=%d stderr=%s", code, stderr.String())
	}
	help := stdout.String()
	for _, want := range []string{"scan", "why <kind/name>", "fix <kind/name>", "debug <tool>", "source <tool>"} {
		if !strings.Contains(help, want) {
			t.Fatalf("focused help missing %q:\n%s", want, help)
		}
	}
	for _, hidden := range []string{"custom-analyzers", "serve --mcp", "cache add|get|remove"} {
		if strings.Contains(help, hidden) {
			t.Fatalf("focused help should hide advanced command %q:\n%s", hidden, help)
		}
	}
}

func TestAdvancedHelpShowsFullReference(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"help", "--advanced"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("advanced help failed: code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"custom-analyzers", "serve --mcp", "patch <kind/name>", "--shadow-timeout"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("advanced help missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestSimplifiedCommandAliases(t *testing.T) {
	tests := []struct {
		cmd     string
		rest    []string
		wantCmd string
		wantArg string
	}{
		{cmd: "scan", wantCmd: "incidents"},
		{cmd: "rca", rest: []string{"deployment/api"}, wantCmd: "why", wantArg: "deployment/api"},
		{cmd: "repair", rest: []string{"deployment/api"}, wantCmd: "fix", wantArg: "deployment/api"},
		{cmd: "debug", rest: []string{"trace", "service/api"}, wantCmd: "trace", wantArg: "service/api"},
		{cmd: "source", rest: []string{"validate", "./charts/api"}, wantCmd: "validate", wantArg: "./charts/api"},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			gotCmd, gotRest, err := normalizeCommand(tt.cmd, tt.rest)
			if err != nil {
				t.Fatal(err)
			}
			if gotCmd != tt.wantCmd {
				t.Fatalf("cmd=%q want %q", gotCmd, tt.wantCmd)
			}
			if tt.wantArg != "" && (len(gotRest) == 0 || gotRest[0] != tt.wantArg) {
				t.Fatalf("rest=%#v want first arg %q", gotRest, tt.wantArg)
			}
		})
	}
}

func TestInterspersedFlagsAfterResource(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	opts, rest, err := parseFlags(reorderFlagArgs([]string{"deployment/api", "-n", "prod", "--proof", "--container", "api", "--selector", "app=api"}))
	if err != nil {
		t.Fatal(err)
	}
	if opts.namespace != "prod" || !opts.proof || opts.container != "api" || opts.labelSelector != "app=api" {
		t.Fatalf("flags after resource were not parsed: %#v", opts)
	}
	if len(rest) != 1 || rest[0] != "deployment/api" {
		t.Fatalf("resource positional not preserved: %#v", rest)
	}
}

func TestAnalyzerFilterSelectionForCommands(t *testing.T) {
	if got := splitCSV("Pod, Deployment,Service"); len(got) != 3 || got[0] != "Pod" || got[1] != "Deployment" || got[2] != "Service" {
		t.Fatalf("splitCSV did not split comma filters: %#v", got)
	}

	explicit := analyzerFiltersForCommand("incidents", nil, options{filters: "service,ingress"})
	if strings.Join(explicit, ",") != "service,ingress" {
		t.Fatalf("explicit filters should win, got %#v", explicit)
	}

	targeted := analyzerFiltersForCommand("why", []string{"service/api"}, options{})
	for _, want := range []string{"pod", "service", "networking"} {
		if !hasString(targeted, want) {
			t.Fatalf("smart service filters missing %q: %#v", want, targeted)
		}
	}

	quick := analyzerFiltersForCommand("incidents", nil, options{quick: true})
	if strings.Join(quick, ",") != "pod" {
		t.Fatalf("quick incident scan should use pod only, got %#v", quick)
	}
}

func TestIncidentDefaultsSimplifyProductionWorkflow(t *testing.T) {
	opts := options{visited: map[string]bool{}}
	applyWorkflowDefaults("fix", &opts)
	if !opts.includeLogs || !opts.typedClient || !opts.redact || !opts.paranoid || !opts.shadowVerify {
		t.Fatalf("fix defaults should enable logs, typed client, redaction, paranoid, and shadow: %#v", opts)
	}

	opts = options{quick: true, visited: map[string]bool{}}
	applyWorkflowDefaults("fix", &opts)
	if opts.shadowVerify {
		t.Fatalf("quick fix should skip default shadow verification: %#v", opts)
	}

	opts = options{gitops: true, repoPath: "./charts/api", visited: map[string]bool{}}
	applyWorkflowDefaults("fix", &opts)
	if !opts.sourcePatch || opts.shadowVerify {
		t.Fatalf("gitops fix should prefer source patch output without implicit shadow: %#v", opts)
	}

	opts = options{visited: map[string]bool{}}
	applyWorkflowDefaults("ui", &opts)
	if opts.includeLogs {
		t.Fatalf("ui should not enable log collection by default: %#v", opts)
	}
	if !opts.typedClient || !opts.redact {
		t.Fatalf("ui should still enable typed reads and redaction: %#v", opts)
	}
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestAICallRequiresRedactionUnlessUnsafe(t *testing.T) {
	var stderr bytes.Buffer
	finding := analyzer.Finding{Summary: "pod failed", Logs: []analyzer.LogSnippet{{Text: "password=hunter2"}}}
	augmentWithAI(context.Background(), &finding, options{redact: false, verbose: true}, &stderr)
	if finding.AI != nil {
		t.Fatal("expected AI to be blocked when redaction is disabled")
	}
	if !strings.Contains(stderr.String(), "require --redact") {
		t.Fatalf("expected redaction warning, got %s", stderr.String())
	}
}

func TestShadowDeliveryPRRequiresYesBeforeMutation(t *testing.T) {
	finding := analyzer.Finding{Namespace: "prod", ResourceKind: "Deployment", ResourceName: "api", Status: "ImagePullBackOff"}
	plan := fix.Concretize(fix.BuildPlan(finding), fix.ConcreteOptions{Container: "api", Image: "repo/api:v2"})
	var stdout, stderr bytes.Buffer
	code := runShadowWorkflow(context.Background(), &stdout, &stderr, options{delivery: "pr", repoPath: t.TempDir()}, kube.Kubectl{}, finding, plan)
	if code != 2 {
		t.Fatalf("expected --yes guard exit 2 before shadow mutation, got %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("expected --yes guard, got stderr=%s", stderr.String())
	}
}

func TestPolicyCheckUsesLint(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	dir := t.TempDir()
	manifest := filepath.Join(dir, "pod.yaml")
	err := os.WriteFile(manifest, []byte(`apiVersion: v1
kind: Pod
metadata:
  name: risky
spec:
  containers:
  - name: app
    image: nginx:latest
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"policy-check", "-f", manifest}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("policy-check failed: code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "latest tag") {
		t.Fatalf("expected policy-check lint finding, got %s", stdout.String())
	}
}

func TestConfigCommandsAreSecretSafe(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"auth", "set", "openai", "secret-token"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("auth set failed: code=%d stderr=%s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Execute([]string{"config", "view"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config view failed: code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "secret-token") {
		t.Fatalf("config view leaked secret: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "aiApiKeySet") {
		t.Fatalf("config view did not show key presence: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Execute([]string{"config", "export"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config export failed: code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "secret-token") || !strings.Contains(stdout.String(), "REDACTED") {
		t.Fatalf("config export should redact secret by default: %s", stdout.String())
	}
}

func TestConfigResolvedAndValidate(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"config", "set", "timeout", "45s"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config set failed: code=%d stderr=%s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Execute([]string{"config", "view", "--resolved", "--show-sources"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config resolved failed: code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"source": "config"`) || !strings.Contains(stdout.String(), `"value": "45s"`) {
		t.Fatalf("expected resolved source/value, got %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Execute([]string{"config", "validate"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config validate failed: code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"valid": true`) {
		t.Fatalf("expected valid config, got %s", stdout.String())
	}
}
