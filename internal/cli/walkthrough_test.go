package cli

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
)

func TestInteractiveFixDetection(t *testing.T) {
	o := options{output: "text", promptInput: bufio.NewReader(strings.NewReader(""))}
	if !interactiveFix(o) {
		t.Fatal("expected interactive when promptInput set and output=text")
	}
	o.yes = true
	if interactiveFix(o) {
		t.Fatal("expected non-interactive when --yes set")
	}
	o.yes = false
	o.output = "json"
	if interactiveFix(o) {
		t.Fatal("expected non-interactive when output != text")
	}
}

func TestWalkthroughQuitAtRootCause(t *testing.T) {
	finding := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Summary: "no cpu request"}
	plan := fix.Plan{Resource: "deployment/api", ApplyEligible: true, PatchTemplate: "spec:\n  replicas: 1\n"}
	opts := options{output: "text", promptInput: bufio.NewReader(strings.NewReader("q\n"))}
	var stdout, stderr bytes.Buffer
	code := runFixWalkthrough(context.Background(), &stdout, &stderr, opts, kube.Kubectl{}, finding, plan, "deployment/api")
	if code != 0 {
		t.Fatalf("quit should exit 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Root cause") {
		t.Fatalf("expected Step 1 header, got %q", stdout.String())
	}
}

func TestWalkthroughNoShadowExplicitPatch(t *testing.T) {
	finding := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Summary: "no cpu request"}
	// Apply-eligible plan with a concrete safe patch.
	plan := fix.Plan{
		Resource:      "deployment/api",
		Strategy:      "resources",
		PatchTemplate: "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        resources:\n          requests:\n            cpu: 100m\n",
		ApplyEligible: true,
		CanApply:      true,
	}
	patchPath := t.TempDir() + "/p.yaml"
	opts := options{
		output:       "text",
		shadowVerify: false,
		delivery:     "patch",
		visited:      map[string]bool{"delivery": true},
		outFile:      patchPath,
		// Step 1 continue, Step 2 proceed. No shadow prompt, no delivery menu.
		promptInput: bufio.NewReader(strings.NewReader("\n\n")),
	}
	var stdout, stderr bytes.Buffer
	code := runFixWalkthrough(context.Background(), &stdout, &stderr, opts, kube.Kubectl{}, finding, plan, "deployment/api")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "skipped (--no-shadow)") {
		t.Fatalf("expected shadow-skipped messaging, got %q", stdout.String())
	}
	if _, err := os.Stat(patchPath); err != nil {
		t.Fatalf("expected patch file written at %s: %v", patchPath, err)
	}
	if !strings.Contains(stdout.String(), "Outcome: Unverified patch saved") || !strings.Contains(stdout.String(), "cluster unchanged") {
		t.Fatalf("missing accurate final outcome: %q", stdout.String())
	}
}

func TestWalkthroughExplicitDeliverySkipsMenu(t *testing.T) {
	finding := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Summary: "no cpu request"}
	plan := fix.Plan{
		Resource:      "deployment/api",
		Strategy:      "resources",
		PatchTemplate: "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        resources:\n          requests:\n            cpu: 100m\n",
		ApplyEligible: true,
		CanApply:      true,
	}
	opts := options{
		output:       "text",
		shadowVerify: false,
		delivery:     "patch",
		visited:      map[string]bool{"delivery": true},
		outFile:      t.TempDir() + "/p.yaml",
		promptInput:  bufio.NewReader(strings.NewReader("\n\n")),
	}
	var stdout, stderr bytes.Buffer
	code := runFixWalkthrough(context.Background(), &stdout, &stderr, opts, kube.Kubectl{}, finding, plan, "deployment/api")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "How do you want to deliver") {
		t.Fatalf("explicit delivery must skip the menu, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Delivering via patch (from --delivery)") {
		t.Fatalf("expected explicit delivery messaging, got %q", stdout.String())
	}
}

func TestWalkthroughReviewOnlyPatchFile(t *testing.T) {
	finding := analyzer.Finding{ResourceKind: "Service", ResourceName: "web", Summary: "selector mismatch"}
	// Non-apply-eligible plan with a concrete, safe review patch so
	// hasConcreteReviewPatch returns true (non-empty PatchTemplate, no TODO_ / leading #).
	plan := fix.Plan{
		Resource:       "service/web",
		Strategy:       "repair-selector",
		PatchTemplate:  "spec:\n  selector:\n    app: web\n",
		BlockedReasons: []string{"selector change is review-only"},
	}
	// Step 1 continue, then delivery choice 3 (patch file). delivery="patch"
	// (the CLI default, not visited) keeps this non-explicit so the menu runs.
	opts := options{
		output:      "text",
		delivery:    "patch",
		promptInput: bufio.NewReader(strings.NewReader("\n3\n")),
		outFile:     t.TempDir() + "/patch.yaml",
	}
	var stdout, stderr bytes.Buffer
	code := runFixWalkthrough(context.Background(), &stdout, &stderr, opts, kube.Kubectl{}, finding, plan, "service/web")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "review-only") {
		t.Fatalf("expected review-only messaging, got %q", stdout.String())
	}
}

func TestWalkthroughWithoutConcretePatchDoesNotOfferFix(t *testing.T) {
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "crashloop-demo", Namespace: "fixora-demo", Status: "CrashLoopBackOff", Evidence: []analyzer.Evidence{{Label: "Container command broken-app", Value: "sh -c exit 1"}}}
	p := fix.BuildPlan(f)
	var out, errout bytes.Buffer
	opts := options{output: "text", noColor: true, promptInput: bufio.NewReader(strings.NewReader("")), outFile: t.TempDir() + "/patch.yaml"}
	if code := runFixWalkthrough(context.Background(), &out, &errout, opts, kube.Kubectl{}, f, p, "Deployment/crashloop-demo"); code != 0 {
		t.Fatal(code)
	}
	for _, unwanted := range []string{"TODO_", "Continue to the proposed fix?", "Step 2/4", "+++ proposed/"} {
		if strings.Contains(out.String(), unwanted) {
			t.Fatalf("misleading output %q: %s", unwanted, out.String())
		}
	}
	if strings.Contains(out.String(), "Container command broken-app") || !strings.Contains(out.String(), "broken-app's intended startup behavior") || !strings.Contains(out.String(), "-n fixora-demo --proof") {
		t.Fatalf("default result should stay concise and provide a scoped details command: %s", out.String())
	}
	if _, err := os.Stat(opts.outFile); !os.IsNotExist(err) {
		t.Fatal("must not write a placeholder patch")
	}
}

func TestPreviewDoesNotRenderPlaceholderPatch(t *testing.T) {
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "prod", Status: "CrashLoopBackOff"}
	var out, errout bytes.Buffer
	runGuidedFix(context.Background(), &out, &errout, options{output: "text", preview: true, noColor: true}, kube.Kubectl{}, f, fix.BuildPlan(f), "Deployment/api")
	if !strings.Contains(out.String(), "-n prod --proof") {
		t.Fatalf("missing scoped next step: %s", out.String())
	}
	if strings.Contains(out.String(), "TODO_") || strings.Contains(out.String(), "Suggested diff") {
		t.Fatal(out.String())
	}
}

func TestPreviewWithRepairablePlanKeepsSummaryAndDiffOnlyByDefault(t *testing.T) {
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "dependency-demo", Namespace: "fixora-demo", Status: "DatabaseUnreachable", Severity: "critical", Summary: "The container cannot reach its database dependency.", AI: &analyzer.AIResult{Summary: "db-client cannot resolve DB_HOST and exits repeatedly.", RootCause: "Long AI explanation with evidence, alternatives, and detailed analysis.", RecommendedFix: "Read DB_HOST and DB_PORT from the existing connection ConfigMap; shadow verification must confirm that the client connects.", Commands: []string{"kubectl logs deployment/dependency-demo -n fixora-demo"}, Warnings: []string{"Check that the owning GitOps source references the approved ConfigMap."}}}
	plan := fix.Plan{Resource: "Deployment/dependency-demo", Namespace: "fixora-demo", Status: f.Status, Strategy: "env", ApplyEligible: true, Confidence: 90, Risk: "low", Steps: []string{"GENERIC_PLAN_STEP"}, Warnings: []string{"SPECIFIC_PLAN_WARNING"}, RollbackCommand: "kubectl rollout undo deployment/dependency-demo -n fixora-demo", PatchTemplate: "spec:\n  template:\n    spec:\n      containers:\n      - name: db-client\n        env:\n        - name: DB_HOST\n          valueFrom:\n            configMapKeyRef:\n              name: dependency-demo-connection\n              key: DB_HOST\n"}
	var out, errout bytes.Buffer
	opts := options{output: "text", preview: true, noColor: true}
	runGuidedFix(context.Background(), &out, &errout, opts, kube.Kubectl{}, f, plan, "Deployment/dependency-demo")
	for _, wanted := range []string{"DatabaseUnreachable", "db-client cannot resolve DB_HOST", "Read DB_HOST and DB_PORT", "--- current/Deployment/dependency-demo", "configMapKeyRef", "no changes made"} {
		if !strings.Contains(out.String(), wanted) {
			t.Fatalf("compact preview missing %q: %s", wanted, out.String())
		}
	}
	for _, unwanted := range []string{"Fixora incident fix", "Fixora why:", "Fixora plan for", "Long AI explanation", "kubectl logs deployment", "GENERIC_PLAN_STEP", "SPECIFIC_PLAN_WARNING", "Rollback hint", "Confidence: 90%"} {
		if strings.Contains(out.String(), unwanted) {
			t.Fatalf("default preview leaked detail %q: %s", unwanted, out.String())
		}
	}

	out.Reset()
	opts.proof = true
	runGuidedFix(context.Background(), &out, &errout, opts, kube.Kubectl{}, f, plan, "Deployment/dependency-demo")
	for _, wanted := range []string{"Long AI explanation", "kubectl logs deployment", "GENERIC_PLAN_STEP", "SPECIFIC_PLAN_WARNING", "Rollback:"} {
		if !strings.Contains(out.String(), wanted) {
			t.Fatalf("--proof preview missing detail %q: %s", wanted, out.String())
		}
	}
}

func TestUnresolvedFixDefaultOutputIsConcise(t *testing.T) {
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "api", Namespace: "prod", Status: "CrashLoopBackOff", Summary: "Container app exited with code 1 and is repeatedly restarting.", Evidence: []analyzer.Evidence{{Label: "Container command app", Value: "sh -c exit 1"}, {Label: "Exit code app", Value: "1"}, {Label: "Controller status", Value: "RAW_CONTROLLER_MAP"}, {Label: "Related analyzer finding 1", Value: "UNRELATED_WORKLOAD"}}, Logs: []analyzer.LogSnippet{{Source: "previous", Text: "Application failed"}}}
	for _, interactive := range []bool{true, false} {
		var out, errout bytes.Buffer
		opts := options{output: "text", noColor: true, preview: !interactive, promptInput: bufio.NewReader(strings.NewReader(""))}
		runGuidedFix(context.Background(), &out, &errout, opts, kube.Kubectl{}, f, fix.BuildPlan(f), "Deployment/api")
		for _, unwanted := range []string{"RAW_CONTROLLER_MAP", "UNRELATED_WORKLOAD", "Proof\n", "55%", "Rollback hint", "Fixora plan"} {
			if strings.Contains(out.String(), unwanted) {
				t.Fatalf("default output leaks details %q: %s", unwanted, out.String())
			}
		}
		for _, wanted := range []string{"Cause:", "app", "code 1", "startup behavior", "-n prod --proof"} {
			if !strings.Contains(out.String(), wanted) {
				t.Fatalf("missing actionable evidence %q: %s", wanted, out.String())
			}
		}
		if len(strings.Split(out.String(), "\n")) > 20 {
			t.Fatal("default diagnosis should fit on one screen")
		}
		out.Reset()
		opts.proof = true
		runGuidedFix(context.Background(), &out, &errout, opts, kube.Kubectl{}, f, fix.BuildPlan(f), "Deployment/api")
		if !strings.Contains(out.String(), "RAW_CONTROLLER_MAP") {
			t.Fatal("explicit --proof must retain full evidence")
		}
	}
}
