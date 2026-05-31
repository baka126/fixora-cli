package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLintAcceptsFilenameFlag(t *testing.T) {
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
