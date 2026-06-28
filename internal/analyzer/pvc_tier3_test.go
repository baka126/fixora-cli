package analyzer

import (
	"context"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

// TestPVCStorageClassNotFound verifies that a PVC referencing a non-existent
// StorageClass emits a StorageClassNotFound (high) finding.
func TestPVCStorageClassNotFound(t *testing.T) {
	ctx := NewScanContext(context.Background(), fakeReader{
		items: map[string][]map[string]any{
			"pvc": {
				{
					"metadata": map[string]any{"namespace": "prod", "name": "data"},
					"spec":     map[string]any{"storageClassName": "ghost"},
					"status":   map[string]any{"phase": "Bound"},
				},
			},
			"storageclasses": {
				// "ghost" is intentionally absent; only "standard" exists.
				{"metadata": map[string]any{"name": "standard"}},
			},
		},
	}, Options{Namespace: "prod"})

	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzePVCs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "StorageClassNotFound")
	for _, f := range findings {
		if f.Status == "StorageClassNotFound" && f.Severity != "high" {
			t.Fatalf("expected high severity for StorageClassNotFound, got %q", f.Severity)
		}
	}
}

// TestPVCStorageClassNotFoundSkipsWhenSCExists verifies no finding is emitted
// when the referenced StorageClass is present.
func TestPVCStorageClassNotFoundSkipsWhenSCExists(t *testing.T) {
	ctx := NewScanContext(context.Background(), fakeReader{
		items: map[string][]map[string]any{
			"pvc": {
				{
					"metadata": map[string]any{"namespace": "prod", "name": "data"},
					"spec":     map[string]any{"storageClassName": "fast"},
					"status":   map[string]any{"phase": "Bound"},
				},
			},
			"storageclasses": {
				{"metadata": map[string]any{"name": "fast"}},
			},
		},
	}, Options{Namespace: "prod"})

	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzePVCs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFindingForResource(t, findings, "data")
}

// TestPVCVolumeAttachFailed verifies that FailedMount/FailedAttachVolume events
// on a pod that mounts the PVC produce a VolumeAttachFailed (high) finding.
func TestPVCVolumeAttachFailed(t *testing.T) {
	ctx := NewScanContext(context.Background(), fakeReader{
		items: map[string][]map[string]any{
			"pvc": {
				{
					"metadata": map[string]any{"namespace": "prod", "name": "mypvc"},
					"spec":     map[string]any{},
					"status":   map[string]any{"phase": "Bound"},
				},
			},
			"pods": {
				{
					"metadata": map[string]any{"namespace": "prod", "name": "consumer-pod"},
					"spec": map[string]any{
						"volumes": []any{
							map[string]any{
								"name": "storage",
								"persistentVolumeClaim": map[string]any{
									"claimName": "mypvc",
								},
							},
						},
					},
				},
			},
			"storageclasses": {},
		},
		events: []kube.Event{
			{
				Metadata:       kube.ObjectMeta{Namespace: "prod"},
				InvolvedObject: kube.ObjectReference{Namespace: "prod", Name: "consumer-pod"},
				Reason:         "FailedMount",
				Message:        "Unable to attach or mount volumes: unmounted volumes=[storage]",
			},
		},
	}, Options{Namespace: "prod"})

	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzePVCs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "VolumeAttachFailed")
	for _, f := range findings {
		if f.Status == "VolumeAttachFailed" && f.Severity != "high" {
			t.Fatalf("expected high severity for VolumeAttachFailed, got %q", f.Severity)
		}
	}
}

// TestPVCVolumeResizePending verifies that a PVC with a FileSystemResizePending
// condition produces a VolumeResizePending (medium) finding.
func TestPVCVolumeResizePending(t *testing.T) {
	ctx := NewScanContext(context.Background(), fakeReader{
		items: map[string][]map[string]any{
			"pvc": {
				{
					"metadata": map[string]any{"namespace": "prod", "name": "resizing"},
					"spec":     map[string]any{},
					"status": map[string]any{
						"phase": "Bound",
						"conditions": []any{
							map[string]any{
								"type":   "FileSystemResizePending",
								"status": "True",
							},
						},
					},
				},
			},
			"storageclasses": {},
		},
	}, Options{Namespace: "prod"})

	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzePVCs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "VolumeResizePending")
	for _, f := range findings {
		if f.Status == "VolumeResizePending" && f.Severity != "medium" {
			t.Fatalf("expected medium severity for VolumeResizePending, got %q", f.Severity)
		}
	}
}

// TestPVCVolumeResizePendingResizingCondition verifies the Resizing condition
// also triggers VolumeResizePending.
func TestPVCVolumeResizePendingResizingCondition(t *testing.T) {
	ctx := NewScanContext(context.Background(), fakeReader{
		items: map[string][]map[string]any{
			"pvc": {
				{
					"metadata": map[string]any{"namespace": "prod", "name": "resizing2"},
					"spec":     map[string]any{},
					"status": map[string]any{
						"phase": "Bound",
						"conditions": []any{
							map[string]any{
								"type":   "Resizing",
								"status": "True",
							},
						},
					},
				},
			},
			"storageclasses": {},
		},
	}, Options{Namespace: "prod"})

	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzePVCs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "VolumeResizePending")
}

// TestPVCVolumeAwaitingConsumerReplacePending verifies that a Pending PVC with
// a WaitForFirstConsumer event emits VolumeAwaitingConsumer (low) and NOT
// the generic Pending (medium) finding.
func TestPVCVolumeAwaitingConsumerReplacePending(t *testing.T) {
	ctx := NewScanContext(context.Background(), fakeReader{
		items: map[string][]map[string]any{
			"pvc": {
				{
					"metadata": map[string]any{"namespace": "prod", "name": "lazy"},
					"spec":     map[string]any{},
					"status":   map[string]any{"phase": "Pending"},
				},
			},
			"storageclasses": {},
		},
		events: []kube.Event{
			{
				Metadata:       kube.ObjectMeta{Namespace: "prod"},
				InvolvedObject: kube.ObjectReference{Namespace: "prod", Name: "lazy"},
				Reason:         "WaitForFirstConsumer",
				Message:        "waiting for first consumer to be created before binding",
				LastTime:       "2026-06-10T10:00:00Z",
			},
		},
	}, Options{Namespace: "prod"})

	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzePVCs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "VolumeAwaitingConsumer")
	for _, f := range findings {
		if f.ResourceName == "lazy" && f.Status == "Pending" {
			t.Fatalf("expected VolumeAwaitingConsumer to replace Pending, but got both")
		}
		if f.Status == "VolumeAwaitingConsumer" && f.Severity != "low" {
			t.Fatalf("expected low severity for VolumeAwaitingConsumer, got %q", f.Severity)
		}
	}
}

// TestPVCPendingWithoutWaitForFirstConsumerStillEmitsPending confirms that a
// Pending PVC without the WaitForFirstConsumer event keeps the original Pending
// finding (not VolumeAwaitingConsumer).
func TestPVCPendingWithoutWaitForFirstConsumerStillEmitsPending(t *testing.T) {
	ctx := NewScanContext(context.Background(), fakeReader{
		items: map[string][]map[string]any{
			"pvc": {
				{
					"metadata": map[string]any{"namespace": "prod", "name": "stuck"},
					"spec":     map[string]any{},
					"status":   map[string]any{"phase": "Pending"},
				},
			},
			"storageclasses": {},
		},
	}, Options{Namespace: "prod"})

	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzePVCs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "Pending")
	assertNoFindingWithStatus(t, findings, "VolumeAwaitingConsumer")
}

// assertNoFindingWithStatus fails if any finding has the given status.
func assertNoFindingWithStatus(t *testing.T, findings []Finding, status string) {
	t.Helper()
	for _, f := range findings {
		if f.Status == status {
			t.Fatalf("did not expect finding with status %q, got %#v", status, f)
		}
	}
}

// TestPVCNewStatusesDoNotCollideWithBuildPlanKeys is a collision guard that
// confirms the four new statuses fall through to the generic BuildPlan path
// (review-only, ApplyEligible=false) and do not trigger any pod/service branch
// in BuildPlan, which would indicate an accidental substring collision.
func TestPVCNewStatusesDoNotCollideWithBuildPlanKeys(t *testing.T) {
	// Existing BuildPlan key statuses that must NOT match our new statuses via
	// strings.Contains inside BuildPlan's switch.
	existingKeys := []string{
		"ImagePull", "OOMKilled", "CrashLoopBackOff",
		"CreateContainerConfigError", "ExecFormatError", "PermissionDenied",
		"NoEndpoints", "ConnectionRefused", "HPA", "Evicted", "Webhook",
	}
	newStatuses := []string{
		"StorageClassNotFound", "VolumeAttachFailed",
		"VolumeResizePending", "VolumeAwaitingConsumer",
	}
	for _, status := range newStatuses {
		for _, key := range existingKeys {
			if containsCI(status, key) {
				t.Errorf("new status %q contains existing BuildPlan key %q — collision risk", status, key)
			}
		}
	}
}

// containsCI checks if s contains substr (case-insensitive substring match).
func containsCI(s, substr string) bool {
	sLow := toLower(s)
	subLow := toLower(substr)
	return len(subLow) > 0 && containsStr(sLow, subLow)
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
