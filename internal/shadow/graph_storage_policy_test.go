package shadow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadGraphStoragePolicyAuthorizesExactPVCAndCapacity(t *testing.T) {
	authority := loadGraphStoragePolicyForTest(t, validGraphStoragePolicy)
	if err := authority.CheckFresh(); err != nil {
		t.Fatalf("CheckFresh: %v", err)
	}
	rule, err := authority.Authorize("payments", "ledger-data", 2<<30)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if rule.PVCName != "ledger-data" || rule.VolumeSnapshotClassName != "ebs-snapshots" || rule.RestoreStorageClassName != "gp3-shadow" || rule.MaxLogicalBytes != 4<<30 || rule.MaxTotalLogicalBytes != 8<<30 || !rule.AllowCrashConsistent {
		t.Fatalf("unexpected authorized rule: %#v", rule)
	}
}

func TestGraphStoragePolicyRejectsMalformedOrAmbiguousYAML(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": strings.Replace(validGraphStoragePolicy, "maxLogicalCapacity: 4Gi", "maxLogicalCapacity: 4Gi\n    guess: true", 1),
		"duplicate key": strings.Replace(validGraphStoragePolicy, "name: ledger-data", "name: ledger-data\n    name: another-data", 1),
		"anchor":        strings.Replace(validGraphStoragePolicy, "maxTotalLogicalCapacity: 8Gi", "maxTotalLogicalCapacity: &limit 8Gi", 1),
		"alias": strings.Replace(
			strings.Replace(validGraphStoragePolicy, "maxTotalLogicalCapacity: 8Gi", "maxTotalLogicalCapacity: &limit 8Gi", 1),
			"maxLogicalCapacity: 4Gi", "maxLogicalCapacity: *limit", 1),
		"multiple documents": validGraphStoragePolicy + "\n---\n" + validGraphStoragePolicy,
		"wrong identity":     strings.Replace(validGraphStoragePolicy, "GraphStorageShadowPolicy", "PVCRepairPolicy", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadGraphStoragePolicy(writeGraphStoragePolicy(t, body)); err == nil {
				t.Fatal("LoadGraphStoragePolicy accepted malformed policy")
			}
		})
	}
}

func TestGraphStoragePolicyRejectsDuplicatePVCsAndInvalidCapacityOrClasses(t *testing.T) {
	duplicate := strings.Replace(validGraphStoragePolicy, "    allowCrashConsistent: true", "    allowCrashConsistent: true\n  - name: ledger-data\n    volumeSnapshotClassName: ebs-snapshots\n    restoreStorageClassName: gp3-shadow\n    maxLogicalCapacity: 1Gi\n    allowCrashConsistent: true", 1)
	badClass := strings.Replace(validGraphStoragePolicy, "volumeSnapshotClassName: ebs-snapshots", "volumeSnapshotClassName: bad/class", 1)
	zeroCapacity := strings.Replace(validGraphStoragePolicy, "maxLogicalCapacity: 4Gi", "maxLogicalCapacity: 0", 1)
	missingConsent := strings.Replace(validGraphStoragePolicy, "    allowCrashConsistent: true\n", "", 1)
	capacityOverflow := strings.Replace(validGraphStoragePolicy, "maxTotalLogicalCapacity: 8Gi", "maxTotalLogicalCapacity: 2Gi", 1)
	for name, body := range map[string]string{"duplicate PVC": duplicate, "invalid class": badClass, "zero capacity": zeroCapacity, "crash consistency not approved": missingConsent, "policy maximum below entry maximum": capacityOverflow} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadGraphStoragePolicy(writeGraphStoragePolicy(t, body)); err == nil {
				t.Fatal("LoadGraphStoragePolicy accepted invalid entry")
			}
		})
	}
}

func TestGraphStoragePolicyAuthorizesOnlyExactPVCAndWithinTotalLimit(t *testing.T) {
	authority := loadGraphStoragePolicyForTest(t, validGraphStoragePolicyTwoPVCs)
	if _, err := authority.Authorize("other", "ledger-data", 1); err == nil {
		t.Fatal("authorized a PVC from the wrong namespace")
	}
	if _, err := authority.Authorize("payments", "unknown", 1); err == nil {
		t.Fatal("authorized an unlisted PVC")
	}
	if _, err := authority.Authorize("payments", "ledger-data", 5<<30); err == nil {
		t.Fatal("authorized a PVC above its per-claim capacity")
	}
	if _, err := authority.AuthorizeGraph("payments", map[string]int64{"ledger-data": 4 << 30, "audit-data": 4 << 30}); err == nil {
		t.Fatal("authorized graph claims above the total capacity")
	}
	if _, err := authority.AuthorizeGraph("payments", map[string]int64{"ledger-data": 2 << 30, "audit-data": 2 << 30}); err != nil {
		t.Fatalf("AuthorizeGraph within total capacity: %v", err)
	}
}

func TestGraphStoragePolicyRequiresCrashConsistencyConsent(t *testing.T) {
	body := strings.Replace(validGraphStoragePolicy, "allowCrashConsistent: true", "allowCrashConsistent: false", 1)
	authority := loadGraphStoragePolicyForTest(t, body)
	if _, err := authority.Authorize("payments", "ledger-data", 1); err == nil {
		t.Fatal("authorized crash-consistent storage without explicit consent")
	}
}

func TestLoadGraphStoragePolicyRejectsSymlinkDirectoryAndOversizedFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	if err := os.WriteFile(target, []byte(validGraphStoragePolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGraphStoragePolicy(link); err == nil {
		t.Fatal("LoadGraphStoragePolicy accepted symlink")
	}
	if _, err := LoadGraphStoragePolicy(dir); err == nil {
		t.Fatal("LoadGraphStoragePolicy accepted a directory")
	}
	large := filepath.Join(dir, "large.yaml")
	if err := os.WriteFile(large, []byte(strings.Repeat("#", (1<<20)+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGraphStoragePolicy(large); err == nil {
		t.Fatal("LoadGraphStoragePolicy accepted oversized file")
	}
}

func TestGraphStoragePolicyDetectsFileReplacementAndMutation(t *testing.T) {
	path := writeGraphStoragePolicy(t, validGraphStoragePolicy)
	authority, err := LoadGraphStoragePolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(validGraphStoragePolicy, "gp3-shadow", "gp3-fast", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authority.CheckFresh(); err == nil {
		t.Fatal("authority remained fresh after policy mutation")
	}

	path = writeGraphStoragePolicy(t, validGraphStoragePolicy)
	authority, err = LoadGraphStoragePolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(t.TempDir(), "replacement.yaml")
	if err := os.WriteFile(replacement, []byte(strings.Replace(validGraphStoragePolicy, "gp3-shadow", "gp3-fast", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if err := authority.CheckFresh(); err == nil {
		t.Fatal("authority remained fresh after policy file replacement")
	}
}

func loadGraphStoragePolicyForTest(t *testing.T, body string) *GraphStorageAuthority {
	t.Helper()
	authority, err := LoadGraphStoragePolicy(writeGraphStoragePolicy(t, body))
	if err != nil {
		t.Fatalf("LoadGraphStoragePolicy: %v", err)
	}
	return authority
}

func writeGraphStoragePolicy(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "storage-shadow.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validGraphStoragePolicy = `apiVersion: fixora.io/v1alpha1
kind: GraphStorageShadowPolicy
metadata:
  name: payments-shadow
  namespace: payments
spec:
  maxTotalLogicalCapacity: 8Gi
  pvcs:
  - name: ledger-data
    volumeSnapshotClassName: ebs-snapshots
    restoreStorageClassName: gp3-shadow
    maxLogicalCapacity: 4Gi
    allowCrashConsistent: true
`

const validGraphStoragePolicyTwoPVCs = `apiVersion: fixora.io/v1alpha1
kind: GraphStorageShadowPolicy
metadata:
  name: payments-shadow
  namespace: payments
spec:
  maxTotalLogicalCapacity: 6Gi
  pvcs:
  - name: ledger-data
    volumeSnapshotClassName: ebs-snapshots
    restoreStorageClassName: gp3-shadow
    maxLogicalCapacity: 4Gi
    allowCrashConsistent: true
  - name: audit-data
    volumeSnapshotClassName: ebs-snapshots
    restoreStorageClassName: gp3-shadow
    maxLogicalCapacity: 4Gi
    allowCrashConsistent: true
`
