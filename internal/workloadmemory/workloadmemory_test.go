package workloadmemory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFingerprintScopesWorkloadAndIgnoresImageTag(t *testing.T) {
	base := ProfileKey{ClusterID: strings.Repeat("a", 64), Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", ImageRepository: "registry.example/team/api:v1.2"}
	if got, want := Fingerprint(base), Fingerprint(ProfileKey{ClusterID: base.ClusterID, Namespace: base.Namespace, ResourceKind: base.ResourceKind, ResourceName: base.ResourceName, ImageRepository: "registry.example/team/api:v9"}); got != want {
		t.Fatalf("image tag change should keep the workload fingerprint: got %s want %s", got, want)
	}
	for label, changed := range map[string]ProfileKey{
		"cluster":    {ClusterID: strings.Repeat("b", 64), Namespace: base.Namespace, ResourceKind: base.ResourceKind, ResourceName: base.ResourceName, ImageRepository: "registry.example/team/api"},
		"namespace":  {ClusterID: base.ClusterID, Namespace: "other", ResourceKind: base.ResourceKind, ResourceName: base.ResourceName, ImageRepository: "registry.example/team/api"},
		"resource":   {ClusterID: base.ClusterID, Namespace: base.Namespace, ResourceKind: base.ResourceKind, ResourceName: "worker", ImageRepository: "registry.example/team/api"},
		"repository": {ClusterID: base.ClusterID, Namespace: base.Namespace, ResourceKind: base.ResourceKind, ResourceName: base.ResourceName, ImageRepository: "registry.example/team/other:v1"},
	} {
		if Fingerprint(changed) == Fingerprint(base) {
			t.Errorf("%s change shared the same workload fingerprint", label)
		}
	}
	if strings.Contains(Fingerprint(base), "payments") || strings.Contains(Fingerprint(base), "registry.example") {
		t.Fatal("fingerprint must not expose identity fields")
	}
}

func TestDefaultPathUsesClusterHashUnderUserHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := DefaultPath(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".fixora", strings.Repeat("a", 64), "profiles.json"); path != want {
		t.Fatalf("profile path = %q, want %q", path, want)
	}
	if _, err := DefaultPath("../bad"); err == nil {
		t.Fatal("unvalidated cluster identifiers must be rejected")
	}
}

func TestStoreRetainsBoundedHistoryWithPrivateAtomicWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	store := Store{Path: path}
	fingerprint := strings.Repeat("f", 64)
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < maxSessionsPerWorkload+2; i++ {
		err := store.Record(fingerprint, Session{
			At: base.Add(time.Duration(i) * time.Minute), Outcome: OutcomeVerified,
			FailureClass: "OOMKilled", Strategy: "resources", Parity: 91,
			MemoryPeakBytes: 700, MemoryLimitBytes: 1000, MemoryHeadroomPercent: 30,
			RepairMemoryLimitBytes: 1200,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	profile, found, err := store.Load(fingerprint)
	if err != nil || !found {
		t.Fatalf("load profile found=%t err=%v", found, err)
	}
	if len(profile.Sessions) != maxSessionsPerWorkload || !profile.Sessions[0].At.Equal(base.Add(2*time.Minute)) {
		t.Fatalf("history did not retain newest bounded records: %#v", profile.Sessions)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("profile file mode = %o, want 600", fileInfo.Mode().Perm())
	}
	if dirInfo, err := os.Stat(dir); err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("profile directory mode = %v, err=%v; want 700", dirInfo, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("atomic write left a temporary file: %s", entry.Name())
		}
	}
}

func TestStoreExpiresOldSessionsAndBoundsProfileCount(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "profiles.json")}
	now := time.Now().UTC()
	oldFingerprint := strings.Repeat("b", 64)
	freshFingerprint := strings.Repeat("c", 64)
	if err := store.Record(oldFingerprint, Session{At: now.Add(-maxHistoryAge - time.Hour), Outcome: OutcomeVerified}); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(freshFingerprint, Session{At: now, Outcome: OutcomeFailed}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxProfilesPerCluster; i++ {
		fingerprint := fmt.Sprintf("%064x", i+10)
		if err := store.Record(fingerprint, Session{At: now.Add(time.Duration(i) * time.Second), Outcome: OutcomeInconclusive}); err != nil {
			t.Fatal(err)
		}
	}
	if _, found, err := store.Load(oldFingerprint); err != nil || found {
		t.Fatalf("expired profile should be absent: found=%t err=%v", found, err)
	}
	profiles, err := store.read()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles.Profiles) > maxProfilesPerCluster {
		t.Fatalf("profile count = %d, maximum %d", len(profiles.Profiles), maxProfilesPerCluster)
	}
}

func TestStoreCorruptOrOversizedFilesReturnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (Store{Path: path}).Load(strings.Repeat("a", 64)); err == nil {
		t.Fatal("corrupt JSON should be rejected for the caller to ignore safely")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxProfileBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (Store{Path: path}).Load(strings.Repeat("a", 64)); err == nil {
		t.Fatal("oversized profile files must be rejected")
	}
}

func TestStoreConcurrentRecordsRemainValidAndBounded(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "profiles.json")}
	fingerprint := strings.Repeat("c", 64)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- store.Record(fingerprint, Session{At: time.Now().UTC().Add(time.Duration(i) * time.Millisecond), Outcome: OutcomeVerified, Strategy: "resources"})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	profile, found, err := store.Load(fingerprint)
	if err != nil || !found {
		t.Fatalf("concurrent profile load found=%t err=%v", found, err)
	}
	if len(profile.Sessions) != maxSessionsPerWorkload {
		t.Fatalf("concurrent history count = %d, want %d", len(profile.Sessions), maxSessionsPerWorkload)
	}
	data, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) || len(data) > maxProfileBytes {
		t.Fatalf("concurrent store is invalid or oversized: valid=%t bytes=%d", json.Valid(data), len(data))
	}
}
