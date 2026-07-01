package repo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTemplateDiskPath(t *testing.T) {
	got := templateDiskPath("/charts/myapp", "myapp/templates/deployment.yaml")
	want := filepath.FromSlash("/charts/myapp/templates/deployment.yaml")
	if got != want {
		t.Fatalf("main chart: got %q want %q", got, want)
	}
	got = templateDiskPath("/charts/myapp", "myapp/charts/redis/templates/ss.yaml")
	want = filepath.FromSlash("/charts/myapp/charts/redis/templates/ss.yaml")
	if got != want {
		t.Fatalf("subchart: got %q want %q", got, want)
	}
}

func TestValuesRefsInTemplate(t *testing.T) {
	text := "replicas: {{ .Values.replicaCount }}\nimage: {{ .Values.image.repo }}:{{ .Values.image.tag }}\nx: {{ .Values.replicaCount }}\n"
	refs := valuesRefsInTemplate(text)
	// de-duplicated, stable order of first appearance
	want := []string{"replicaCount", "image.repo", "image.tag"}
	if len(refs) != len(want) {
		t.Fatalf("got %v want %v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("got %v want %v", refs, want)
		}
	}
}

func TestTemplateLineForField(t *testing.T) {
	text := "spec:\n  replicas: {{ .Values.replicaCount }}\n  paused: false\n"
	refs, found := templateLineForField(text, "replicas")
	if !found || len(refs) != 1 || refs[0] != "replicaCount" {
		t.Fatalf("replicas: found=%v refs=%v", found, refs)
	}
	refs, found = templateLineForField(text, "paused")
	if !found || len(refs) != 0 {
		t.Fatalf("paused should be found with no refs: found=%v refs=%v", found, refs)
	}
	if _, found := templateLineForField(text, "missing"); found {
		t.Fatal("missing key should not be found")
	}
}

func TestValuesFileLookup(t *testing.T) {
	dir := t.TempDir()
	vf := filepath.Join(dir, "values.yaml")
	if err := os.WriteFile(vf, []byte("replicaCount: 1\nimage:\n  repo: nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, ok := valuesFileLookup([]string{vf}, "replicaCount"); !ok || v != "1" {
		t.Fatalf("replicaCount: v=%q ok=%v", v, ok)
	}
	if v, ok := valuesFileLookup([]string{vf}, "image.repo"); !ok || v != "nginx" {
		t.Fatalf("image.repo: v=%q ok=%v", v, ok)
	}
	if _, ok := valuesFileLookup([]string{vf}, "nope"); ok {
		t.Fatal("missing key should not resolve")
	}
	// a key that resolves to a map (not a scalar) does not resolve
	if _, ok := valuesFileLookup([]string{vf}, "image"); ok {
		t.Fatal("map-valued key should not resolve as a scalar")
	}
}
