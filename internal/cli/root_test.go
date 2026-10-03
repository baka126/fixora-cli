package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/repo"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

func TestHelpFixShowsFocusedUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"help", "fix"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help fix exit %d: %s", code, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "kubectl fixora fix <kind/name>") || !strings.Contains(got, "--delivery") {
		t.Fatalf("missing focused fix guidance: %s", got)
	}
	if strings.Contains(got, "Fast incident workflow:") {
		t.Fatalf("help fix showed general help: %s", got)
	}
}

func TestUnknownCommandSuggestsLikelyCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"scna"}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown command exit %d", code)
	}
	if !strings.Contains(stderr.String(), "Did you mean 'scan'?") {
		t.Fatalf("missing typo suggestion: %s", stderr.String())
	}
}

func TestCommandHelpFlagShowsFocusedUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"fix", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("fix --help exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage: kubectl fixora fix <kind/name>") {
		t.Fatalf("missing fix usage: %s", stdout.String())
	}
}

func TestUnknownCommandWithoutCloseMatchStaysConcise(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"teleportation"}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown command exit %d", code)
	}
	if strings.Contains(stderr.String(), "Did you mean") || strings.Contains(stderr.String(), "Fast incident workflow:") {
		t.Fatalf("unexpected suggestion or full help: %s", stderr.String())
	}
}

func TestDoctorChecksClusterCapabilities(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "kubectl"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"doctor", "-A", "-o", "json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("doctor exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "pods read") || !strings.Contains(stdout.String(), "checks") {
		t.Fatalf("doctor did not report cluster capabilities: %s", stdout.String())
	}
}

func TestAIDoctorCommandIsAvailable(t *testing.T) {
	if cmd, rest, err := normalizeCommand("ai", []string{"doctor"}); err != nil || cmd != "ai-doctor" || len(rest) != 0 {
		t.Fatalf("ai doctor routing: cmd=%q rest=%v err=%v", cmd, rest, err)
	}
}

func TestStartChooserDefaultsToDashboardAndCanSelectScan(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"\n", "dashboard"},
		{"1\n", "scan"},
		{"2\n", "doctor"},
		{"q\n", ""},
	} {
		var out bytes.Buffer
		if got := chooseStartCommand(strings.NewReader(tc.input), &out); got != tc.want {
			t.Fatalf("input %q: got %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestNoArgumentNonTerminalShowsHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := executeNoArgs(false, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("no args exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Fast incident workflow:") || strings.Contains(stderr.String(), "Scanning") {
		t.Fatalf("expected help without cluster work, stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestVerifiedPatchDeliverySummarizesClusterState(t *testing.T) {
	var stdout, stderr bytes.Buffer
	opts := options{output: "text", outFile: "patch.yaml"}
	if code := deliverVerifiedFix(context.Background(), &stdout, &stderr, opts, kube.Kubectl{}, analyzer.Finding{}, fix.Plan{}, shadow.Result{Parity: 95}, shadow.DeliveryPatch, true); code != 0 {
		t.Fatalf("patch delivery exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Outcome: Verified patch saved to patch.yaml; cluster unchanged.") {
		t.Fatalf("missing outcome: %s", stdout.String())
	}
}

func TestDeliveryHeadlineDoesNotClaimSkippedVerification(t *testing.T) {
	if got := deliveryHeadline(false, 0); got != "Fix not shadow-verified" {
		t.Fatalf("unverified headline: %q", got)
	}
	if got := deliveryHeadline(true, 95); got != "Fix Verified - Parity 95%" {
		t.Fatalf("verified headline: %q", got)
	}
}

func TestScriptedQuickFixSummarizesSavedPatch(t *testing.T) {
	plan := fix.Plan{
		Resource:      "deployment/api",
		Strategy:      "resources",
		PatchTemplate: "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        resources:\n          requests:\n            cpu: 100m\n",
		ApplyEligible: true,
		CanApply:      true,
	}
	opts := options{output: "text", yes: true, outFile: filepath.Join(t.TempDir(), "patch.yaml")}
	var stdout, stderr bytes.Buffer
	if code := runGuidedFix(context.Background(), &stdout, &stderr, opts, kube.Kubectl{}, analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api"}, plan, "deployment/api"); code != 0 {
		t.Fatalf("quick fix exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Outcome: Unverified patch saved") || !strings.Contains(stdout.String(), "cluster unchanged") {
		t.Fatalf("missing quick fix outcome: %s", stdout.String())
	}
}

func TestReviewOnlyPRBodyDoesNotClaimShadowVerification(t *testing.T) {
	body := prBody(shadow.Result{}, repo.SourcePatch{Path: "patch.yaml"}, analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "prod"}, false)
	if strings.Contains(body, "verified this remediation") || strings.Contains(body, "Parity") {
		t.Fatalf("unverified PR claims verification: %s", body)
	}
	if !strings.Contains(body, "Shadow verification was not run") || !strings.Contains(body, "Deployment/api") {
		t.Fatalf("missing review-only context: %s", body)
	}
}

func TestIsTerminalRejectsDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("/dev/null is a character device but not an interactive terminal")
	}
}

func TestQuickPRDeliveryOpensReviewRequest(t *testing.T) {
	root := t.TempDir()
	repoPath := filepath.Join(root, "source")
	remotePath := filepath.Join(root, "remote.git")
	if err := os.Mkdir(repoPath, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git(root, "init", "--bare", remotePath)
	git(repoPath, "init")
	git(repoPath, "config", "user.email", "fixora@example.test")
	git(repoPath, "config", "user.name", "Fixora Test")
	git(repoPath, "remote", "add", "origin", remotePath)
	if err := os.WriteFile(filepath.Join(repoPath, "deployment.yaml"), []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(repoPath, "add", ".")
	git(repoPath, "commit", "-m", "initial")
	binDir := filepath.Join(root, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte("#!/bin/sh\nprintf 'https://example.test/pr/1\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	plan := fix.Plan{Resource: "deployment/api", PatchTemplate: "spec:\n  replicas: 2\n", ApplyEligible: true, CanApply: true}
	opts := options{output: "text", yes: true, delivery: "pr", visited: map[string]bool{"delivery": true}, sourcePatch: true, repoPath: repoPath, outFile: filepath.Join(root, "patch.yaml")}
	var stdout, stderr bytes.Buffer
	if code := runGuidedFix(context.Background(), &stdout, &stderr, opts, kube.Kubectl{}, analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "prod"}, plan, "deployment/api"); code != 0 {
		t.Fatalf("quick PR exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Outcome: PR opened at https://example.test/pr/1") {
		t.Fatalf("quick PR did not open a request: %s", stdout.String())
	}
}

func TestCanonicalPRDeliveryAcceptsCaseInsensitiveMode(t *testing.T) {
	if !canonicalPRDelivery(options{delivery: "PR", visited: map[string]bool{"delivery": true}}) {
		t.Fatal("explicit uppercase PR must use the PR delivery path")
	}
	if canonicalPRDelivery(options{delivery: "pr"}) {
		t.Fatal("legacy/default delivery must keep its existing path")
	}
}

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

func TestProductionDefaultTimeouts(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	opts, _, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.timeout != 3*time.Minute {
		t.Fatalf("expected default analysis timeout 3m, got %s", opts.timeout)
	}
	if opts.shadowTimeout != 10*time.Minute {
		t.Fatalf("expected default shadow timeout 10m, got %s", opts.shadowTimeout)
	}
}

func TestFixCommandContextDoesNotExpireBeforeShadow(t *testing.T) {
	base := context.Background()
	ctx, cancel := commandContext(base, "fix", options{timeout: time.Second})
	defer cancel()
	if deadline, ok := ctx.Deadline(); ok {
		t.Fatalf("fix command context should not have global deadline before shadow, got %s", deadline)
	}

	analysisCtx, analysisCancel := fixAnalysisContext(ctx, time.Second)
	defer analysisCancel()
	if _, ok := analysisCtx.Deadline(); !ok {
		t.Fatal("fix analysis context should retain the configured analysis timeout")
	}
}

func TestClusterCommandContextDoesNotExpire(t *testing.T) {
	ctx, cancel := commandContext(context.Background(), "cluster", options{timeout: time.Second})
	defer cancel()
	if deadline, ok := ctx.Deadline(); ok {
		t.Fatalf("cluster command context should not have global deadline, got %s", deadline)
	}
}

func TestNonFixCommandContextKeepsGlobalTimeout(t *testing.T) {
	ctx, cancel := commandContext(context.Background(), "incidents", options{timeout: time.Second})
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("non-fix command context should keep the global timeout")
	}
}

func TestCoordinateInHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("help exit %d", code)
	}
	if !strings.Contains(stdout.String(), "coordinate") {
		t.Fatalf("help must document the coordinate subcommand")
	}
}

func TestCoordinateRequiresRefs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"coordinate"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("coordinate with no refs must fail")
	}
	if !strings.Contains(stderr.String(), "resource") {
		t.Fatalf("expected a usage error mentioning resources, got %q", stderr.String())
	}

	// Boundary: exactly one ref must also fail (coordinate needs two or more).
	stdout.Reset()
	stderr.Reset()
	if code := Execute([]string{"coordinate", "deployment/api"}, &stdout, &stderr); code == 0 {
		t.Fatal("coordinate with a single ref must fail (requires two or more)")
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
	for _, want := range []string{"custom-analyzers", "serve --mcp", "fix [kind/name]", "--shadow-timeout"} {
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
	opts, rest, err := parseFlags([]string{"deployment/api", "-n", "prod", "--proof", "--container", "api", "--selector", "app=api"})
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

	targeted := analyzerFiltersForCommand("fix", []string{"service/api"}, options{})
	for _, want := range []string{"pod", "service", "networking"} {
		if !hasString(targeted, want) {
			t.Fatalf("smart service filters missing %q: %#v", want, targeted)
		}
	}

	quick := analyzerFiltersForCommand("incidents", nil, options{quick: true})
	if strings.Join(quick, ",") != "pod" {
		t.Fatalf("quick incident scan should use pod only, got %#v", quick)
	}
	defaultScan := analyzerFiltersForCommand("incidents", nil, options{})
	if strings.Join(defaultScan, ",") != "pod" {
		t.Fatalf("default incident scan should use pod only, got %#v", defaultScan)
	}
	health := analyzerFiltersForCommand("health", nil, options{})
	for _, want := range []string{"pod", "deployment", "service", "pvc"} {
		if !hasString(health, want) {
			t.Fatalf("health filters should stay comprehensive, missing %q: %#v", want, health)
		}
	}
}

func TestIncidentDefaultsSimplifyProductionWorkflow(t *testing.T) {
	opts := options{visited: map[string]bool{}}
	applyWorkflowDefaults("fix", &opts)
	if !opts.includeLogs || !opts.typedClient || !opts.redact || !opts.paranoid || !opts.shadowVerify || !opts.useAI {
		t.Fatalf("fix defaults should enable logs, typed client, redaction, paranoid, AI, and shadow: %#v", opts)
	}
	if opts.shadowRetries != 1 {
		t.Fatalf("fix defaults should allow one redacted AI shadow retry, got %d", opts.shadowRetries)
	}

	opts = options{quick: true, visited: map[string]bool{"no-ai": true}, noAI: true}
	applyWorkflowDefaults("fix", &opts)
	if opts.shadowVerify {
		t.Fatalf("quick fix should skip default shadow verification: %#v", opts)
	}
	if opts.useAI {
		t.Fatalf("--no-ai should disable default AI remediation: %#v", opts)
	}

	// --gitops now maps to --delivery=pr via reconcileDeliveryFlags (intentional consolidation).
	// applyWorkflowDefaults still disables shadow for --gitops; delivery is set by reconcileDeliveryFlags.
	opts = options{gitops: true, repoPath: "./charts/api", visited: map[string]bool{"gitops": true}}
	var warnBuf bytes.Buffer
	applyWorkflowDefaults("fix", &opts)
	reconcileDeliveryFlags(&opts, &warnBuf)
	if opts.delivery != "pr" || opts.shadowVerify {
		t.Fatalf("gitops fix should set delivery=pr and disable shadow: delivery=%q shadowVerify=%v opts=%#v", opts.delivery, opts.shadowVerify, opts)
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

func TestShadowClusterDeliveryBlocksGitOpsManagedResource(t *testing.T) {
	finding := analyzer.Finding{
		Namespace:    "prod",
		ResourceKind: "Deployment",
		ResourceName: "api",
		Status:       "ImagePullBackOff",
		GitOps:       analyzer.GitOpsHints{ManagedBy: "Helm", TargetAdvice: "Patch the Helm values source, not rendered Kubernetes YAML."},
	}
	plan := fix.Concretize(fix.BuildPlan(finding), fix.ConcreteOptions{Container: "api", Image: "repo/api:v2"})
	var stdout, stderr bytes.Buffer
	code := runShadowWorkflow(context.Background(), &stdout, &stderr, options{delivery: "cluster"}, kube.Kubectl{}, finding, plan)
	if code != 2 || !strings.Contains(stderr.String(), "GitOps-managed") {
		t.Fatalf("expected GitOps cluster delivery block, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestShadowPRDeliveryBlocksAdvisoryHelmSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte("apiVersion: v2\nname: api\nversion: 0.1.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	finding := analyzer.Finding{Namespace: "prod", ResourceKind: "Deployment", ResourceName: "api", Status: "ImagePullBackOff"}
	plan := fix.Concretize(fix.BuildPlan(finding), fix.ConcreteOptions{Container: "api", Image: "repo/api:v2"})
	var stdout, stderr bytes.Buffer
	code := runShadowWorkflow(context.Background(), &stdout, &stderr, options{delivery: "pr", repoPath: dir, yes: true}, kube.Kubectl{}, finding, plan)
	if code != 2 || !strings.Contains(stderr.String(), "Helm PR delivery is blocked") {
		t.Fatalf("expected Helm PR delivery block, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestEditorCommandPrefersVisualAndDoesNotUseShell(t *testing.T) {
	parts, err := editorCommand("go env", "vi")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0] != "go" || parts[1] != "env" {
		t.Fatalf("expected VISUAL command to be split into argv, got %#v", parts)
	}
	if _, err := editorCommand("", "/definitely/not/fixora-editor"); err == nil {
		t.Fatal("expected missing editor binary to be rejected")
	}
}

func TestApplyAIPatchIfSafeAcceptsValidatedPatch(t *testing.T) {
	finding := analyzer.Finding{
		Namespace:    "prod",
		ResourceKind: "Deployment",
		ResourceName: "api",
		Status:       "ImagePullBackOff",
		AI: &analyzer.AIResult{
			PatchYAML: `spec:
  template:
    spec:
      containers:
      - name: api
        image: repo/api:v2
`,
			Confidence: 94,
		},
	}
	plan := applyAIPatchIfSafe(context.Background(), fix.BuildPlan(finding), finding, &bytes.Buffer{}, true)
	if !plan.ApplyEligible {
		t.Fatalf("expected AI patch to become apply eligible: %#v", plan)
	}
	if !strings.Contains(plan.PatchTemplate, "image: repo/api:v2") {
		t.Fatalf("expected AI patch in plan:\n%s", plan.PatchTemplate)
	}
	if !hasString(plan.Guardrails, "ai-patch-safety-validated") {
		t.Fatalf("expected AI validation guardrail: %#v", plan.Guardrails)
	}
}

func TestApplyAIPatchIfSafeRejectsUnsafePatch(t *testing.T) {
	finding := analyzer.Finding{
		Namespace:    "prod",
		ResourceKind: "Deployment",
		ResourceName: "api",
		Status:       "ImagePullBackOff",
		AI: &analyzer.AIResult{
			PatchYAML: `metadata:
  labels:
    app: changed
spec:
  template:
    spec:
      containers:
      - name: api
        image: repo/api:v2
`,
		},
	}
	plan := applyAIPatchIfSafe(context.Background(), fix.BuildPlan(finding), finding, &bytes.Buffer{}, true)
	if plan.ApplyEligible {
		t.Fatalf("unsafe AI patch must not become apply eligible: %#v", plan)
	}
	if !strings.Contains(strings.Join(plan.Warnings, "\n"), "AI patch rejected") {
		t.Fatalf("expected AI rejection warning, got %#v", plan.Warnings)
	}
}

func TestApplyAIPatchIfSafeKeepsServicePatchReviewOnly(t *testing.T) {
	finding := analyzer.Finding{
		Namespace:    "prod",
		ResourceKind: "Service",
		ResourceName: "api",
		Status:       "NoEndpoints",
		AI: &analyzer.AIResult{
			PatchYAML: `apiVersion: v1
kind: Service
metadata:
  name: api
  namespace: prod
spec:
  selector:
    app.kubernetes.io/name: api
`,
			Confidence: 90,
		},
	}
	plan := applyAIPatchIfSafe(context.Background(), fix.BuildPlan(finding), finding, &bytes.Buffer{}, true)
	if plan.ApplyEligible {
		t.Fatalf("service selector patch must remain review-only: %#v", plan)
	}
	if !strings.Contains(plan.PatchTemplate, "kind: Service") || !hasString(plan.Guardrails, "ai-patch-review-only") {
		t.Fatalf("expected review-only service patch, got %#v patch=%s", plan.Guardrails, plan.PatchTemplate)
	}
}

func TestValidateReviewOnlyAIPatchRejectsUnsafeFields(t *testing.T) {
	err := validateReviewOnlyAIPatch(`apiVersion: v1
kind: Pod
metadata:
  name: bad
spec:
  hostNetwork: true
  containers:
  - name: app
    image: busybox
`)
	if err == nil {
		t.Fatal("expected unsafe review-only patch rejection")
	}
	if err := validateReviewOnlyAIPatch(`apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: prod
data:
  MODE: safe
`); err != nil {
		t.Fatalf("expected configmap review patch to be allowed: %v", err)
	}
}

func TestPatchImagesAndNodePlatformFromFinding(t *testing.T) {
	finding := analyzer.Finding{Evidence: []analyzer.Evidence{
		{Label: "Node platform", Value: "linux/arm64"},
		{Label: "Container image api", Value: "repo/api:v1"},
	}}
	platform, ok := nodePlatformFromFinding(finding)
	if !ok || platform.OS != "linux" || platform.Architecture != "arm64" {
		t.Fatalf("unexpected platform: %#v ok=%t", platform, ok)
	}
	images, err := patchImages(`spec:
  containers:
  - name: api
    image: repo/api:v2
  - name: sidecar
    image: repo/sidecar:v1
`)
	if err != nil || len(images) != 2 || images[0] != "repo/api:v2" {
		t.Fatalf("unexpected patch images: %#v err=%v", images, err)
	}
}

func TestWriteReviewPatchUsesEditedFileAsPlanPatch(t *testing.T) {
	dir := t.TempDir()
	editor := filepath.Join(dir, "editor.sh")
	err := os.WriteFile(editor, []byte(`#!/bin/sh
cat > "$1" <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    spec:
      containers:
      - name: api
        image: repo/api:v3
EOF
`), 0o700)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", editor)

	outFile := filepath.Join(dir, "fixora-patch.yaml")
	plan := fix.Plan{Resource: "Deployment/api", Strategy: "image", PatchTemplate: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    spec:
      containers:
      - name: api
        image: repo/api:v2
`}
	var stdout, stderr bytes.Buffer
	updated, err := writeReviewPatch(context.Background(), &stdout, &stderr, options{outFile: outFile, editPatch: true}, plan)
	if err != nil {
		t.Fatalf("writeReviewPatch failed: %v stderr=%s", err, stderr.String())
	}
	if !strings.Contains(updated.PatchTemplate, "image: repo/api:v3") {
		t.Fatalf("edited patch was not loaded into plan:\n%s", updated.PatchTemplate)
	}
	onDisk, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != updated.PatchTemplate {
		t.Fatalf("plan patch should match reviewed file:\nfile=%s\nplan=%s", string(onDisk), updated.PatchTemplate)
	}
}

func TestWriteReviewPatchRejectsInvalidEditedIdentity(t *testing.T) {
	dir := t.TempDir()
	editor := filepath.Join(dir, "editor.sh")
	err := os.WriteFile(editor, []byte(`#!/bin/sh
sed -i.bak 's/apiVersion: v1/apiVersion: v0/' "$1"
rm -f "$1.bak"
`), 0o700)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", editor)
	outFile := filepath.Join(dir, "fixora-patch.yaml")
	plan := fix.Plan{Resource: "Pod/api", Strategy: "image", PatchTemplate: `apiVersion: v1
kind: Pod
metadata:
  name: api
  namespace: prod
spec:
  containers:
  - name: api
    image: repo/api:v1
`}
	_, err = writeReviewPatch(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, options{outFile: outFile, editPatch: true}, plan)
	if err == nil || !strings.Contains(err.Error(), "apiVersion must match") {
		t.Fatalf("expected edited identity validation error, got %v", err)
	}
}

func TestWriteShadowFailureDoesNotEmitRawJSON(t *testing.T) {
	var output bytes.Buffer
	writeShadowFailure(&output, shadow.Result{Attempts: []shadow.Attempt{{Number: 1, Phase: "Pending", ExitReason: "Error", Logs: []string{"password=hunter2"}}}}, "fixora-patch.yaml", analyzer.Finding{}, fix.Plan{})
	got := output.String()
	for _, want := range []string{"Shadow verification failed", "No production mutation", "Last log: password=[REDACTED]", "fixora-patch.yaml"} {
		if !strings.Contains(got, want) {
			t.Fatalf("shadow failure output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\"verified\"") {
		t.Fatalf("shadow failure should not render JSON:\n%s", got)
	}
}

func TestWriteShadowFailureExplainsArchitectureOOMFollowup(t *testing.T) {
	var output bytes.Buffer
	writeShadowFailure(
		&output,
		shadow.Result{Attempts: []shadow.Attempt{{Number: 1, Phase: "Pending", ExitReason: "OOMKilled"}}},
		"fixora-patch.yaml",
		analyzer.Finding{Status: "ExecFormatError"},
		fix.Plan{Strategy: "fix-architecture"},
	)
	got := output.String()
	for _, want := range []string{"architecture symptom appears resolved", "Treat this as a second failure", "combined resource right-sizing patch", "Delivery remains blocked"} {
		if !strings.Contains(got, want) {
			t.Fatalf("shadow architecture/OOM guidance missing %q:\n%s", want, got)
		}
	}
}

func TestShadowRetryProviderUsesConfiguredProvider(t *testing.T) {
	t.Setenv("FIXORA_AI_PROVIDER", "noop")
	provider, retries := shadowRetryProvider(options{useAI: true, shadowRetries: 1}, &bytes.Buffer{})
	if provider == nil || retries != 1 {
		t.Fatalf("expected configured AI retry provider and one retry, provider=%T retries=%d", provider, retries)
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
	code = Execute([]string{"config"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("bare config command failed: code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "secret-token") {
		t.Fatalf("bare config command leaked secret: %s", stdout.String())
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

func TestAugmentWithAIDiscardsUnstructured(t *testing.T) {
	finding := analyzer.Finding{Summary: "deterministic summary"}
	finding.AI = &analyzer.AIResult{RootCause: "garbage", Unstructured: true}
	var stderr bytes.Buffer
	// handleUnstructuredAI is the extracted, testable post-processing step.
	handleUnstructuredAI(&finding, &stderr)
	if finding.AI != nil {
		t.Fatalf("expected AI result discarded when unstructured")
	}
	if !strings.Contains(stderr.String(), "deterministic plan") {
		t.Fatalf("expected warning about deterministic fallback, got %q", stderr.String())
	}
}

func TestFailWritesNextStep(t *testing.T) {
	var buf bytes.Buffer
	code := fail(&buf, "something broke", "kubectl fixora fix api --repo .")
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "error: something broke") {
		t.Fatalf("missing error line: %q", out)
	}
	if !strings.Contains(out, "Next: kubectl fixora fix api --repo .") {
		t.Fatalf("missing Next line: %q", out)
	}
}

func TestFailWithoutNextStep(t *testing.T) {
	var buf bytes.Buffer
	fail(&buf, "no hint", "")
	if strings.Contains(buf.String(), "Next:") {
		t.Fatalf("did not expect Next line: %q", buf.String())
	}
}

func TestSourceManagedTreatsHelmChartAsManaged(t *testing.T) {
	// Hardening: a finding whose only Helm signal is HelmChart must still be
	// blocked from direct cluster apply and routed to PR delivery.
	f := analyzer.Finding{GitOps: analyzer.GitOpsHints{HelmChart: "redis-1.2.3"}}
	if !sourceManaged(f) {
		t.Fatal("HelmChart-only finding must be treated as source-managed")
	}
}

func TestReconcileDeliveryFlags(t *testing.T) {
	t.Run("apply maps to cluster", func(t *testing.T) {
		var w bytes.Buffer
		o := &options{apply: true, delivery: "patch", visited: map[string]bool{"apply": true}}
		reconcileDeliveryFlags(o, &w)
		if o.delivery != "cluster" {
			t.Fatalf("got %q", o.delivery)
		}
		if !strings.Contains(w.String(), "deprecated") {
			t.Fatalf("expected deprecation notice, got %q", w.String())
		}
	})
	t.Run("gitops maps to pr", func(t *testing.T) {
		var w bytes.Buffer
		o := &options{gitops: true, delivery: "patch", visited: map[string]bool{"gitops": true}}
		reconcileDeliveryFlags(o, &w)
		if o.delivery != "pr" {
			t.Fatalf("got %q", o.delivery)
		}
		if !o.sourcePatch {
			t.Fatalf("gitops must keep sourcePatch=true for the legacy non-shadow delivery path")
		}
		if !strings.Contains(w.String(), "deprecated") {
			t.Fatalf("expected deprecation notice, got %q", w.String())
		}
	})
	t.Run("explicit delivery wins", func(t *testing.T) {
		var w bytes.Buffer
		o := &options{apply: true, delivery: "pr", visited: map[string]bool{"apply": true, "delivery": true}}
		reconcileDeliveryFlags(o, &w)
		if o.delivery != "pr" {
			t.Fatalf("explicit --delivery must win, got %q", o.delivery)
		}
	})
	t.Run("delivery=cluster sets apply for the non-shadow path", func(t *testing.T) {
		var w bytes.Buffer
		o := &options{delivery: "cluster", visited: map[string]bool{"delivery": true}}
		reconcileDeliveryFlags(o, &w)
		if !o.apply {
			t.Fatal("--delivery=cluster must set apply so --quick performs the apply")
		}
	})
	t.Run("delivery=pr sets sourcePatch for the non-shadow path", func(t *testing.T) {
		var w bytes.Buffer
		o := &options{delivery: "pr", visited: map[string]bool{"delivery": true}}
		reconcileDeliveryFlags(o, &w)
		if !o.sourcePatch {
			t.Fatal("--delivery=pr must set sourcePatch so --quick writes the source patch")
		}
	})
}

func TestPrintHelpDocumentsDelivery(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	out := buf.String()
	for _, want := range []string{"--delivery", "walkthrough", "patch, cluster, or pr"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help missing %q; got:\n%s", want, out)
		}
	}
}

type fakeGate struct {
	ok       bool
	output   string
	statErr  error
	events   []kube.Event
	runCalls [][]string
	job      kube.JobState
	jobErr   error
	cron     kube.CronJobState
	cronErr  error
}

func (f *fakeGate) RolloutStatus(_ context.Context, _, _, _ string, _ time.Duration) (bool, string, error) {
	return f.ok, f.output, f.statErr
}
func (f *fakeGate) GetEvents(_ context.Context, _, _ string) ([]kube.Event, error) {
	return f.events, nil
}
func (f *fakeGate) Run(_ context.Context, args ...string) ([]byte, error) {
	f.runCalls = append(f.runCalls, args)
	return nil, nil
}

// Completion fields + methods for fakeGate (Job/CronJob gate tests).
func (f *fakeGate) JobStatus(_ context.Context, _, _ string, _ time.Duration) (kube.JobState, error) {
	return f.job, f.jobErr
}
func (f *fakeGate) CronJobStatus(_ context.Context, _, _ string) (kube.CronJobState, error) {
	return f.cron, f.cronErr
}

func gateFinding() analyzer.Finding {
	return analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "prod"}
}
func gatePlan() fix.Plan {
	return fix.Plan{RollbackCommand: "kubectl rollout undo deployment/api -n prod"}
}

func TestGateRolloutHealthyReturnsZero(t *testing.T) {
	g := &fakeGate{ok: true, output: "successfully rolled out"}
	var out, errb bytes.Buffer
	code := gateRollout(context.Background(), &out, &errb, strings.NewReader(""), false, g, gateFinding(), gatePlan(), time.Minute)
	if code != 0 {
		t.Fatalf("healthy rollout must return 0, got %d", code)
	}
	if len(g.runCalls) != 0 {
		t.Fatalf("healthy rollout must not run rollback, got %#v", g.runCalls)
	}
	// Status lines go to stderr so structured stdout (json/yaml/sarif) stays clean.
	if out.Len() != 0 {
		t.Fatalf("gate must not write status to stdout, got %q", out.String())
	}
	if !strings.Contains(errb.String(), "rollout healthy") {
		t.Fatalf("healthy status must go to stderr, got %q", errb.String())
	}
}

func TestGateRolloutSkippedReturnsZero(t *testing.T) {
	g := &fakeGate{ok: false}
	var out, errb bytes.Buffer
	finding := analyzer.Finding{ResourceKind: "Pod", ResourceName: "api-0", Namespace: "prod"}
	code := gateRollout(context.Background(), &out, &errb, strings.NewReader(""), false, g, finding, gatePlan(), time.Minute)
	if code != 0 {
		t.Fatalf("skipped rollout must return 0, got %d", code)
	}
	if !strings.Contains(errb.String(), "warning:") {
		t.Fatalf("skipped rollout must warn on stderr, got %q", errb.String())
	}
}

func TestGateRolloutUnknownReturnsZero(t *testing.T) {
	g := &fakeGate{statErr: errors.New("forbidden")}
	var out, errb bytes.Buffer
	code := gateRollout(context.Background(), &out, &errb, strings.NewReader(""), false, g, gateFinding(), gatePlan(), time.Minute)
	if code != 0 {
		t.Fatalf("unknown rollout must return 0, got %d", code)
	}
	if !strings.Contains(errb.String(), "warning:") {
		t.Fatalf("unknown rollout must warn on stderr, got %q", errb.String())
	}
}

func TestGateRolloutMissingNameFailsVerification(t *testing.T) {
	g := &fakeGate{ok: false}
	var out, errb bytes.Buffer
	finding := analyzer.Finding{ResourceKind: "Deployment", Namespace: "prod"}
	code := gateRollout(context.Background(), &out, &errb, strings.NewReader(""), false, g, finding, gatePlan(), time.Minute)
	if code == 0 {
		t.Fatal("missing rollout target name must fail verification")
	}
	if !strings.Contains(errb.String(), "missing resource name") {
		t.Fatalf("missing name failure must explain the verification issue, got %q", errb.String())
	}
}

func TestGateRolloutFailureOffersAndRunsRollback(t *testing.T) {
	g := &fakeGate{ok: false, output: "Waiting for deployment rollout to finish: 1 of 3 updated replicas are available"}
	var out, errb bytes.Buffer
	code := gateRollout(context.Background(), &out, &errb, strings.NewReader("y\n"), false, g, gateFinding(), gatePlan(), time.Minute)
	if code == 0 {
		t.Fatal("failed rollout must return non-zero")
	}
	if len(g.runCalls) != 1 || g.runCalls[0][0] != "rollout" {
		t.Fatalf("confirmed rollback must run kubectl rollout undo, got %#v", g.runCalls)
	}
}

func TestGateRolloutYesModeReportsWithoutRunning(t *testing.T) {
	g := &fakeGate{ok: false, output: "Waiting for deployment rollout to finish"}
	var out, errb bytes.Buffer
	code := gateRollout(context.Background(), &out, &errb, strings.NewReader(""), true, g, gateFinding(), gatePlan(), time.Minute)
	if code == 0 {
		t.Fatal("failed rollout must return non-zero in --yes mode")
	}
	if len(g.runCalls) != 0 {
		t.Fatalf("--yes must not auto-rollback, got %#v", g.runCalls)
	}
	if !strings.Contains(errb.String(), "kubectl rollout undo deployment/api") {
		t.Fatalf("--yes must print the rollback command, got %q", errb.String())
	}
}

func jobGateFinding() analyzer.Finding {
	return analyzer.Finding{ResourceKind: "Job", ResourceName: "migrate", Namespace: "prod"}
}

func TestGateRolloutJobCompleteReturnsZero(t *testing.T) {
	g := &fakeGate{job: kube.JobState{Complete: true}}
	var out, errb bytes.Buffer
	code := gateRollout(context.Background(), &out, &errb, strings.NewReader(""), false, g, jobGateFinding(), fix.Plan{}, time.Minute)
	if code != 0 {
		t.Fatalf("completed job must return 0, got %d", code)
	}
	if len(g.runCalls) != 0 {
		t.Fatalf("completed job must not run remediation, got %#v", g.runCalls)
	}
}

func TestGateRolloutJobFailedRunsDeleteOnConsent(t *testing.T) {
	g := &fakeGate{job: kube.JobState{Failed: true, Detail: "succeeded 0, failed 6, active 0"}}
	var out, errb bytes.Buffer
	code := gateRollout(context.Background(), &out, &errb, strings.NewReader("y\n"), false, g, jobGateFinding(), fix.Plan{}, time.Minute)
	if code == 0 {
		t.Fatal("failed job must return non-zero")
	}
	if len(g.runCalls) != 1 || g.runCalls[0][0] != "delete" {
		t.Fatalf("consented remediation must run kubectl delete, got %#v", g.runCalls)
	}
}

func TestGateRolloutJobFailedYesModeReportsWithoutRunning(t *testing.T) {
	g := &fakeGate{job: kube.JobState{Failed: true}}
	var out, errb bytes.Buffer
	code := gateRollout(context.Background(), &out, &errb, strings.NewReader(""), true, g, jobGateFinding(), fix.Plan{}, time.Minute)
	if code == 0 {
		t.Fatal("failed job must return non-zero in --yes mode")
	}
	if len(g.runCalls) != 0 {
		t.Fatalf("--yes must not auto-remediate, got %#v", g.runCalls)
	}
	if !strings.Contains(errb.String(), "kubectl delete job/migrate") {
		t.Fatalf("--yes must print the remediation command, got %q", errb.String())
	}
}

func TestGateRolloutJobPendingReturnsZero(t *testing.T) {
	g := &fakeGate{job: kube.JobState{}}
	var out, errb bytes.Buffer
	code := gateRollout(context.Background(), &out, &errb, strings.NewReader(""), false, g, jobGateFinding(), fix.Plan{}, time.Minute)
	if code != 0 {
		t.Fatalf("still-running job must be non-blocking (0), got %d", code)
	}
}

func cronGateFinding() analyzer.Finding {
	return analyzer.Finding{ResourceKind: "CronJob", ResourceName: "nightly", Namespace: "prod"}
}

func TestGateRolloutCronJobFailingRunsSuspendOnConsent(t *testing.T) {
	g := &fakeGate{cron: kube.CronJobState{Schedule: "0 * * * *", RecentJobFailed: true, Detail: "most recent job nightly-1: succeeded 0, failed 1, active 0"}}
	var out, errb bytes.Buffer
	code := gateRollout(context.Background(), &out, &errb, strings.NewReader("y\n"), false, g, cronGateFinding(), fix.Plan{}, time.Minute)
	if code == 0 {
		t.Fatal("failing cronjob must return non-zero")
	}
	if len(g.runCalls) != 1 || g.runCalls[0][0] != "patch" {
		t.Fatalf("consented remediation must run kubectl patch suspend, got %#v", g.runCalls)
	}
}

func TestGateRolloutCronJobHealthyReturnsZero(t *testing.T) {
	g := &fakeGate{cron: kube.CronJobState{Schedule: "0 * * * *", LastSuccessful: "2026-06-27T00:00:00Z"}}
	var out, errb bytes.Buffer
	code := gateRollout(context.Background(), &out, &errb, strings.NewReader(""), false, g, cronGateFinding(), fix.Plan{}, time.Minute)
	if code != 0 {
		t.Fatalf("healthy cronjob must return 0, got %d", code)
	}
	if len(g.runCalls) != 0 {
		t.Fatalf("healthy cronjob must not run remediation, got %#v", g.runCalls)
	}
}

func TestGateRolloutTrimsCompletionKind(t *testing.T) {
	g := &fakeGate{cron: kube.CronJobState{Schedule: "0 * * * *", LastSuccessful: "2026-06-27T00:00:00Z"}}
	var out, errb bytes.Buffer
	finding := analyzer.Finding{ResourceKind: " CronJob ", ResourceName: "nightly", Namespace: "prod"}
	code := gateRollout(context.Background(), &out, &errb, strings.NewReader(""), false, g, finding, fix.Plan{}, time.Minute)
	if code != 0 {
		t.Fatalf("trimmed cronjob kind must route to completion verifier, got %d", code)
	}
	if !strings.Contains(errb.String(), "CronJob/nightly accepted") {
		t.Fatalf("expected completion verifier output, got %q", errb.String())
	}
}

func TestMCPShadowFlagRequiresMCPMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"serve", "--mcp-shadow"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "requires --mcp") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestAugmentWithAIReportsDeterministicFallback(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("private provider detail"))
	}))
	defer srv.Close()
	t.Setenv("FIXORA_AI_PROVIDER", "openai")
	t.Setenv("FIXORA_AI_BASE_URL", srv.URL)
	t.Setenv("FIXORA_AI_API_KEY", "test")
	finding := analyzer.Finding{Summary: "pod failed"}
	var stderr bytes.Buffer
	augmentWithAI(context.Background(), &finding, options{redact: true}, &stderr)
	if finding.AI != nil || !strings.Contains(stderr.String(), "deterministic plan") || !strings.Contains(stderr.String(), "HTTP 400") || strings.Contains(stderr.String(), "private provider detail") {
		t.Fatalf("AI=%#v warning=%q", finding.AI, stderr.String())
	}
}
