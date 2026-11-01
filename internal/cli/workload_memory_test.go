package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/workloadmemory"
)

func TestWorkloadMemoryLookupUsesLatestVerifiedRecord(t *testing.T) {
	k := workloadMemoryKubectl(t, "https://cluster-a.example:6443")
	path := filepath.Join(t.TempDir(), "profiles.json")
	clusterSum := sha256.Sum256([]byte("https://cluster-a.example:6443"))
	finding := workloadMemoryFinding("registry.example/team/api:v1")
	fingerprint := workloadmemory.Fingerprint(workloadmemory.ProfileKey{
		ClusterID: fmt.Sprintf("%x", clusterSum), Namespace: finding.Namespace, ResourceKind: finding.ResourceKind,
		ResourceName: finding.ResourceName, ImageRepository: "registry.example/team/api",
	})
	store := workloadmemory.Store{Path: path}
	if err := store.Record(fingerprint, workloadmemory.Session{
		At: time.Now().UTC().Add(-time.Hour), Outcome: workloadmemory.OutcomeVerified,
		FailureClass: "OOMKilled", Strategy: "resources", Parity: 91, TimeToReadySeconds: 4,
		MemoryPeakBytes: 700 * 1024 * 1024, MemoryLimitBytes: 1024 * 1024 * 1024,
		MemoryHeadroomPercent: 31, RepairMemoryLimitBytes: 1280 * 1024 * 1024,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(fingerprint, workloadmemory.Session{Outcome: workloadmemory.OutcomeFailed, Strategy: "resources"}); err != nil {
		t.Fatal(err)
	}

	got, err := lookupWorkloadMemory(context.Background(), k, finding, path)
	if err != nil || got.fingerprint != fingerprint {
		t.Fatalf("lookup fingerprint=%q err=%v, want %q", got.fingerprint, err, fingerprint)
	}
	for _, want := range []string{"previously verified", "OOMKilled", "parity 91%", "1280Mi", "700Mi", "31%", "ready in 4s"} {
		if !strings.Contains(got.note, want) {
			t.Errorf("historical note missing %q: %q", want, got.note)
		}
	}
	if strings.Contains(got.note, "api") || strings.Contains(got.note, "cluster-a.example") {
		t.Fatalf("historical note exposed workload or endpoint identity: %q", got.note)
	}
}

func TestWorkloadMemoryLookupSeparatesImageRepositoriesButIgnoresTags(t *testing.T) {
	k := workloadMemoryKubectl(t, "https://cluster-b.example:6443")
	path := filepath.Join(t.TempDir(), "profiles.json")
	finding := workloadMemoryFinding("registry.example/team/api:v7")
	clusterSum := sha256.Sum256([]byte("https://cluster-b.example:6443"))
	fingerprint := workloadmemory.Fingerprint(workloadmemory.ProfileKey{
		ClusterID: fmt.Sprintf("%x", clusterSum), Namespace: finding.Namespace, ResourceKind: finding.ResourceKind,
		ResourceName: finding.ResourceName, ImageRepository: "registry.example/team/api",
	})
	if err := (workloadmemory.Store{Path: path}).Record(fingerprint, workloadmemory.Session{Outcome: workloadmemory.OutcomeVerified, Strategy: "resources"}); err != nil {
		t.Fatal(err)
	}
	finding.Evidence[0].Value = "registry.example/team/api:v8"
	if got, err := lookupWorkloadMemory(context.Background(), k, finding, path); err != nil || got.note == "" {
		t.Fatalf("image tag update should reuse the matching repository profile: note=%q err=%v", got.note, err)
	}
	finding.Evidence[0].Value = "registry.example/other/api:v8"
	if got, err := lookupWorkloadMemory(context.Background(), k, finding, path); err != nil || got.note != "" {
		t.Fatalf("different image repository must not match: note=%q err=%v", got.note, err)
	}
}

func TestWorkloadMemoryLookupSkipsUnverifiedMissingAndCorruptProfiles(t *testing.T) {
	k := workloadMemoryKubectl(t, "https://cluster-c.example:6443")
	path := filepath.Join(t.TempDir(), "profiles.json")
	finding := workloadMemoryFinding("registry.example/team/api:v1")
	clusterSum := sha256.Sum256([]byte("https://cluster-c.example:6443"))
	fingerprint := workloadmemory.Fingerprint(workloadmemory.ProfileKey{
		ClusterID: fmt.Sprintf("%x", clusterSum), Namespace: finding.Namespace, ResourceKind: finding.ResourceKind,
		ResourceName: finding.ResourceName, ImageRepository: "registry.example/team/api",
	})
	store := workloadmemory.Store{Path: path}
	if err := store.Record(fingerprint, workloadmemory.Session{Outcome: workloadmemory.OutcomeFailed, Strategy: "resources"}); err != nil {
		t.Fatal(err)
	}
	if got, err := lookupWorkloadMemory(context.Background(), k, finding, path); err != nil || got.note != "" {
		t.Fatalf("failed history must not be presented as verified: note=%q err=%v", got.note, err)
	}
	if got, err := lookupWorkloadMemory(context.Background(), k, finding, filepath.Join(t.TempDir(), "missing.json")); err != nil || got.note != "" {
		t.Fatalf("missing profile should be an empty lookup: note=%q err=%v", got.note, err)
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := lookupWorkloadMemory(context.Background(), k, finding, corrupt); err == nil || got.note != "" {
		t.Fatalf("corrupt profile should be ignored without evidence: note=%q err=%v", got.note, err)
	}
}

func TestAppendWorkloadMemoryEvidenceIsAdvisoryOnly(t *testing.T) {
	finding := workloadMemoryFinding("registry.example/team/api:v1")
	before := fix.BuildPlan(finding)
	appendWorkloadMemoryEvidence(&finding, "previously verified OOMKilled via resources; parity 91%")
	after := fix.BuildPlan(finding)
	if after.ApplyEligible != before.ApplyEligible || after.CanApply != before.CanApply || after.Strategy != before.Strategy {
		t.Fatalf("historical evidence changed repair authorization: before=%#v after=%#v", before, after)
	}
	if len(finding.Evidence) != 2 || finding.Evidence[1].Label != workloadMemoryEvidenceLabel {
		t.Fatalf("historical context evidence missing: %#v", finding.Evidence)
	}
}

func TestAppendWorkloadMemoryEvidenceDoesNotStoreOrExposeSensitiveText(t *testing.T) {
	finding := workloadMemoryFinding("registry.example/team/api:v1")
	appendWorkloadMemoryEvidence(&finding, "previously verified OOMKilled via resources; parity 91%")
	joined := finding.Evidence[len(finding.Evidence)-1].Value
	if strings.Contains(joined, "registry.example") || strings.Contains(joined, "payments") {
		t.Fatalf("memory context exposed sensitive or identifying data: %s", joined)
	}
}

func TestWorkloadMemoryLookupDropsUnsafeStoredText(t *testing.T) {
	const privateValue = "password=local-secret-value"
	k := workloadMemoryKubectl(t, "https://private-cluster.example:6443")
	path := filepath.Join(t.TempDir(), "profiles.json")
	finding := workloadMemoryFinding("registry.example/team/api:v1")
	clusterSum := sha256.Sum256([]byte("https://private-cluster.example:6443"))
	fingerprint := workloadmemory.Fingerprint(workloadmemory.ProfileKey{
		ClusterID: fmt.Sprintf("%x", clusterSum), Namespace: finding.Namespace, ResourceKind: finding.ResourceKind,
		ResourceName: finding.ResourceName, ImageRepository: "registry.example/team/api",
	})
	if err := (workloadmemory.Store{Path: path}).Record(fingerprint, workloadmemory.Session{
		Outcome: workloadmemory.OutcomeVerified, FailureClass: privateValue, Strategy: privateValue, Parity: 90,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := lookupWorkloadMemory(context.Background(), k, finding, path)
	if err != nil || strings.Contains(got.note, privateValue) || strings.Contains(got.note, "private-cluster.example") {
		t.Fatalf("unsafe stored text reached historical context: result=%#v err=%v", got, err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), privateValue) {
		t.Fatalf("unsafe session text was persisted: %s", stored)
	}
}

func workloadMemoryFinding(image string) analyzer.Finding {
	return analyzer.Finding{
		Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", Status: "OOMKilled",
		Evidence: []analyzer.Evidence{{Label: "Container image api", Value: image}},
	}
}

func workloadMemoryKubectl(t *testing.T, endpoint string) kube.Kubectl {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "kubectl")
	script := "#!/bin/sh\nprintf '%s' '" + endpoint + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return kube.Kubectl{}
}
