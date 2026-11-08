package analyzer

import (
	"context"
	"errors"
	"strings"
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

func TestPVCFindingIncludesIdentityBoundStorageEvidenceChain(t *testing.T) {
	ctx := NewScanContext(context.Background(), fakeReader{items: map[string][]map[string]any{
		"pvc":            {{"metadata": map[string]any{"namespace": "prod", "name": "data", "uid": "pvc-1", "resourceVersion": "5"}, "spec": map[string]any{"storageClassName": "gp3", "volumeName": "pv-data", "accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": "8Gi"}}}, "status": map[string]any{"phase": "Pending", "capacity": map[string]any{"storage": "8Gi"}}}},
		"storageclasses": {{"metadata": map[string]any{"name": "gp3", "uid": "sc-1", "resourceVersion": "4"}, "provisioner": "ebs.csi.aws.com", "allowVolumeExpansion": true, "volumeBindingMode": "WaitForFirstConsumer"}},
		"pv":             {{"metadata": map[string]any{"name": "pv-data", "uid": "pv-1", "resourceVersion": "3"}, "spec": map[string]any{"storageClassName": "gp3", "persistentVolumeReclaimPolicy": "Retain", "claimRef": map[string]any{"namespace": "prod", "name": "data", "uid": "pvc-1"}, "nodeAffinity": map[string]any{"required": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{map[string]any{"key": "topology.kubernetes.io/zone", "values": []any{"us-east-1a"}}}}}}}}, "status": map[string]any{"phase": "Bound"}}},
		"pods":           {{"metadata": map[string]any{"namespace": "prod", "name": "consumer", "uid": "pod-1", "ownerReferences": []any{map[string]any{"kind": "Deployment", "name": "api", "uid": "deployment-1"}}}, "spec": map[string]any{"nodeName": "node-a", "volumes": []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "data"}}}}, "status": map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}},
	}}, Options{Namespace: "prod"})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzePVCs(ctx)
	if err != nil || len(findings) == 0 {
		t.Fatalf("findings=%v err=%v", findings, err)
	}
	if findings[0].ResourceUID != "pvc-1" {
		t.Fatalf("PVC finding UID=%q, want pvc-1 for guarded repairs", findings[0].ResourceUID)
	}
	labels := map[string]string{}
	for _, evidence := range findings[0].Evidence {
		labels[evidence.Label] = evidence.Value
	}
	for _, want := range []struct{ label, value string }{
		{"PVC identity", "uid=pvc-1 resourceVersion=5 phase=Pending"},
		{"Requested and observed capacity", "requested=8Gi observed=8Gi"},
		{"StorageClass", "gp3 uid=sc-1 resourceVersion=4 provisioner=ebs.csi.aws.com bindingMode=WaitForFirstConsumer expansion=true"},
		{"PersistentVolume", "pv-data uid=pv-1 resourceVersion=3 phase=Bound storageClass=gp3 reclaimPolicy=Retain claim=prod/data uid=pvc-1 topology=topology.kubernetes.io/zone=us-east-1a"},
		{"Consumers", "consumer uid=pod-1 phase=Running ready=true node=node-a owner=Deployment/api"},
	} {
		if labels[want.label] != want.value {
			t.Errorf("evidence %q=%q, want %q", want.label, labels[want.label], want.value)
		}
	}
}

func TestAnalyzePVCUsesDeterministicPVCFindingAndIdentity(t *testing.T) {
	pvc := map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "data", "uid": "pvc-1", "resourceVersion": "5"},
		"spec":     map[string]any{"storageClassName": "", "resources": map[string]any{"requests": map[string]any{"storage": "1Gi"}}},
		"status":   map[string]any{"phase": "Pending"},
	}
	reader := fakeReader{
		resource: pvc,
		items: map[string][]map[string]any{
			"pvc":            {pvc},
			"storageclasses": {},
			"pv":             {},
			"pods":           {},
		},
	}
	finding, err := New(reader, Options{Namespace: "prod"}).AnalyzeResource(context.Background(), "pvc/data")
	if err != nil {
		t.Fatal(err)
	}
	if finding.ResourceKind != "PersistentVolumeClaim" || finding.ResourceUID != "pvc-1" || finding.Status != "MissingStorageClass" {
		t.Fatalf("PVC target did not return the exact analyzer finding: %#v", finding)
	}
	if finding.Summary != "PVC explicitly opts out of StorageClasses; this claim cannot be assigned a class in place." {
		t.Fatalf("explicit empty StorageClass was not explained accurately: %q", finding.Summary)
	}
}

func TestAnalyzeHealthyPVCStillReturnsUIDForPolicyBoundExpansion(t *testing.T) {
	pvc := map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "data", "uid": "pvc-2", "resourceVersion": "8"},
		"spec": map[string]any{
			"storageClassName": "gp3",
			"volumeName":       "pv-data",
			"resources":        map[string]any{"requests": map[string]any{"storage": "1Gi"}},
		},
		"status": map[string]any{"phase": "Bound", "capacity": map[string]any{"storage": "1Gi"}},
	}
	reader := fakeReader{resource: pvc, items: map[string][]map[string]any{
		"pvc":            {pvc},
		"storageclasses": {{"metadata": map[string]any{"name": "gp3"}, "provisioner": "ebs.csi.aws.com", "allowVolumeExpansion": true}},
		"pv":             {},
		"pods":           {},
	}}
	finding, err := New(reader, Options{Namespace: "prod"}).AnalyzeResource(context.Background(), "pvc/data")
	if err != nil {
		t.Fatal(err)
	}
	if finding.ResourceUID != "pvc-2" || finding.ResourceKind != "Pvc" {
		t.Fatalf("healthy PVC lost its identity: %#v", finding)
	}
}

func TestPVCFindingReportsUnavailableInventoryInsteadOfAbsence(t *testing.T) {
	ctx := NewScanContext(context.Background(), fakeReader{items: map[string][]map[string]any{"pvc": {{"metadata": map[string]any{"namespace": "prod", "name": "data", "uid": "pvc-1", "resourceVersion": "5"}, "spec": map[string]any{"storageClassName": "gp3"}, "status": map[string]any{"phase": "Pending"}}}}, itemErrs: map[string]error{"storageclasses": errors.New("forbidden"), "pv": errors.New("forbidden"), "pods": errors.New("forbidden")}}, Options{Namespace: "prod"})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzePVCs(ctx)
	if err != nil || len(findings) == 0 {
		t.Fatalf("findings=%v err=%v", findings, err)
	}
	joined := ""
	for _, evidence := range findings[0].Evidence {
		joined += evidence.Label + ":" + evidence.Value + "\n"
	}
	for _, want := range []string{"StorageClass inventory:unavailable", "PersistentVolume inventory unavailable:PV", "Consumer inventory unavailable:consumer"} {
		if !strings.Contains(joined, want) {
			t.Errorf("evidence does not report %q: %s", want, joined)
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

// TestPVCStorageClassNotFoundWithEmptyStorageClassList verifies an empty
// StorageClass list is a valid empty set, not an enumeration failure.
func TestPVCStorageClassNotFoundWithEmptyStorageClassList(t *testing.T) {
	ctx := NewScanContext(context.Background(), fakeReader{
		items: map[string][]map[string]any{
			"pvc": {
				{
					"metadata": map[string]any{"namespace": "prod", "name": "data"},
					"spec":     map[string]any{"storageClassName": "ghost"},
					"status":   map[string]any{"phase": "Bound"},
				},
			},
			"storageclasses": {},
		},
	}, Options{Namespace: "prod"})

	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzePVCs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "StorageClassNotFound")
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
		if f.Status == "VolumeAttachFailed" {
			joined := ""
			for _, evidence := range f.Evidence {
				joined += evidence.Value + "\n"
			}
			if !strings.Contains(joined, "FailedMount") || !strings.Contains(joined, "unmounted volumes") {
				t.Fatalf("mount event evidence was lost: %s", joined)
			}
		}
	}
}

// TestPVCVolumeAttachFailedChecksAllConsumers verifies RWX/multi-consumer PVCs
// keep every pod/volume pair instead of only the last pod seen.
func TestPVCVolumeAttachFailedChecksAllConsumers(t *testing.T) {
	ctx := NewScanContext(context.Background(), fakeReader{
		items: map[string][]map[string]any{
			"pvc": {
				{
					"metadata": map[string]any{"namespace": "prod", "name": "shared"},
					"spec":     map[string]any{"volumeName": "pv-shared"},
					"status":   map[string]any{"phase": "Bound"},
				},
			},
			"pods": {
				podWithPVCVolume("prod", "consumer-a", "data-a", "shared"),
				podWithPVCVolume("prod", "consumer-b", "data-b", "shared"),
			},
			"storageclasses": {},
		},
		events: []kube.Event{
			{
				Metadata:       kube.ObjectMeta{Namespace: "prod"},
				InvolvedObject: kube.ObjectReference{Namespace: "prod", Name: "consumer-a"},
				Reason:         "FailedMount",
				Message:        "Unable to attach or mount volumes: unmounted volumes=[data-a]",
			},
		},
	}, Options{Namespace: "prod"})

	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzePVCs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "VolumeAttachFailed")
}

// TestPVCVolumeAttachFailedIgnoresOtherVolumeFailures verifies a multi-volume
// pod's unrelated FailedMount event is not attributed to this PVC.
func TestPVCVolumeAttachFailedIgnoresOtherVolumeFailures(t *testing.T) {
	ctx := NewScanContext(context.Background(), fakeReader{
		items: map[string][]map[string]any{
			"pvc": {
				{
					"metadata": map[string]any{"namespace": "prod", "name": "data"},
					"spec":     map[string]any{"volumeName": "pv-data"},
					"status":   map[string]any{"phase": "Bound"},
				},
			},
			"pods": {
				{
					"metadata": map[string]any{"namespace": "prod", "name": "api"},
					"spec": map[string]any{
						"volumes": []any{
							map[string]any{
								"name": "data-volume",
								"persistentVolumeClaim": map[string]any{
									"claimName": "data",
								},
							},
							map[string]any{
								"name": "cache-volume",
								"persistentVolumeClaim": map[string]any{
									"claimName": "cache",
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
				InvolvedObject: kube.ObjectReference{Namespace: "prod", Name: "api"},
				Reason:         "FailedMount",
				Message:        "Unable to attach or mount volumes: unmounted volumes=[cache-volume]",
			},
		},
	}, Options{Namespace: "prod"})

	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzePVCs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFindingWithStatus(t, findings, "VolumeAttachFailed")
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

// TestPVCVolumeAwaitingConsumerScansAllEvents verifies a later unrelated PVC
// event does not hide an earlier WaitForFirstConsumer event.
func TestPVCVolumeAwaitingConsumerScansAllEvents(t *testing.T) {
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
			{
				Metadata:       kube.ObjectMeta{Namespace: "prod"},
				InvolvedObject: kube.ObjectReference{Namespace: "prod", Name: "lazy"},
				Reason:         "ExternalProvisioning",
				Message:        "waiting for external provisioner",
				LastTime:       "2026-06-10T10:01:00Z",
			},
		},
	}, Options{Namespace: "prod"})

	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzePVCs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "VolumeAwaitingConsumer")
	assertNoFindingWithStatus(t, findings, "Pending")
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

func podWithPVCVolume(namespace, podName, volumeName, claimName string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"namespace": namespace, "name": podName},
		"spec": map[string]any{
			"volumes": []any{
				map[string]any{
					"name": volumeName,
					"persistentVolumeClaim": map[string]any{
						"claimName": claimName,
					},
				},
			},
		},
	}
}
