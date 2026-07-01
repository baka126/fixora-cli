package repo

import (
	"strings"
	"testing"
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
