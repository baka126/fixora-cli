package cli

import (
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/shadow"
)

func TestCoordinateGraphStorageConsentRequiresDistinctToken(t *testing.T) {
	items := []shadow.GraphStorageConsentItem{{Name: "ledger-data", UID: "pvc-uid", RequestedBytes: 1 << 30, MaximumBytes: 2 << 30, SourceStorageClass: "gp3", VolumeSnapshotClass: "snapshots", RestoreStorageClass: "gp3-shadow"}}
	for _, input := range []string{"yes\n", "", "snapshot\n", "SNAPSHOT"} {
		var prompt strings.Builder
		if confirmGraphStoragePlan("payments", items, strings.NewReader(input), &prompt) {
			t.Fatalf("input %q must not authorize storage side effects", input)
		}
		if strings.Contains(prompt.String(), "ledger-data") && (!strings.Contains(prompt.String(), "pvc-uid") || !strings.Contains(prompt.String(), "exact temporary ReferenceGrant") || !strings.Contains(prompt.String(), "crash-consistent") || !strings.Contains(prompt.String(), "provider charges")) {
			t.Fatalf("consent omitted storage impact details: %s", prompt.String())
		}
	}
	var prompt strings.Builder
	if !confirmGraphStoragePlan("payments", items, strings.NewReader("SNAPSHOT\n"), &prompt) {
		t.Fatal("exact distinct storage token should authorize the plan")
	}
	if !strings.Contains(prompt.String(), "payments/ledger-data") || !strings.Contains(prompt.String(), "policy maximum 2147483648 bytes") {
		t.Fatalf("consent does not enumerate exact source and capacity: %s", prompt.String())
	}
}

func TestCoordinateGraphStorageConsentRejectsMissingPlan(t *testing.T) {
	if confirmGraphStoragePlan("payments", nil, strings.NewReader("SNAPSHOT\n"), &strings.Builder{}) {
		t.Fatal("empty storage plan must not be authorized")
	}
}

func TestCoordinateGraphStoragePolicyIsRequiredOnlyForPVCGraphs(t *testing.T) {
	pvcGraph := shadow.DependencyGraph{Nodes: []shadow.GraphNode{{Kind: "PersistentVolumeClaim", Namespace: "payments", Name: "ledger-data"}}}
	if err := validateGraphStoragePolicySelection(pvcGraph, nil); err == nil {
		t.Fatal("PVC graph must require an exact local storage policy")
	}
	if err := validateGraphStoragePolicySelection(shadow.DependencyGraph{}, nil); err != nil {
		t.Fatalf("ordinary graph path should not require a storage policy: %v", err)
	}
	if err := validateGraphStoragePolicySelection(shadow.DependencyGraph{}, &shadow.GraphStorageAuthority{}); err == nil {
		t.Fatal("storage policy must not be silently ignored on a graph without PVCs")
	}
}
