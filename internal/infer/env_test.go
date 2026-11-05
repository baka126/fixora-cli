package infer

import (
	"context"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

type fakeConfigReader struct {
	kubeReader
	items []map[string]any
}

func (f fakeConfigReader) GetResourceItems(_ context.Context, _ string, _ bool, _ string) ([]map[string]any, error) {
	return f.items, nil
}

func configMap(name string, keys ...string) map[string]any {
	data := map[string]any{}
	for _, k := range keys {
		data[k] = "v"
	}
	return map[string]any{
		"metadata": map[string]any{"name": name},
		"data":     data,
	}
}

// envFinding mirrors what internal/analyzer's envRefEvidence really attaches
// for a CreateContainerConfigError pod: the env var name and key, plus the
// referenced object's kind and name. No hand-injected event text — the real
// analyzer filters kubelet events that lack the pod name.
func envFinding(refKind, refName string) analyzer.Finding {
	return analyzer.Finding{
		Status: "CreateContainerConfigError",
		Evidence: []analyzer.Evidence{
			{Label: "Container image config-consumer", Value: "busybox:1.36"},
			{Label: "Env reference name", Value: "REQUIRED_ENV"},
			{Label: "Env reference key", Value: "some-key"},
			{Label: "Env reference kind", Value: refKind},
			{Label: "Env reference object", Value: refName},
		},
	}
}

func TestEnvInferrerCorrectsTypoedConfigMap(t *testing.T) {
	f := envFinding("ConfigMap", "app-confg")
	r := fakeConfigReader{items: []map[string]any{configMap("app-config", "some-key")}}
	res, ok, err := envInferrer{}.Infer(context.Background(), r, f, fix.Plan{Strategy: "env"})
	if err != nil || !ok {
		t.Fatalf("want accept, got ok=%v err=%v", ok, err)
	}
	if res.Options.Container != "config-consumer" {
		t.Fatalf("want config-consumer, got %q", res.Options.Container)
	}
	if res.Options.EnvName != "REQUIRED_ENV" {
		t.Fatalf("want REQUIRED_ENV, got %q", res.Options.EnvName)
	}
	if res.Options.ConfigMap != "app-config" {
		t.Fatalf("want app-config, got %q", res.Options.ConfigMap)
	}
	if res.Options.ConfigKey != "some-key" {
		t.Fatalf("want some-key, got %q", res.Options.ConfigKey)
	}
}

func TestEnvInferrerRefusesSecrets(t *testing.T) {
	// shadow/clone.go blocks every Secret-referencing pod, so a Secret patch
	// can never be shadow-verified and must never reach apply — even when a
	// near-match Secret exists in the namespace.
	f := envFinding("Secret", "app-secret")
	r := fakeConfigReader{items: []map[string]any{configMap("app-secret", "some-key")}}
	if _, ok, _ := (envInferrer{}).Infer(context.Background(), r, f, fix.Plan{Strategy: "env"}); ok {
		t.Fatal("Secret references must always be declined")
	}
}

func TestEnvInferrerDeclinesOnAmbiguity(t *testing.T) {
	f := envFinding("ConfigMap", "app-confg")
	r := fakeConfigReader{items: []map[string]any{
		configMap("app-config", "some-key"),
		configMap("app-confug", "some-key"),
	}}
	if _, ok, _ := (envInferrer{}).Infer(context.Background(), r, f, fix.Plan{Strategy: "env"}); ok {
		t.Fatal("two plausible matches is ambiguity, not a fix")
	}
}

func TestEnvInferrerDeclinesWhenKeyAbsent(t *testing.T) {
	f := envFinding("ConfigMap", "app-confg")
	r := fakeConfigReader{items: []map[string]any{configMap("app-config", "other-key")}}
	if _, ok, _ := (envInferrer{}).Infer(context.Background(), r, f, fix.Plan{Strategy: "env"}); ok {
		t.Fatal("candidate without the referenced key must be declined")
	}
}

func TestEnvInferrerDeclinesWhenNothingSimilar(t *testing.T) {
	f := envFinding("ConfigMap", "fixora-non-existent")
	r := fakeConfigReader{items: []map[string]any{configMap("totally-different", "some-key")}}
	if _, ok, _ := (envInferrer{}).Infer(context.Background(), r, f, fix.Plan{Strategy: "env"}); ok {
		t.Fatal("no near match must decline")
	}
}

func TestEnvInferrerDeclinesOnSelfMatch(t *testing.T) {
	// The referenced name equals an existing ConfigMap: the reference already
	// resolves, so a distance-0 self-match would emit a no-op patch that
	// passes shadow while fixing nothing.
	f := envFinding("ConfigMap", "app-config")
	r := fakeConfigReader{items: []map[string]any{configMap("app-config", "some-key")}}
	if _, ok, _ := (envInferrer{}).Infer(context.Background(), r, f, fix.Plan{Strategy: "env"}); ok {
		t.Fatal("a name equal to the reference must decline, not self-match")
	}
}

func TestEnvInferrerDeclinesWithoutKeyEvidence(t *testing.T) {
	f := analyzer.Finding{
		Status: "CreateContainerConfigError",
		Evidence: []analyzer.Evidence{
			{Label: "Container image config-consumer", Value: "busybox:1.36"},
			{Label: "Env reference name", Value: "REQUIRED_ENV"},
			{Label: "Env reference kind", Value: "ConfigMap"},
			{Label: "Env reference object", Value: "app-confg"},
		},
	}
	r := fakeConfigReader{items: []map[string]any{configMap("app-config", "some-key")}}
	if _, ok, _ := (envInferrer{}).Infer(context.Background(), r, f, fix.Plan{Strategy: "env"}); ok {
		t.Fatal("no env-key evidence must decline")
	}
}

func TestEnvInferrerDeclinesWithoutReferenceEvidence(t *testing.T) {
	// Without the analyzer's kind/object evidence there is nothing to match.
	f := analyzer.Finding{
		Status: "CreateContainerConfigError",
		Evidence: []analyzer.Evidence{
			{Label: "Container image config-consumer", Value: "busybox:1.36"},
			{Label: "Env reference name", Value: "REQUIRED_ENV"},
			{Label: "Env reference key", Value: "some-key"},
		},
	}
	r := fakeConfigReader{items: []map[string]any{configMap("app-config", "some-key")}}
	if _, ok, _ := (envInferrer{}).Infer(context.Background(), r, f, fix.Plan{Strategy: "env"}); ok {
		t.Fatal("no reference kind/object evidence must decline")
	}
}

func TestLevenshtein(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{{"app-config", "app-confg", 1}, {"a", "a", 0}, {"abc", "xyz", 3}} {
		if got := levenshtein(tc.a, tc.b); got != tc.want {
			t.Fatalf("levenshtein(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestEnvInferrerConcretePlanIsApplyEligible builds a real
// CreateContainerConfigError finding, runs it through fix.BuildPlan then
// infer.Concrete, and asserts the concretized plan comes back ApplyEligible
// with every one of the four env placeholders — TODO_CONTAINER_NAME,
// TODO_ENV_NAME, TODO_CONFIGMAP, TODO_KEY — filled. A branch that leaves any
// unfilled silently fails Concretize's validation gate and concreteWith
// discards the whole Result.
func TestEnvInferrerConcretePlanIsApplyEligible(t *testing.T) {
	f := analyzer.Finding{
		ResourceKind: "Deployment", ResourceName: "missing-config-demo", Namespace: "default",
		Status: "CreateContainerConfigError",
		Evidence: []analyzer.Evidence{
			{Label: "Container image config-consumer", Value: "busybox:1.36"},
			{Label: "Env reference name", Value: "REQUIRED_ENV"},
			{Label: "Env reference key", Value: "some-key"},
			{Label: "Env reference kind", Value: "ConfigMap"},
			{Label: "Env reference object", Value: "app-confg"},
		},
	}
	r := fakeConfigReader{items: []map[string]any{configMap("app-config", "some-key")}}
	plan := fix.BuildPlan(f)
	got, ok := Concrete(context.Background(), r, f, plan)
	if !ok {
		t.Fatalf("want a concretized plan, got ok=false (blocked: %v)", got.BlockedReasons)
	}
	if !got.ApplyEligible {
		t.Fatalf("want ApplyEligible, got false (blocked: %v)", got.BlockedReasons)
	}
	if strings.Contains(got.PatchTemplate, "TODO_") {
		t.Fatalf("patch still has an unsubstituted placeholder:\n%s", got.PatchTemplate)
	}
}
