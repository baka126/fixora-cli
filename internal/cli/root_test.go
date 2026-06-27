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
	"github.com/fixora/kubectl-fixora/internal/shadow"
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

func TestBestTrustedImageCandidatePrefersHigherScore(t *testing.T) {
	finding := analyzer.Finding{Evidence: []analyzer.Evidence{
		{Label: "Ranked public image candidate (score 45)", Value: "example/low | public"},
		{Label: "Ranked public image candidate (score 65)", Value: "example/high | public"},
	}}
	image, score := bestTrustedImageCandidate(finding)
	if image != "example/high" || score != 65 {
		t.Fatalf("unexpected trusted candidate %q score=%d", image, score)
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
