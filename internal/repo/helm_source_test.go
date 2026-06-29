package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

const sampleRender = `---
# Source: myapp/templates/serviceaccount.yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: rel-myapp
---
# Source: myapp/templates/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: rel-myapp
spec:
  replicas: 1
---
# Source: myapp/charts/redis/templates/statefulset.yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: rel-redis
`

func TestHelmSourceMatchesMainChart(t *testing.T) {
	got, ok := helmSourceMatches(sampleRender, "Deployment", "myapp", "rel")
	if !ok || got != "myapp/templates/deployment.yaml" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
}

func TestHelmSourceMatchesSubchart(t *testing.T) {
	got, ok := helmSourceMatches(sampleRender, "StatefulSet", "redis", "rel")
	if !ok || got != "myapp/charts/redis/templates/statefulset.yaml" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
}

func TestHelmSourceMatchesExactName(t *testing.T) {
	got, ok := helmSourceMatches(sampleRender, "ServiceAccount", "rel-myapp", "")
	if !ok || got != "myapp/templates/serviceaccount.yaml" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
}

func TestHelmSourceMatchesNoMatch(t *testing.T) {
	if _, ok := helmSourceMatches(sampleRender, "ConfigMap", "nope", "rel"); ok {
		t.Fatal("expected no match")
	}
}

// writeFixtureChart creates a minimal Helm chart tree under dir:
//
//	Chart.yaml (name: myapp)
//	values.yaml
//	values-prod.yaml
//	templates/deployment.yaml
//	charts/redis/values.yaml
func writeFixtureChart(t *testing.T, dir string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte("apiVersion: v2\nname: myapp\nversion: 0.1.0\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "values.yaml"), []byte("replicaCount: 1\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "values-prod.yaml"), []byte("replicaCount: 3\n"), 0o644))
	must(os.MkdirAll(filepath.Join(dir, "templates"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "templates", "deployment.yaml"), []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-myapp
spec:
  replicas: {{ .Values.replicaCount }}
`), 0o644))
	must(os.MkdirAll(filepath.Join(dir, "charts", "redis"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "charts", "redis", "Chart.yaml"), []byte("apiVersion: v2\nname: redis\nversion: 0.1.0\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "charts", "redis", "values.yaml"), []byte("image: redis:7\n"), 0o644))
}

func TestIdentifyHelmSourceEnumeratesValuesAndDegrades(t *testing.T) {
	dir := t.TempDir()
	writeFixtureChart(t, dir)
	t.Setenv("PATH", "") // helm absent -> deterministic degrade
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "myapp", Namespace: "prod"}
	f.GitOps.HelmRelease = "rel"
	loc, err := IdentifyHelmSource(dir, f)
	if err != nil {
		t.Fatal(err)
	}
	if loc.Release != "rel" || loc.Namespace != "prod" {
		t.Fatalf("release/ns: %+v", loc)
	}
	if len(loc.ValuesFiles) < 3 {
		t.Fatalf("expected >=3 values files, got %v", loc.ValuesFiles)
	}
	if loc.Pinpointed {
		t.Fatal("expected Pinpointed=false when helm absent")
	}
	if len(loc.Notes) == 0 {
		t.Fatal("expected a degrade note")
	}
}

func TestIdentifyHelmSourceEmptyRelease(t *testing.T) {
	dir := t.TempDir()
	writeFixtureChart(t, dir)
	t.Setenv("PATH", "")
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "myapp", Namespace: "default"}
	// GitOps.HelmRelease intentionally left empty
	loc, err := IdentifyHelmSource(dir, f)
	if err != nil {
		t.Fatal(err)
	}
	if loc.Release != "" {
		t.Fatalf("expected empty release, got %q", loc.Release)
	}
	found := false
	for _, n := range loc.Notes {
		if len(n) > 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected at least one note for empty release")
	}
}

func TestIdentifyHelmSourcePinpointEndToEnd(t *testing.T) {
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
		t.Fatalf("expected Pinpointed=true when helm is available; notes: %v", loc.Notes)
	}
	want := "myapp/templates/deployment.yaml"
	if loc.TemplateFile != want {
		t.Fatalf("TemplateFile: got %q, want %q", loc.TemplateFile, want)
	}
}
