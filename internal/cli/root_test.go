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
