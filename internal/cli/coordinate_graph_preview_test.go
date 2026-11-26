package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/coordinate"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

func previewGraphFixture() shadow.DependencyGraph {
	return shadow.DependencyGraph{
		Root: "Deployment/payments/api",
		Nodes: []shadow.GraphNode{
			{Kind: "Deployment", Namespace: "payments", Name: "api", UID: "uid-api", ResourceVersion: "3"},
			{Kind: "Deployment", Namespace: "payments", Name: "worker", UID: "uid-worker", ResourceVersion: "4"},
			{Kind: "Secret", Namespace: "payments", Name: "credentials", UID: "uid-secret", ResourceVersion: "5", Object: map[string]any{"data": map[string]any{"token": "preview-secret-sentinel"}}},
		},
		Edges: []shadow.GraphEdge{
			{From: "Deployment/payments/api", To: "Secret/payments/credentials", Type: shadow.GraphSecretEdge},
			{From: "Deployment/payments/worker", To: "Secret/payments/credentials", Type: shadow.GraphSecretEdge},
		},
	}
}

func previewStepsFixture() []coordinate.Step {
	return []coordinate.Step{
		{Ref: "Deployment/api", Kind: "Deployment", Name: "api", Namespace: "payments", Plan: fix.Plan{Strategy: "resources", ApplyEligible: true}},
		{Ref: "Deployment/worker", Kind: "Deployment", Name: "worker", Namespace: "payments", Plan: fix.Plan{Strategy: "resources", ApplyEligible: false, BlockedReasons: []string{"candidate requires review"}}},
	}
}

func TestCoordinateFromPreviewBuildsGraphWithoutClusterWrites(t *testing.T) {
	var out bytes.Buffer
	preview := buildCoordinateGraphPreview(previewGraphFixture(), previewStepsFixture())
	if preview.ShadowVerified || preview.LiveVerified || preview.ExternalDependenciesTested || preview.Cleanup != "not-started" {
		t.Fatalf("preview must not claim execution: %#v", preview)
	}
	if code := writeCoordinateGraphPreview(&out, "json", preview); code != 0 {
		t.Fatalf("write preview returned %d", code)
	}
	if strings.Contains(out.String(), "preview-secret-sentinel") {
		t.Fatalf("preview leaked dependency values: %s", out.String())
	}
	if !strings.Contains(out.String(), `"shadowVerified": false`) || !strings.Contains(out.String(), `"liveVerified": false`) {
		t.Fatalf("preview JSON omitted verification status: %s", out.String())
	}
}

func TestCoordinateFromPreviewReportsUnsupportedEdges(t *testing.T) {
	graph := previewGraphFixture()
	graph.Blockers = []string{"PersistentVolumeClaim storage is outside combined shadow scope"}
	preview := buildCoordinateGraphPreview(graph, previewStepsFixture())
	var out bytes.Buffer
	if code := writeCoordinateGraphPreview(&out, "text", preview); code != 0 {
		t.Fatalf("write preview returned %d", code)
	}
	if !strings.Contains(out.String(), "PersistentVolumeClaim storage is outside combined shadow scope") {
		t.Fatalf("preview omitted graph blocker: %s", out.String())
	}
	if !strings.Contains(out.String(), "requires review") {
		t.Fatalf("preview omitted candidate eligibility: %s", out.String())
	}
}

func TestCoordinateFromPreviewNeverPrintsDependencyValues(t *testing.T) {
	preview := buildCoordinateGraphPreview(previewGraphFixture(), previewStepsFixture())
	for _, format := range []string{"text", "json", "yaml"} {
		var out bytes.Buffer
		writeCoordinateGraphPreview(&out, format, preview)
		if strings.Contains(out.String(), "preview-secret-sentinel") {
			t.Fatalf("%s output leaked Secret value: %s", format, out.String())
		}
	}
}

func TestCoordinateFromPreviewShowsAuthorizedPVCPlanAndNoWrites(t *testing.T) {
	graph := previewGraphFixture()
	graph.Nodes = append(graph.Nodes, shadow.GraphNode{Kind: "PersistentVolumeClaim", Namespace: "payments", Name: "ledger-data", UID: "pvc-uid", ResourceVersion: "9", Storage: &shadow.GraphStorageSource{StorageClassName: "gp3", VolumeSnapshotClassName: "ebs-snapshots", RestoreStorageClassName: "gp3-shadow", RequestedBytes: 2 << 30, MaxLogicalBytes: 4 << 30}})
	graph.Edges = append(graph.Edges, shadow.GraphEdge{From: "Deployment/payments/api", To: "PersistentVolumeClaim/payments/ledger-data", Type: shadow.GraphPVCEdge, SourceVolumeName: "data"})
	preview := buildCoordinateGraphPreview(graph, previewStepsFixture())
	if preview.ShadowVerified || preview.LiveVerified || preview.Cleanup != "not-started" {
		t.Fatalf("read-only preview claims a run: %#v", preview)
	}
	if len(preview.StoragePlan) != 1 || preview.StoragePlan[0].PVC != "PersistentVolumeClaim/payments/ledger-data" || preview.StoragePlan[0].SnapshotClass != "ebs-snapshots" || preview.StoragePlan[0].RestoreStorageClass != "gp3-shadow" || preview.StoragePlan[0].RequestedBytes != 2<<30 || preview.StoragePlan[0].MaximumBytes != 4<<30 {
		t.Fatalf("preview omitted exact authorized PVC plan: %#v", preview.StoragePlan)
	}
	var out bytes.Buffer
	if code := writeCoordinateGraphPreview(&out, "text", preview); code != 0 {
		t.Fatalf("write preview returned %d", code)
	}
	for _, want := range []string{"CSI snapshot plan (no writes in preview)", "AnyVolumeDataSource and CrossNamespaceVolumeDataSource", "crash-consistent", "temporary VolumeSnapshot and exact ReferenceGrant", "restored PVCs and shadow Pods"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("preview omitted %q: %s", want, out.String())
		}
	}
}

func TestStorageShadowPolicyFlagParses(t *testing.T) {
	got, _, err := parseFlags([]string{"--storage-shadow-policy", "/tmp/storage-policy.yaml", "--from", "Deployment/api", "--preview"})
	if err != nil {
		t.Fatal(err)
	}
	if got.storageShadowPolicy != "/tmp/storage-policy.yaml" || !got.preview {
		t.Fatalf("flag values not retained: %#v", got)
	}
	writeOptions, _, err := parseFlags([]string{"--storage-shadow-policy", "/tmp/storage-policy.yaml", "--from", "Deployment/api"})
	if err != nil {
		t.Fatal(err)
	}
	if writeOptions.storageShadowPolicy != "/tmp/storage-policy.yaml" || writeOptions.preview {
		t.Fatalf("storage execution must be selectable outside preview, with consent enforced at runtime: %#v", writeOptions)
	}
}
