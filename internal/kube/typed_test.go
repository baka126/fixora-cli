package kube

import "testing"

func TestResourceCandidateHandlesShortcutsAndGroups(t *testing.T) {
	tests := map[string]string{
		"hpa":                                  "autoscaling/v2, Resource=horizontalpodautoscalers",
		"pdb":                                  "policy/v1, Resource=poddisruptionbudgets",
		"httproutes.gateway.networking.k8s.io": "gateway.networking.k8s.io/, Resource=httproutes",
		"pods":                                 "/, Resource=pods",
	}
	for input, want := range tests {
		if got := resourceCandidate(input).String(); got != want {
			t.Fatalf("resourceCandidate(%q) = %q, want %q", input, got, want)
		}
	}
}
