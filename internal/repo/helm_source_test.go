package repo

import "testing"

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
