package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

const aiProbeCandidate = `spec:
  template:
    spec:
      containers:
      - name: app
        readinessProbe:
          httpGet: {path: /ready, port: 9090}
          periodSeconds: 3
`

func TestAIRepairSelectsFieldsIndependentOfFailureCategory(t *testing.T) {
	_, r, f, p := startupFixture(t, validStartupBaseline)
	f.AI = &analyzer.AIResult{RootCause: "Application readiness endpoint uses port 9090.", PatchYAML: aiProbeCandidate, Confidence: 94}
	got := selectAIRepair(context.Background(), r, p, f, &bytes.Buffer{}, true)
	if !got.ApplyEligible || got.Strategy != "probe" || !strings.Contains(got.PatchYAML(), "9090") || !hasString(got.Guardrails, "shadow-required") {
		t.Fatalf("AI must generate a verified probe candidate from a runtime finding: %+v", got)
	}
}

func TestAIRepairConsidersCandidateWhenDeterministicRepairIsEligible(t *testing.T) {
	_, r, f, p := startupFixture(t, validStartupBaseline)
	p.Strategy = "resources"
	p.CanApply, p.ApplyEligible = true, true
	original := p.PatchYAML()
	p.PatchTemplate = original
	f.AI = &analyzer.AIResult{PatchYAML: aiProbeCandidate, Confidence: 96}
	got := selectAIRepair(context.Background(), r, p, f, &bytes.Buffer{}, false)
	if got.Strategy != "probe" || !strings.Contains(got.PatchYAML(), "9090") {
		t.Fatalf("AI was bypassed: %+v", got)
	}
}

func TestAIRepairRejectsUnknownLiveContainerWithoutUsingDeterministicPatch(t *testing.T) {
	_, r, f, p := startupFixture(t, validStartupBaseline)
	p.CanApply, p.ApplyEligible = true, true
	f.AI = &analyzer.AIResult{PatchYAML: strings.Replace(aiProbeCandidate, "name: app", "name: stranger", 1), Confidence: 99}
	got := selectAIRepair(context.Background(), r, p, f, &bytes.Buffer{}, false)
	if got.ApplyEligible || got.PatchYAML() != "" || len(got.Warnings) == 0 {
		t.Fatalf("invalid AI must not fall through to a code-defined patch: %+v", got)
	}
}

func TestNarrativeOnlyAIResultDoesNotUseDeterministicFilesystemPatch(t *testing.T) {
	f := analyzer.Finding{
		Namespace: "fixora-demo", ResourceKind: "Deployment", ResourceName: "security-demo", PodName: "security-demo-pod", Status: "PermissionDenied",
		AI: &analyzer.AIResult{Summary: "The cache write is denied.", RecommendedFix: "Add a writable cache mount and fsGroup."},
	}
	fallback := fix.WithEphemeralCachePatch(fix.BuildPlan(f), fix.EphemeralCache{Container: "restricted-app", Directory: "/var/cache", Group: 1000})
	got := selectAIRepair(context.Background(), nil, fallback, f, &bytes.Buffer{}, false)
	if got.ApplyEligible || got.PatchYAML() != "" || len(got.Warnings) == 0 {
		t.Fatalf("AI explanation without a validated patch must not reuse deterministic repair: %+v", got)
	}
}

func TestAIRepairDoesNotInventConfidenceOrOverrideRecovery(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		_, r, f, p := startupFixture(t, validStartupBaseline)
		f.Recovered = recovered
		f.AI = &analyzer.AIResult{PatchYAML: aiProbeCandidate, Confidence: 61}
		got := selectAIRepair(context.Background(), r, p, f, &bytes.Buffer{}, false)
		if got.ApplyEligible || (!recovered && got.Confidence != 61) {
			t.Fatalf("model confidence was promoted to authorization: %+v", got)
		}
	}
}

func TestAICommandProposalRemainsVisibleWithoutInventedBaseline(t *testing.T) {
	_, r, f, p := startupFixture(t, validStartupBaseline)
	f.AI = &analyzer.AIResult{PatchYAML: `spec:
  template:
    spec:
      containers:
      - name: app
        command: [httpd, -f, -p, "8080"]
`, Confidence: 95}
	got := selectAIRepair(context.Background(), r, p, f, &bytes.Buffer{}, false)
	if got.ApplyEligible || !strings.Contains(got.PatchYAML(), "httpd") || !hasString(got.Guardrails, "ai-patch-review-only") {
		t.Fatalf("command suggestion lost or silently authorized: %+v", got)
	}
}

func TestAIRepairPreservesExplicitStartupBaseline(t *testing.T) {
	path, r, f, p := startupFixture(t, validStartupBaseline)
	p, err := withStartupBaseline(context.Background(), r, path, f, p)
	if err != nil {
		t.Fatal(err)
	}
	f.AI = &analyzer.AIResult{PatchYAML: aiProbeCandidate, Confidence: 99}
	got := selectAIRepair(context.Background(), r, p, f, &bytes.Buffer{}, false)
	if got.Strategy != "restore-command" || got.PatchYAML() != p.PatchYAML() {
		t.Fatal("AI replaced explicit operator baseline")
	}
}

func TestAIWorkloadContextContainsProbesWithoutLiteralEnvironmentValues(t *testing.T) {
	_, r, f, _ := startupFixture(t, validStartupBaseline)
	podSpec := r.object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	c := podSpec["containers"].([]any)[0].(map[string]any)
	c["readinessProbe"] = map[string]any{"httpGet": map[string]any{"port": 9090, "path": "/ready"}}
	c["env"] = []any{map[string]any{"name": "CUSTOM_CREDENTIAL", "value": "never-send-this-value"}, map[string]any{"name": "MODE", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "settings", "key": "mode"}}}}
	got := withAIWorkloadContext(context.Background(), r, f)
	var contextValue string
	for _, e := range got.Evidence {
		if e.Label == "Live workload configuration" {
			contextValue = e.Value
		}
	}
	if !strings.Contains(contextValue, "readinessProbe") || !strings.Contains(contextValue, "settings") || strings.Contains(contextValue, "never-send-this-value") {
		t.Fatalf("incomplete or unsafe context: %s", contextValue)
	}
}

func TestAICandidateCannotBypassShadowAtDelivery(t *testing.T) {
	_, _, f, p := startupFixture(t, validStartupBaseline)
	p.Strategy = "probe"
	p.Guardrails = append(p.Guardrails, "shadow-required")
	var out, errs bytes.Buffer
	code := deliverVerifiedFix(context.Background(), &out, &errs, options{yes: true}, kube.Kubectl{}, f, p, shadow.Result{}, shadow.DeliveryCluster, false)
	if code == 0 || !strings.Contains(errs.String(), "shadow verification") {
		t.Fatalf("unverified AI delivery allowed: %d %s", code, errs.String())
	}
}

func TestAILiveConfigurationSurvivesEvidenceBounding(t *testing.T) {
	_, r, f, _ := startupFixture(t, validStartupBaseline)
	spec := r.object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	c := spec["containers"].([]any)[0].(map[string]any)
	c["args"] = []any{strings.Repeat("x", 2500)}
	got := withAIWorkloadContext(context.Background(), r, f)
	got.Evidence = boundEvidence(got.Evidence, 40)
	var decoded map[string]any
	if len(got.Evidence) == 0 || json.Unmarshal([]byte(got.Evidence[0].Value), &decoded) != nil {
		t.Fatal("bounded AI configuration must remain complete JSON")
	}
}

func TestAIRepairWorksWithoutDeterministicPatchTemplate(t *testing.T) {
	_, r, f, _ := startupFixture(t, validStartupBaseline)
	f.Status = "ApplicationInitializationFailure"
	f.AI = &analyzer.AIResult{PatchYAML: aiProbeCandidate, Confidence: 94}
	got := selectAIRepair(context.Background(), r, fix.BuildPlan(f), f, &bytes.Buffer{}, false)
	if !got.ApplyEligible || got.Strategy != "probe" {
		t.Fatalf("AI repair incorrectly depends on a known symptom template: %+v", got)
	}
}

func TestAISourceManagedRepairStaysReviewOnly(t *testing.T) {
	_, r, f, p := startupFixture(t, validStartupBaseline)
	f.GitOps.ManagedBy = "helm"
	f.AI = &analyzer.AIResult{PatchYAML: aiProbeCandidate, Confidence: 99}
	got := selectAIRepair(context.Background(), r, p, f, &bytes.Buffer{}, false)
	if got.Strategy != "probe" || got.ApplyEligible || !hasString(got.Guardrails, "ai-patch-review-only") {
		t.Fatalf("source-managed proposal was promoted to direct repair: %+v", got)
	}
}

type aiConfigReader struct {
	baselineReader
	config map[string]any
}

func (r aiConfigReader) GetResource(ctx context.Context, namespace, resource string) (map[string]any, error) {
	if resource == "configmap/settings" {
		return r.config, nil
	}
	return r.baselineReader.GetResource(ctx, namespace, resource)
}

func TestAIEnvironmentRepairChecksExistingConfigMapKeys(t *testing.T) {
	for _, keyExists := range []bool{true, false} {
		_, r, f, p := startupFixture(t, validStartupBaseline)
		data := map[string]any{}
		if keyExists {
			data["mode"] = "production"
		}
		reader := aiConfigReader{baselineReader: r, config: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "settings", "namespace": "demo"}, "data": data}}
		f.AI = &analyzer.AIResult{Confidence: 95, PatchYAML: `spec:
  template:
    spec:
      containers:
      - name: app
        env:
        - name: MODE
          value: null
          valueFrom:
            configMapKeyRef: {name: settings, key: mode}
`}
		got := selectAIRepair(context.Background(), reader, p, f, &bytes.Buffer{}, false)
		if got.ApplyEligible != keyExists {
			t.Fatalf("config key existence=%t, plan=%+v", keyExists, got)
		}
	}
}

func TestAICacheProposalCannotOverlayExistingStorage(t *testing.T) {
	_, r, f, p := startupFixture(t, validStartupBaseline)
	spec := r.object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	spec["volumes"] = []any{map[string]any{"name": "fixora-cache", "persistentVolumeClaim": map[string]any{"claimName": "important-data"}}}
	f.AI = &analyzer.AIResult{Confidence: 95, PatchYAML: `spec:
  template:
    spec:
      securityContext: {fsGroup: 1000}
      volumes:
      - name: fixora-cache
        emptyDir: {}
      containers:
      - name: app
        volumeMounts:
        - name: fixora-cache
          mountPath: /var/cache
`}
	got := selectAIRepair(context.Background(), r, p, f, &bytes.Buffer{}, false)
	if got.ApplyEligible || !hasString(got.Guardrails, "ai-patch-review-only") {
		t.Fatalf("cache candidate overlaid existing storage: %+v", got)
	}
}

func TestAIWorkloadSpecificFilesystemRepairPreservesExistingStorage(t *testing.T) {
	_, r, f, p := startupFixture(t, validStartupBaseline)
	spec := r.object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	spec["volumes"] = []any{map[string]any{"name": "app-data", "emptyDir": map[string]any{}}}
	container := spec["containers"].([]any)[0].(map[string]any)
	container["volumeMounts"] = []any{map[string]any{"name": "app-data", "mountPath": "/srv/data"}}
	f.AI = &analyzer.AIResult{Confidence: 96, PatchYAML: `spec:
  template:
    spec:
      securityContext: {fsGroup: 2001}
      volumes:
      - name: app-runtime-cache
        emptyDir: {}
      containers:
      - name: app
        volumeMounts:
        - name: app-runtime-cache
          mountPath: /opt/nebula/cache
`}
	got := selectAIRepair(context.Background(), r, p, f, &bytes.Buffer{}, false)
	if !got.ApplyEligible || got.Strategy != "filesystem" || !strings.Contains(got.PatchYAML(), "app-runtime-cache") {
		t.Fatalf("AI should accept a unique workload-specific cache mount while preserving existing storage: %+v", got)
	}
}

func TestAINonTemplateProposalRemainsReviewOnlyInsteadOfUsingDeterministicRepair(t *testing.T) {
	for _, patch := range []string{"spec: {replicas: 2}", "metadata: {labels: {app: changed}}"} {
		_, r, f, p := startupFixture(t, validStartupBaseline)
		p.CanApply, p.ApplyEligible = true, true
		f.AI = &analyzer.AIResult{PatchYAML: patch, Confidence: 99}
		got := selectAIRepair(context.Background(), r, p, f, &bytes.Buffer{}, false)
		if got.ApplyEligible || got.PatchYAML() == p.PatchYAML() {
			t.Fatalf("non-template AI proposal must remain review-only instead of using a deterministic repair: %+v", got)
		}
		if strings.Contains(patch, "replicas") && (!strings.Contains(got.PatchYAML(), "replicas") || !hasString(got.Guardrails, "ai-patch-review-only")) {
			t.Fatalf("reviewable AI source proposal should remain visible for review: %+v", got)
		}
	}
}

func TestAISourceReviewKeepsConfidenceAndOwnershipBlockers(t *testing.T) {
	for _, bad := range []string{"confidence", "ownership"} {
		t.Run(bad, func(t *testing.T) {
			_, r, f, p := startupFixture(t, validStartupBaseline)
			f.GitOps.ManagedBy = "helm"
			f.AI = &analyzer.AIResult{PatchYAML: aiProbeCandidate, Confidence: 99}
			if bad == "confidence" {
				f.AI.Confidence = 70
			} else {
				p.Guardrails = append(p.Guardrails, "ownership-unverified")
			}
			got := selectAIRepair(context.Background(), r, p, f, &bytes.Buffer{}, false)
			if got.ApplyEligible || fix.SourceReviewAllowed(got) {
				t.Fatal("source policy blocker lost", got)
			}
		})
	}
}
