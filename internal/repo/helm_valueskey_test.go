package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
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

// writeVKChart writes a minimal chart used by the SuggestValuesKeys tier tests.
func writeVKChart(t *testing.T) (chartPath, templateSource string, valuesFiles []string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	tmpl := "spec:\n" +
		"  replicas: {{ .Values.replicaCount }}\n" +
		"  image: {{ .Values.img }}\n" +
		"  host: {{ .Values.a }}-{{ .Values.b }}\n"
	if err := os.WriteFile(filepath.Join(dir, "templates", "deployment.yaml"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	vf := filepath.Join(dir, "values.yaml")
	if err := os.WriteFile(vf, []byte("replicaCount: 1\nimg: nginx\na: x\nb: y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, "myapp/templates/deployment.yaml", []string{vf}
}

func suggestionFor(sugs []ValuesKeySuggestion, path string) (ValuesKeySuggestion, bool) {
	for _, s := range sugs {
		if s.FieldPath == path {
			return s, true
		}
	}
	return ValuesKeySuggestion{}, false
}

func TestSuggestValuesKeysPinpointed(t *testing.T) {
	chartPath, src, vfs := writeVKChart(t)
	loc := HelmSourceLocation{ChartPath: chartPath, TemplateFile: src, ValuesFiles: vfs}
	rv := RenderValidation{Fields: []FieldVerdict{
		{Path: "spec.replicas", Class: "managed-divergent", RenderedValue: "1", IntendedValue: "3"},
	}}
	s, ok := suggestionFor(SuggestValuesKeys(loc, rv), "spec.replicas")
	if !ok || s.Confidence != "pinpointed" || len(s.Candidates) != 1 || s.Candidates[0] != "replicaCount" {
		t.Fatalf("got %#v", s)
	}
}

func TestSuggestValuesKeysUncertainMultiRef(t *testing.T) {
	chartPath, src, vfs := writeVKChart(t)
	loc := HelmSourceLocation{ChartPath: chartPath, TemplateFile: src, ValuesFiles: vfs}
	// host line references two keys; rendered "x-y" matches neither a nor b alone.
	rv := RenderValidation{Fields: []FieldVerdict{
		{Path: "spec.host", Class: "managed-divergent", RenderedValue: "x-y", IntendedValue: "z-w"},
	}}
	s, _ := suggestionFor(SuggestValuesKeys(loc, rv), "spec.host")
	if s.Confidence != "uncertain" || len(s.Candidates) != 2 {
		t.Fatalf("got %#v", s)
	}
}

func TestSuggestValuesKeysLikelyByValueMatch(t *testing.T) {
	chartPath, src, vfs := writeVKChart(t)
	loc := HelmSourceLocation{ChartPath: chartPath, TemplateFile: src, ValuesFiles: vfs}
	// "foo" has no template line; rendered "nginx" value-matches only img.
	rv := RenderValidation{Fields: []FieldVerdict{
		{Path: "spec.foo", Class: "managed-divergent", RenderedValue: "nginx", IntendedValue: "apache"},
	}}
	s, _ := suggestionFor(SuggestValuesKeys(loc, rv), "spec.foo")
	if s.Confidence != "likely" || len(s.Candidates) != 1 || s.Candidates[0] != "img" {
		t.Fatalf("got %#v", s)
	}
}

func TestSuggestValuesKeysUnmapped(t *testing.T) {
	chartPath, vfs := t.TempDir(), []string{}
	// TemplateFile points at a missing file -> no refs at all.
	loc := HelmSourceLocation{ChartPath: chartPath, TemplateFile: "myapp/templates/missing.yaml", ValuesFiles: vfs}
	rv := RenderValidation{Fields: []FieldVerdict{
		{Path: "spec.replicas", Class: "managed-divergent", RenderedValue: "1", IntendedValue: "3"},
	}}
	s, _ := suggestionFor(SuggestValuesKeys(loc, rv), "spec.replicas")
	if s.Confidence != "unmapped" || len(s.Candidates) != 0 || s.Note == "" {
		t.Fatalf("got %#v", s)
	}
}

func TestSuggestValuesKeysSkipsNonDivergent(t *testing.T) {
	chartPath, src, vfs := writeVKChart(t)
	loc := HelmSourceLocation{ChartPath: chartPath, TemplateFile: src, ValuesFiles: vfs}
	rv := RenderValidation{Fields: []FieldVerdict{
		{Path: "spec.replicas", Class: "managed-match", RenderedValue: "1", IntendedValue: "1"},
		{Path: "spec.img", Class: "unmanaged", IntendedValue: "nginx"},
	}}
	if got := SuggestValuesKeys(loc, rv); len(got) != 0 {
		t.Fatalf("only managed-divergent fields get suggestions, got %#v", got)
	}
}

func TestSuggestValuesKeysSecretNoValueLeak(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates", "secret.yaml"),
		[]byte("data:\n  password: {{ .Values.secretPass }}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vf := filepath.Join(dir, "values.yaml")
	if err := os.WriteFile(vf, []byte("secretPass: s3cr3tVALUE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loc := HelmSourceLocation{ChartPath: dir, TemplateFile: "myapp/templates/secret.yaml", ValuesFiles: []string{vf}}
	// #13b redacts RenderedValue to "" for Secret findings.
	rv := RenderValidation{Fields: []FieldVerdict{
		{Path: "data.password", Class: "managed-divergent", RenderedValue: "", IntendedValue: ""},
	}}
	s, _ := suggestionFor(SuggestValuesKeys(loc, rv), "data.password")
	if s.Confidence != "pinpointed" || len(s.Candidates) != 1 || s.Candidates[0] != "secretPass" {
		t.Fatalf("got %#v", s)
	}
	if strings.Contains(s.Note, "s3cr3tVALUE") || strings.Contains(strings.Join(s.Candidates, ","), "s3cr3tVALUE") {
		t.Fatalf("values-file value leaked into suggestion: %#v", s)
	}
}

func TestPreviewSourcePatchAttachesSuggestions(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed")
	}
	dir := t.TempDir()
	writeFixtureChart(t, dir) // defined in helm_source_test.go: renders spec.replicas from .Values.replicaCount (=1)
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "myapp", Namespace: "default"}
	f.GitOps.HelmRelease = "rel"

	plan := fix.Plan{PatchTemplate: "spec:\n  replicas: 3\n"}
	result, err := PreviewSourcePatch(dir, "", f, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.RenderValidation == nil || len(result.RenderValidation.Suggestions) == 0 {
		t.Fatalf("expected suggestions attached, got %#v", result.RenderValidation)
	}
	foundSuggestion := false
	for _, s := range result.RenderValidation.Suggestions {
		if s.FieldPath == "spec.replicas" && s.Confidence == "pinpointed" && len(s.Candidates) == 1 && s.Candidates[0] == "replicaCount" {
			foundSuggestion = true
		}
	}
	if !foundSuggestion {
		t.Fatalf("expected pinpointed replicaCount suggestion, got %#v", result.RenderValidation.Suggestions)
	}
	foundWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "spec.replicas") && strings.Contains(w, "replicaCount") && strings.Contains(w, "pinpointed") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("expected a values-key warning, warnings=%v", result.Warnings)
	}
}
