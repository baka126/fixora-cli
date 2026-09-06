package infer

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

// maxNameDistance is how far a ConfigMap name may drift from the missing
// reference and still be treated as the intended target. Deliberately narrow:
// a wrong-but-plausible name is the failure shadow catches least reliably.
const maxNameDistance = 2

var missingRefPattern = regexp.MustCompile(`(?i)(configmap|secret)\s+"([^"]+)"\s+not found`)

func init() { register(envInferrer{}) }

type envInferrer struct{}

func (envInferrer) Name() string { return "env" }

func (envInferrer) Handles(plan fix.Plan) bool {
	return strings.EqualFold(plan.Strategy, "env")
}

func (envInferrer) Infer(ctx context.Context, r kubeReader, f analyzer.Finding, _ fix.Plan) (Result, bool, error) {
	if r == nil {
		return Result{}, false, nil
	}
	container := ContainerName(f)
	if container == "" {
		return Result{}, false, nil
	}
	kind, missing, ok := missingRef(f)
	if !ok {
		return Result{}, false, nil
	}
	// internal/shadow/clone.go blocks every pod that reads Secret env or
	// mounts a Secret volume, so a Secret-referencing patch could never be
	// shadow-verified — and this design lets nothing reach apply unverified.
	// This holds even when a near-match Secret exists: the remedy is
	// `kubectl create secret`, not a patch.
	if strings.EqualFold(kind, "secret") {
		return Result{}, false, nil
	}
	key := EvidenceValue(f, "Env reference key")
	if key == "" {
		return Result{}, false, nil
	}
	items, err := r.GetResourceItems(ctx, f.Namespace, false, "configmaps")
	if err != nil {
		return Result{}, false, err
	}
	match := ""
	for _, item := range items {
		name := configMapName(item)
		if name == "" || levenshtein(name, missing) > maxNameDistance {
			continue
		}
		if !configMapHasKey(item, key) {
			continue
		}
		if match != "" {
			// Two plausible matches is ambiguity, not a fix.
			return Result{}, false, nil
		}
		match = name
	}
	if match == "" {
		return Result{}, false, nil
	}
	return Result{
		Options: fix.ConcreteOptions{
			Container: container,
			EnvName:   EvidenceValue(f, "Env reference name"),
			ConfigMap: match,
			ConfigKey: key,
		},
		Guardrail: "inferred-env-from-namespace",
		Warning:   fmt.Sprintf("Fixora matched missing ConfigMap %q to existing %q by name similarity; confirm it is the intended source before delivery.", missing, match),
	}, true, nil
}

// missingRef pulls the kind and name of the absent object out of the kubelet
// event text.
func missingRef(f analyzer.Finding) (string, string, bool) {
	for _, evidence := range f.Evidence {
		if m := missingRefPattern.FindStringSubmatch(evidence.Value); m != nil {
			return m[1], m[2], true
		}
	}
	return "", "", false
}

func configMapName(item map[string]any) string {
	meta, _ := item["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	return name
}

func configMapHasKey(item map[string]any, key string) bool {
	data, _ := item["data"].(map[string]any)
	_, ok := data[key]
	return ok
}

// levenshtein is the standard edit distance, used only to spot a typo'd
// object name.
func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
