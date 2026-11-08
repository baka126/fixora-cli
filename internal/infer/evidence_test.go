package infer

import (
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

func TestContainerNameFromEvidence(t *testing.T) {
	f := analyzer.Finding{Evidence: []analyzer.Evidence{
		{Label: "Pod phase", Value: "Running"},
		{Label: "Container image web-app", Value: "python:3.9-slim"},
	}}
	if got := ContainerName(f); got != "web-app" {
		t.Fatalf("want web-app, got %q", got)
	}
}

func TestContainerNameMissing(t *testing.T) {
	if got := ContainerName(analyzer.Finding{}); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestEvidenceValueByPrefix(t *testing.T) {
	f := analyzer.Finding{Evidence: []analyzer.Evidence{
		{Label: "Metrics pod containers", Value: "POD NAME CPU MEM"},
	}}
	if got := EvidenceValue(f, "Metrics pod containers"); got != "POD NAME CPU MEM" {
		t.Fatalf("unexpected value %q", got)
	}
	if got := EvidenceValue(f, "Nope"); got != "" {
		t.Fatalf("want empty for missing prefix, got %q", got)
	}
}

func TestContainerNamePrioritizesFailingContainer(t *testing.T) {
	f := analyzer.Finding{Evidence: []analyzer.Evidence{
		{Label: "Container image sidecar", Value: "busybox"},
		{Label: "Container image app", Value: "polinux/stress"},
		{Label: "Failing container", Value: "app"},
	}}
	if got := ContainerName(f); got != "app" {
		t.Fatalf("want app, got %q", got)
	}
	f.Evidence = append(f.Evidence, analyzer.Evidence{Label: "Failing container", Value: "sidecar"})
	if got := ContainerName(f); got != "" {
		t.Fatalf("conflicting target must decline, got %q", got)
	}
}
