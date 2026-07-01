package repo

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

const renderedDocSample = `---
# Source: myapp/templates/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: rel-myapp
spec:
  replicas: 1
  paused: false
---
# Source: myapp/charts/redis/templates/statefulset.yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: rel-redis
spec:
  replicas: 2
`

func TestRenderedDocForMainChart(t *testing.T) {
	body, ok := renderedDocFor(renderedDocSample, "Deployment", "myapp", "rel")
	if !ok {
		t.Fatal("expected a match")
	}
	if !strings.Contains(body, "replicas: 1") || !strings.Contains(body, "paused: false") {
		t.Fatalf("body missing expected fields: %q", body)
	}
	if strings.Contains(body, "kind: StatefulSet") {
		t.Fatalf("body leaked the next document: %q", body)
	}
}

func TestRenderedDocForSubchart(t *testing.T) {
	body, ok := renderedDocFor(renderedDocSample, "StatefulSet", "redis", "rel")
	if !ok || !strings.Contains(body, "replicas: 2") {
		t.Fatalf("subchart doc not matched: ok=%v body=%q", ok, body)
	}
}

func TestRenderedDocForNoMatch(t *testing.T) {
	if _, ok := renderedDocFor(renderedDocSample, "ConfigMap", "nope", "rel"); ok {
		t.Fatal("expected no match")
	}
}

func TestClassifyPatchThreeWay(t *testing.T) {
	patch := map[string]any{
		"spec": map[string]any{
			"replicas": 3,    // rendered=1 -> managed-divergent
			"paused":   true, // absent in rendered -> unmanaged
		},
	}
	rendered := map[string]any{
		"spec": map[string]any{
			"replicas": 1,
		},
	}
	got := map[string]string{}
	for _, v := range classifyPatch(patch, rendered, "Deployment") {
		got[v.Path] = v.Class
	}
	if got["spec.replicas"] != "managed-divergent" {
		t.Fatalf("spec.replicas: got %q", got["spec.replicas"])
	}
	if got["spec.paused"] != "unmanaged" {
		t.Fatalf("spec.paused: got %q", got["spec.paused"])
	}
}

func TestClassifyPatchManagedMatch(t *testing.T) {
	patch := map[string]any{"spec": map[string]any{"replicas": 3}}
	rendered := map[string]any{"spec": map[string]any{"replicas": 3}}
	v := classifyPatch(patch, rendered, "Deployment")
	if len(v) != 1 || v[0].Class != "managed-match" {
		t.Fatalf("expected one managed-match, got %#v", v)
	}
	if v[0].RenderedValue != "3" || v[0].IntendedValue != "3" {
		t.Fatalf("non-secret verdict must carry values, got %#v", v[0])
	}
}

func TestClassifyPatchSecretRedaction(t *testing.T) {
	patch := map[string]any{"data": map[string]any{"password": "aGVsbG8="}}
	rendered := map[string]any{"data": map[string]any{"password": "d29ybGQ="}}
	v := classifyPatch(patch, rendered, "Secret")
	if len(v) != 1 || v[0].Class != "managed-divergent" {
		t.Fatalf("expected one managed-divergent, got %#v", v)
	}
	if v[0].RenderedValue != "" || v[0].IntendedValue != "" {
		t.Fatalf("Secret verdict must omit values, got %#v", v[0])
	}
}

func TestValidateAgainstRenderNoPatch(t *testing.T) {
	loc := HelmSourceLocation{Pinpointed: true, ChartPath: t.TempDir(), Release: "rel"}
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "myapp"}
	rv := ValidateAgainstRender(loc, f, "   ")
	if len(rv.Fields) != 0 || len(rv.Notes) == 0 {
		t.Fatalf("empty patch must degrade to a note, got %#v", rv)
	}
}

func TestValidateAgainstRenderNotPinpointed(t *testing.T) {
	loc := HelmSourceLocation{Pinpointed: false}
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "myapp"}
	rv := ValidateAgainstRender(loc, f, "spec:\n  replicas: 3\n")
	if len(rv.Fields) != 0 || len(rv.Notes) == 0 {
		t.Fatalf("un-pinpointed loc must degrade to a note, got %#v", rv)
	}
}

func TestValidateAgainstRenderMultiDoc(t *testing.T) {
	loc := HelmSourceLocation{Pinpointed: true, ChartPath: t.TempDir(), Release: "rel"}
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "myapp"}
	rv := ValidateAgainstRender(loc, f, "spec:\n  replicas: 3\n---\nkind: Service\n")
	if len(rv.Fields) != 0 || len(rv.Notes) == 0 {
		t.Fatalf("multi-document patch must degrade to a note with no fields, got %#v", rv)
	}
}

func TestValidateAgainstRenderDegradesWithoutHelm(t *testing.T) {
	loc := HelmSourceLocation{Pinpointed: true, ChartPath: t.TempDir(), Release: "rel"}
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "myapp"}
	t.Setenv("PATH", "")
	rv := ValidateAgainstRender(loc, f, "spec:\n  replicas: 3\n")
	if len(rv.Fields) != 0 {
		t.Fatalf("no fields expected when helm absent, got %#v", rv.Fields)
	}
	if len(rv.Notes) == 0 {
		t.Fatal("expected a degrade note when helm absent")
	}
}

func TestValidateAgainstRenderEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed")
	}
	dir := t.TempDir()
	writeFixtureChart(t, dir)
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "myapp", Namespace: "default"}
	f.GitOps.HelmRelease = "rel"
	loc, err := IdentifyHelmSource(dir, f)
	if err != nil {
		t.Fatal(err)
	}
	if !loc.Pinpointed {
		t.Fatalf("fixture should pinpoint; notes: %v", loc.Notes)
	}
	// Fixture renders replicas from .Values.replicaCount (=1). Patch to 3.
	rv := ValidateAgainstRender(loc, f, "spec:\n  replicas: 3\n")
	var class string
	for _, v := range rv.Fields {
		if v.Path == "spec.replicas" {
			class = v.Class
		}
	}
	if class != "managed-divergent" {
		t.Fatalf("spec.replicas should be managed-divergent (rendered 1 vs patch 3), got %q; fields=%#v notes=%v", class, rv.Fields, rv.Notes)
	}
}

func TestPreviewSourcePatchAttachesRenderValidation(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed")
	}
	dir := t.TempDir()
	writeFixtureChart(t, dir)
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "myapp", Namespace: "default"}
	f.GitOps.HelmRelease = "rel"

	plan := fix.Plan{PatchTemplate: "spec:\n  replicas: 3\n"}
	result, err := PreviewSourcePatch(dir, "", f, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.RenderValidation == nil {
		t.Fatal("expected RenderValidation to be attached for a pinpointed Helm source")
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "spec.replicas") && strings.Contains(w, "reverted") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a managed-divergent warning for spec.replicas, warnings=%v", result.Warnings)
	}
}
