package analyzer

import (
	"context"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

// makeDS builds a minimal daemonset fixture for analyzer tests.
func makeDS(namespace, name string, desired int, tolerations []any, nodeSelector map[string]any) map[string]any {
	spec := map[string]any{
		"template": map[string]any{
			"spec": map[string]any{},
		},
	}
	if len(tolerations) > 0 {
		spec["template"].(map[string]any)["spec"].(map[string]any)["tolerations"] = tolerations
	}
	if len(nodeSelector) > 0 {
		spec["template"].(map[string]any)["spec"].(map[string]any)["nodeSelector"] = nodeSelector
	}
	return map[string]any{
		"metadata": map[string]any{"namespace": namespace, "name": name},
		"spec":     spec,
		"status": map[string]any{
			"desiredNumberScheduled": desired,
			"numberReady":            desired, // healthy — NotReady won't fire
		},
	}
}

func makeNode(name string, labels map[string]string, taints []map[string]any) kube.Node {
	return kube.Node{
		Metadata: kube.ObjectMeta{Name: name, Labels: labels},
		Spec: struct {
			ProviderID string           `json:"providerID"`
			Taints     []map[string]any `json:"taints"`
		}{Taints: taints},
	}
}

// TestDaemonSetUnderScheduledTaintGap: 3 nodes total, 1 with NoSchedule taint DS doesn't tolerate,
// desiredNumberScheduled=2 → DaemonSetUnderScheduled finding.
func TestDaemonSetUnderScheduledTaintGap(t *testing.T) {
	ds := makeDS("prod", "log-collector", 2, nil, nil)
	nodes := []kube.Node{
		makeNode("node-1", map[string]string{"kubernetes.io/arch": "amd64"}, nil),
		makeNode("node-2", map[string]string{"kubernetes.io/arch": "amd64"}, nil),
		makeNode("node-3", map[string]string{"kubernetes.io/arch": "amd64"}, []map[string]any{
			{"key": "dedicated", "effect": "NoSchedule", "value": "gpu"},
		}),
	}

	reader := fakeReader{
		items: map[string][]map[string]any{"daemonsets": {ds}},
		nodes: nodes,
	}
	report := New(reader, Options{Namespace: "prod"}).ScanReport(context.Background())

	var found *Finding
	for i := range report.Findings {
		if report.Findings[i].Status == "DaemonSetUnderScheduled" {
			found = &report.Findings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected DaemonSetUnderScheduled finding, got %#v", report.Findings)
	}
	if found.Severity != "medium" {
		t.Fatalf("expected medium severity, got %q", found.Severity)
	}
	// Evidence should mention the taint key.
	var evidenceText string
	for _, e := range found.Evidence {
		evidenceText += e.Value + " "
	}
	if !strings.Contains(evidenceText, "dedicated") {
		t.Fatalf("expected taint key 'dedicated' in evidence, got %q", evidenceText)
	}
}

// TestDaemonSetUnderScheduledHonorsNodeSelector verifies a DaemonSet pinned to
// a subset of nodes is compared only against nodes matching that selector.
func TestDaemonSetUnderScheduledHonorsNodeSelector(t *testing.T) {
	ds := makeDS("prod", "gpu-agent", 1, nil, map[string]any{"pool": "gpu"})
	nodes := []kube.Node{
		makeNode("gpu-1", map[string]string{"pool": "gpu", "kubernetes.io/arch": "amd64"}, nil),
		makeNode("general-1", map[string]string{"pool": "general", "kubernetes.io/arch": "amd64"}, []map[string]any{
			{"key": "dedicated", "effect": "NoSchedule", "value": "general"},
		}),
	}

	reader := fakeReader{
		items: map[string][]map[string]any{"daemonsets": {ds}},
		nodes: nodes,
	}
	report := New(reader, Options{Namespace: "prod"}).ScanReport(context.Background())

	for _, f := range report.Findings {
		if f.Status == "DaemonSetUnderScheduled" {
			t.Fatalf("nodeSelector-pinned DaemonSet should ignore non-target nodes, got %#v", f)
		}
	}
}

// TestDaemonSetUnderScheduledEqualTolerationRequiresValueMatch verifies Equal
// tolerations do not mask taints with the same key/effect but a different value.
func TestDaemonSetUnderScheduledEqualTolerationRequiresValueMatch(t *testing.T) {
	ds := makeDS("prod", "infra-agent", 1,
		[]any{map[string]any{"key": "dedicated", "effect": "NoSchedule", "operator": "Equal", "value": "infra"}},
		nil,
	)
	nodes := []kube.Node{
		makeNode("node-1", map[string]string{"kubernetes.io/arch": "amd64"}, nil),
		makeNode("node-2", map[string]string{"kubernetes.io/arch": "amd64"}, []map[string]any{
			{"key": "dedicated", "effect": "NoSchedule", "value": "gpu"},
		}),
	}

	reader := fakeReader{
		items: map[string][]map[string]any{"daemonsets": {ds}},
		nodes: nodes,
	}
	report := New(reader, Options{Namespace: "prod"}).ScanReport(context.Background())

	var found *Finding
	for i := range report.Findings {
		if report.Findings[i].Status == "DaemonSetUnderScheduled" {
			found = &report.Findings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected DaemonSetUnderScheduled for mismatched Equal toleration value, got %#v", report.Findings)
	}
}

// TestDaemonSetFleetHeterogeneous: nodes with arch amd64+arm64, DS has no arch nodeSelector → finding.
func TestDaemonSetFleetHeterogeneous(t *testing.T) {
	ds := makeDS("prod", "agent", 2, nil, nil)
	nodes := []kube.Node{
		makeNode("node-1", map[string]string{"kubernetes.io/arch": "amd64", "kubernetes.io/os": "linux"}, nil),
		makeNode("node-2", map[string]string{"kubernetes.io/arch": "arm64", "kubernetes.io/os": "linux"}, nil),
	}

	reader := fakeReader{
		items: map[string][]map[string]any{"daemonsets": {ds}},
		nodes: nodes,
	}
	report := New(reader, Options{Namespace: "prod"}).ScanReport(context.Background())

	var found *Finding
	for i := range report.Findings {
		if report.Findings[i].Status == "DaemonSetFleetHeterogeneous" {
			found = &report.Findings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected DaemonSetFleetHeterogeneous finding, got %#v", report.Findings)
	}
	if found.Severity != "low" {
		t.Fatalf("expected low severity, got %q", found.Severity)
	}
	var evidenceText string
	for _, e := range found.Evidence {
		evidenceText += e.Value + " "
	}
	if !strings.Contains(evidenceText, "amd64") || !strings.Contains(evidenceText, "arm64") {
		t.Fatalf("expected arch values in evidence, got %q", evidenceText)
	}
}

// TestDaemonSetNoFindingsWhenHealthy: uniform arch, fully scheduled, DS has toleration → no new findings.
func TestDaemonSetNoFindingsWhenHealthy(t *testing.T) {
	// DS tolerates the taint, nodeSelector pins arch.
	ds := makeDS("prod", "logger", 2,
		[]any{
			map[string]any{"key": "dedicated", "effect": "NoSchedule", "operator": "Equal", "value": "infra"},
		},
		map[string]any{"kubernetes.io/arch": "amd64"},
	)
	nodes := []kube.Node{
		makeNode("node-1", map[string]string{"kubernetes.io/arch": "amd64"}, nil),
		makeNode("node-2", map[string]string{"kubernetes.io/arch": "amd64"}, []map[string]any{
			{"key": "dedicated", "effect": "NoSchedule", "value": "infra"},
		}),
	}

	reader := fakeReader{
		items: map[string][]map[string]any{"daemonsets": {ds}},
		nodes: nodes,
	}
	report := New(reader, Options{Namespace: "prod"}).ScanReport(context.Background())

	for _, f := range report.Findings {
		if f.Status == "DaemonSetUnderScheduled" || f.Status == "DaemonSetFleetHeterogeneous" {
			t.Fatalf("unexpected finding %q for healthy DS: %#v", f.Status, f)
		}
	}
}

// TestDaemonSetNewStatusNoStringCollision: new statuses must not substring-match any
// BuildPlan switch key so they safely fall through to the default (review-only) branch.
func TestDaemonSetNewStatusNoStringCollision(t *testing.T) {
	knownKeys := []string{
		"ImagePull", "OOMKilled", "CrashLoopBackOff", "CreateContainerConfigError",
		"ExecFormatError", "PermissionDenied", "NoEndpoints", "ConnectionRefused",
		"HPA", "Evicted", "Webhook",
	}
	for _, status := range []string{"DaemonSetUnderScheduled", "DaemonSetFleetHeterogeneous"} {
		for _, key := range knownKeys {
			if strings.Contains(status, key) {
				t.Fatalf("status %q collides with BuildPlan switch key %q", status, key)
			}
		}
	}
}
