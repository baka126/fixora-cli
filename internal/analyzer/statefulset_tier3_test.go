package analyzer

import (
	"context"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

// stsScanCtx builds a ScanContext that serves typed pods plus arbitrary
// resource-item maps (statefulsets, services, storageclasses, etc.).
func stsScanCtx(items map[string][]map[string]any, pods kube.PodList) (*ScanContext, Analyzer) {
	reader := fakeReader{items: items, pods: pods}
	opts := Options{Namespace: "prod"}
	return NewScanContext(context.Background(), reader, opts), New(reader, opts)
}

// notReadyPod returns a pod where no container is ready.
func notReadyPod(name, namespace string) kube.Pod {
	return kube.Pod{
		Metadata: kube.ObjectMeta{Name: name, Namespace: namespace},
		Status: kube.PodStatus{
			ContainerStatuses: []kube.ContainerStatus{{Name: "app", Ready: false}},
		},
	}
}

// readyPod returns a pod where the single container is ready.
func readyPod(name, namespace string) kube.Pod {
	return kube.Pod{
		Metadata: kube.ObjectMeta{Name: name, Namespace: namespace},
		Status: kube.PodStatus{
			ContainerStatuses: []kube.ContainerStatus{{Name: "app", Ready: true}},
		},
	}
}

func stsFixture(namespace, name string, spec, status map[string]any) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"namespace": namespace, "name": name},
		"spec":     spec,
		"status":   status,
	}
}

// ---- StatefulSetRolloutBlocked ----

func TestStatefulSetRolloutBlockedWhenPod0NotReady(t *testing.T) {
	sts := stsFixture("prod", "mysql", map[string]any{
		"replicas":            float64(3),
		"podManagementPolicy": "OrderedReady",
	}, map[string]any{
		"currentRevision":   "mysql-7f6d5b9b8d",
		"updateRevision":    "mysql-9c4a2e1f3b",
		"availableReplicas": float64(3),
	})
	pods := kube.PodList{Items: []kube.Pod{
		notReadyPod("mysql-0", "prod"),
		readyPod("mysql-1", "prod"),
	}}
	ctx, a := stsScanCtx(map[string][]map[string]any{"statefulsets": {sts}}, pods)
	findings, err := a.analyzeStatefulSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "StatefulSetRolloutBlocked")
}

func TestStatefulSetRolloutBlockedRevisionsEqualNoFinding(t *testing.T) {
	rev := "mysql-7f6d5b9b8d"
	sts := stsFixture("prod", "mysql", map[string]any{"replicas": float64(3)}, map[string]any{
		"currentRevision":   rev,
		"updateRevision":    rev,
		"availableReplicas": float64(3),
	})
	pods := kube.PodList{Items: []kube.Pod{notReadyPod("mysql-0", "prod")}}
	ctx, a := stsScanCtx(map[string][]map[string]any{"statefulsets": {sts}}, pods)
	findings, err := a.analyzeStatefulSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFindingForStatus(t, findings, "StatefulSetRolloutBlocked")
}

func TestStatefulSetRolloutBlockedPod0ReadyNoFinding(t *testing.T) {
	sts := stsFixture("prod", "mysql", map[string]any{"replicas": float64(3)}, map[string]any{
		"currentRevision":   "mysql-aaa",
		"updateRevision":    "mysql-bbb",
		"availableReplicas": float64(3),
	})
	pods := kube.PodList{Items: []kube.Pod{readyPod("mysql-0", "prod")}}
	ctx, a := stsScanCtx(map[string][]map[string]any{"statefulsets": {sts}}, pods)
	findings, err := a.analyzeStatefulSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFindingForStatus(t, findings, "StatefulSetRolloutBlocked")
}

func TestStatefulSetRolloutBlockedPod0AbsentNoFinding(t *testing.T) {
	// pod-0 does not exist yet — revisions differ but the check must NOT fire.
	// The existing ReplicasMismatch check covers missing pods; this check only
	// fires when pod-0 is present-but-not-ready.
	sts := stsFixture("prod", "mysql", map[string]any{
		"replicas":            float64(3),
		"podManagementPolicy": "OrderedReady",
	}, map[string]any{
		"currentRevision":   "mysql-aaa",
		"updateRevision":    "mysql-bbb",
		"availableReplicas": float64(3),
	})
	// Pod list has mysql-1 and mysql-2, but NO mysql-0.
	pods := kube.PodList{Items: []kube.Pod{
		notReadyPod("mysql-1", "prod"),
		readyPod("mysql-2", "prod"),
	}}
	ctx, a := stsScanCtx(map[string][]map[string]any{"statefulsets": {sts}}, pods)
	findings, err := a.analyzeStatefulSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFindingForStatus(t, findings, "StatefulSetRolloutBlocked")
}

func TestStatefulSetRolloutBlockedParallelPolicyNoFinding(t *testing.T) {
	// Parallel podManagementPolicy does not gate rollout on ordinal-0; must NOT fire.
	sts := stsFixture("prod", "mysql", map[string]any{
		"replicas":            float64(3),
		"podManagementPolicy": "Parallel",
	}, map[string]any{
		"currentRevision":   "mysql-aaa",
		"updateRevision":    "mysql-bbb",
		"availableReplicas": float64(3),
	})
	pods := kube.PodList{Items: []kube.Pod{
		notReadyPod("mysql-0", "prod"),
		readyPod("mysql-1", "prod"),
	}}
	ctx, a := stsScanCtx(map[string][]map[string]any{"statefulsets": {sts}}, pods)
	findings, err := a.analyzeStatefulSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFindingForStatus(t, findings, "StatefulSetRolloutBlocked")
}

func TestStatefulSetRolloutBlockedEmptyRevisionSkipped(t *testing.T) {
	sts := stsFixture("prod", "mysql", map[string]any{"replicas": float64(1)}, map[string]any{
		"currentRevision":   "",
		"updateRevision":    "mysql-bbb",
		"availableReplicas": float64(1),
	})
	pods := kube.PodList{Items: []kube.Pod{notReadyPod("mysql-0", "prod")}}
	ctx, a := stsScanCtx(map[string][]map[string]any{"statefulsets": {sts}}, pods)
	findings, err := a.analyzeStatefulSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFindingForStatus(t, findings, "StatefulSetRolloutBlocked")
}

// ---- HeadlessServiceMissing ----

func TestHeadlessServiceMissingWhenServiceAbsent(t *testing.T) {
	sts := stsFixture("prod", "zk", map[string]any{
		"replicas":    float64(3),
		"serviceName": "zk-headless",
	}, map[string]any{"availableReplicas": float64(3)})
	ctx, a := stsScanCtx(map[string][]map[string]any{
		"statefulsets": {sts},
		"services":     {},
	}, kube.PodList{})
	findings, err := a.analyzeStatefulSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "HeadlessServiceMissing")
}

func TestHeadlessServiceMissingWhenServiceNotHeadless(t *testing.T) {
	sts := stsFixture("prod", "zk", map[string]any{
		"replicas":    float64(3),
		"serviceName": "zk-headless",
	}, map[string]any{"availableReplicas": float64(3)})
	svc := map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "zk-headless"},
		"spec":     map[string]any{"clusterIP": "10.0.0.5"},
	}
	ctx, a := stsScanCtx(map[string][]map[string]any{
		"statefulsets": {sts},
		"services":     {svc},
	}, kube.PodList{})
	findings, err := a.analyzeStatefulSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "HeadlessServiceMissing")
}

func TestHeadlessServicePresentNoFinding(t *testing.T) {
	sts := stsFixture("prod", "zk", map[string]any{
		"replicas":    float64(3),
		"serviceName": "zk-headless",
	}, map[string]any{"availableReplicas": float64(3)})
	svc := map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "zk-headless"},
		"spec":     map[string]any{"clusterIP": "None"},
	}
	ctx, a := stsScanCtx(map[string][]map[string]any{
		"statefulsets": {sts},
		"services":     {svc},
	}, kube.PodList{})
	findings, err := a.analyzeStatefulSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFindingForStatus(t, findings, "HeadlessServiceMissing")
}

// ---- StatefulSetStorageUnbindable ----

func TestStatefulSetStorageUnbindableMissingClass(t *testing.T) {
	sts := stsFixture("prod", "pg", map[string]any{
		"replicas": float64(1),
		"volumeClaimTemplates": []any{
			map[string]any{
				"metadata": map[string]any{"name": "data"},
				"spec":     map[string]any{"storageClassName": "fast-ssd"},
			},
		},
	}, map[string]any{"availableReplicas": float64(1)})
	ctx, a := stsScanCtx(map[string][]map[string]any{
		"statefulsets":   {sts},
		"services":       {},
		"storageclasses": {},
	}, kube.PodList{})
	findings, err := a.analyzeStatefulSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "StatefulSetStorageUnbindable")
}

func TestStatefulSetStorageUnbindableClassPresent(t *testing.T) {
	sts := stsFixture("prod", "pg", map[string]any{
		"replicas": float64(1),
		"volumeClaimTemplates": []any{
			map[string]any{
				"metadata": map[string]any{"name": "data"},
				"spec":     map[string]any{"storageClassName": "fast-ssd"},
			},
		},
	}, map[string]any{"availableReplicas": float64(1)})
	sc := map[string]any{
		"metadata": map[string]any{"name": "fast-ssd"},
	}
	ctx, a := stsScanCtx(map[string][]map[string]any{
		"statefulsets":   {sts},
		"services":       {},
		"storageclasses": {sc},
	}, kube.PodList{})
	findings, err := a.analyzeStatefulSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFindingForStatus(t, findings, "StatefulSetStorageUnbindable")
}

func TestStatefulSetStorageUnbindableEmptyClassSkipped(t *testing.T) {
	sts := stsFixture("prod", "pg", map[string]any{
		"replicas": float64(1),
		"volumeClaimTemplates": []any{
			map[string]any{
				"metadata": map[string]any{"name": "data"},
				"spec":     map[string]any{"storageClassName": ""},
			},
		},
	}, map[string]any{"availableReplicas": float64(1)})
	ctx, a := stsScanCtx(map[string][]map[string]any{
		"statefulsets":   {sts},
		"services":       {},
		"storageclasses": {},
	}, kube.PodList{})
	findings, err := a.analyzeStatefulSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFindingForStatus(t, findings, "StatefulSetStorageUnbindable")
}

// ---- Healthy STS: no new findings ----

func TestHealthyStatefulSetProducesNoTier3Findings(t *testing.T) {
	rev := "app-aabbcc"
	sts := stsFixture("prod", "app", map[string]any{
		"replicas":    float64(1),
		"serviceName": "app-headless",
		"volumeClaimTemplates": []any{
			map[string]any{
				"metadata": map[string]any{"name": "data"},
				"spec":     map[string]any{"storageClassName": "standard"},
			},
		},
	}, map[string]any{
		"currentRevision":   rev,
		"updateRevision":    rev,
		"availableReplicas": float64(1),
	})
	svc := map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "app-headless"},
		"spec":     map[string]any{"clusterIP": "None"},
	}
	sc := map[string]any{"metadata": map[string]any{"name": "standard"}}
	pods := kube.PodList{Items: []kube.Pod{readyPod("app-0", "prod")}}
	ctx, a := stsScanCtx(map[string][]map[string]any{
		"statefulsets":   {sts},
		"services":       {svc},
		"storageclasses": {sc},
	}, pods)
	findings, err := a.analyzeStatefulSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"StatefulSetRolloutBlocked", "HeadlessServiceMissing", "StatefulSetStorageUnbindable"} {
		assertNoFindingForStatus(t, findings, status)
	}
}

// ---- Collision guard ----

func TestStatefulSetTier3StatusesDoNotCollideWithPlannerKeys(t *testing.T) {
	statuses := []string{
		"StatefulSetRolloutBlocked",
		"HeadlessServiceMissing",
		"StatefulSetStorageUnbindable",
	}
	plannerKeys := []string{
		"ImagePull", "OOMKilled", "CrashLoopBackOff", "CreateContainerConfigError",
		"ExecFormatError", "PermissionDenied", "NoEndpoints", "ConnectionRefused",
		"HPA", "Evicted", "Webhook",
	}
	for _, s := range statuses {
		for _, key := range plannerKeys {
			if strings.Contains(s, key) || strings.Contains(key, s) {
				t.Fatalf("status %q collides with planner key %q", s, key)
			}
		}
	}
}

// ---- local helpers ----

func assertNoFindingForStatus(t *testing.T, findings []Finding, status string) {
	t.Helper()
	for _, f := range findings {
		if f.Status == status {
			t.Fatalf("did not expect finding with status %q, got %+v", status, f)
		}
	}
}
