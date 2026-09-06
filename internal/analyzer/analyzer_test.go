package analyzer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func TestLintFindsProductionRisks(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "deployment.yaml")
	err := os.WriteFile(manifest, []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
      - name: api
        image: ghcr.io/acme/api:latest
        securityContext:
          privileged: true
      volumes:
      - name: host
        hostPath:
          path: /var/run
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	results, err := Lint([]string{manifest})
	if err != nil {
		t.Fatal(err)
	}

	assertLintContains(t, results, "latest tag")
	assertLintContains(t, results, "privileged containers")
	assertLintContains(t, results, "hostPath volumes")
	assertLintContains(t, results, "resource requests")
	assertLintContains(t, results, "readiness probes")
}

func TestLintWalksDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("skip me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pod.yaml"), []byte(`apiVersion: v1
kind: Pod
metadata:
  name: pinned
spec:
  containers:
  - name: app
    image: ghcr.io/acme/app:v1.2.3
    resources:
      requests:
        cpu: "100m"
        memory: "128Mi"
      limits:
        memory: "256Mi"
    readinessProbe:
      httpGet:
        path: /healthz
        port: 8080
    livenessProbe:
      httpGet:
        path: /healthz
        port: 8080
`), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := Lint([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one lint result, got %d: %#v", len(results), results)
	}
	if results[0].Severity != "ok" {
		t.Fatalf("expected ok lint result, got %#v", results[0])
	}
}

func assertLintContains(t *testing.T, results []LintResult, want string) {
	t.Helper()
	for _, result := range results {
		if strings.Contains(result.Message, want) {
			return
		}
	}
	t.Fatalf("expected lint result containing %q, got %#v", want, results)
}

func TestPrecisionHelpersFindIngressBackends(t *testing.T) {
	services := ingressBackendServices(map[string]any{
		"defaultBackend": map[string]any{
			"service": map[string]any{"name": "api"},
		},
		"rules": []any{map[string]any{
			"http": map[string]any{
				"paths": []any{map[string]any{
					"backend": map[string]any{"serviceName": "legacy"},
				}},
			},
		}},
	})
	got := strings.Join(services, ",")
	if got != "api,legacy" {
		t.Fatalf("expected api,legacy, got %q", got)
	}
}

func TestScanReportEnvelopeStatus(t *testing.T) {
	report := ScanReport{Findings: []Finding{{ID: "ns/pod/fail"}}}
	envelope := report.Envelope()
	if envelope.APIVersion != "fixora.dev/v1alpha1" || envelope.Kind != "AnalysisReport" {
		t.Fatalf("unexpected envelope identity: %#v", envelope)
	}
	if envelope.Status != "ProblemDetected" || envelope.Problems != 1 {
		t.Fatalf("unexpected envelope status: %#v", envelope)
	}
}

func TestScanReportKeepsPodFindingsWhenEventsFail(t *testing.T) {
	reader := fakeReader{
		pods: kube.PodList{Items: []kube.Pod{{
			Metadata: kube.ObjectMeta{Name: "api", Namespace: "prod"},
			Status: kube.PodStatus{
				ContainerStatuses: []kube.ContainerStatus{{
					Name: "api",
					State: map[string]kube.StatusState{
						"waiting": {Reason: "CrashLoopBackOff"},
					},
				}},
			},
		}}},
		eventsErr: fmt.Errorf("events forbidden"),
	}
	report := New(reader, Options{Namespace: "prod"}).ScanReport(context.Background())
	if len(report.Findings) != 1 {
		t.Fatalf("expected pod finding despite events error, got %#v", report)
	}
	foundEventsSkip := false
	for _, skipped := range report.Skipped {
		if skipped.Name == "events" {
			foundEventsSkip = true
		}
	}
	if !foundEventsSkip {
		t.Fatalf("expected skipped events check, got %#v", report.Skipped)
	}
}

func TestTopOwnerKeepsJobIdentity(t *testing.T) {
	pod := kube.Pod{Metadata: kube.ObjectMeta{
		Name:      "worker",
		Namespace: "prod",
		OwnerRefs: []kube.OwnerReference{{
			Kind: "Job",
			Name: "data-migration-2026",
		}},
	}}
	if got := topOwnerKind(pod); got != "Job" {
		t.Fatalf("expected Job owner kind, got %q", got)
	}
	if got := topOwnerName(pod); got != "data-migration-2026" {
		t.Fatalf("expected full Job owner name, got %q", got)
	}
}

func TestAnalyzeDeploymentInspectsOwnedFailingPod(t *testing.T) {
	reader := fakeReader{
		resource: map[string]any{
			"metadata": map[string]any{"name": "api", "namespace": "prod"},
			"spec": map[string]any{
				"selector": map[string]any{"matchLabels": map[string]any{"app": "api"}},
			},
			"status": map[string]any{"availableReplicas": 0},
		},
		pods: kube.PodList{Items: []kube.Pod{{
			Metadata: kube.ObjectMeta{Name: "api-abc", Namespace: "prod", Labels: map[string]string{"app": "api"}},
			Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{
				Name:  "api",
				State: map[string]kube.StatusState{"waiting": {Reason: "ImagePullBackOff"}},
			}}},
		}}},
		events: []kube.Event{{Metadata: kube.ObjectMeta{Namespace: "prod"}, InvolvedObject: kube.ObjectReference{Name: "api-abc"}, Reason: "Failed", Message: "api-abc image pull failed"}},
	}
	finding, err := New(reader, Options{Namespace: "prod"}).AnalyzeResource(context.Background(), "deployment/api")
	if err != nil {
		t.Fatal(err)
	}
	if finding.PodName != "api-abc" || finding.Status != "ImagePullBackOff" {
		t.Fatalf("expected owned failing pod RCA, got %#v", finding)
	}
}

func TestAnalyzeDeploymentNoOwnedPods(t *testing.T) {
	reader := fakeReader{
		resource: map[string]any{
			"metadata": map[string]any{"name": "api", "namespace": "prod"},
			"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "api"}}},
		},
		pods: kube.PodList{},
	}
	finding, err := New(reader, Options{Namespace: "prod"}).AnalyzeResource(context.Background(), "deployment/api")
	if err != nil {
		t.Fatal(err)
	}
	if finding.Status != "NoOwnedPods" {
		t.Fatalf("expected no owned pods state, got %#v", finding)
	}
}

func TestObjectNameStatePreservesForbidden(t *testing.T) {
	reader := fakeReader{runErr: fmt.Errorf("Error from server (Forbidden): secrets is forbidden")}
	ctx := NewScanContext(context.Background(), reader, Options{Namespace: "prod"})
	state := New(reader, Options{Namespace: "prod"}).objectNameState(ctx, "prod", "secret", "tls")
	if !state.Forbidden || state.Exists {
		t.Fatalf("expected forbidden unreadable state, got %#v", state)
	}
}

func TestObjectNameStateClassifiesAPIErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantExists bool
		wantText   string
	}{
		{name: "readable", err: nil, wantExists: true, wantText: "readable"},
		{name: "missing", err: fmt.Errorf("Error from server (NotFound): secrets \"tls\" not found"), wantText: "not found"},
		{name: "unknown resource", err: fmt.Errorf("the server doesn't have a resource type \"widgets\""), wantText: "unknown resource type"},
		{name: "timeout", err: context.DeadlineExceeded, wantText: "timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := fakeReader{runErr: tt.err}
			ctx := NewScanContext(context.Background(), reader, Options{Namespace: "prod"})
			state := New(reader, Options{Namespace: "prod"}).objectNameState(ctx, "prod", "secret", "tls")
			if state.Exists != tt.wantExists || !strings.Contains(state.Message, tt.wantText) {
				t.Fatalf("state=%#v, want exists=%t text=%q", state, tt.wantExists, tt.wantText)
			}
		})
	}
}

func TestScanReportBoundsPodLogConcurrency(t *testing.T) {
	var pods []kube.Pod
	for i := 0; i < 30; i++ {
		pods = append(pods, kube.Pod{
			Metadata: kube.ObjectMeta{Name: fmt.Sprintf("api-%d", i), Namespace: "prod"},
			Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{
				Name:  "api",
				State: map[string]kube.StatusState{"waiting": {Reason: "CrashLoopBackOff"}},
			}}},
		})
	}
	var active int32
	var maxActive int32
	reader := fakeReader{
		pods: kube.PodList{Items: pods},
		logFn: func() {
			now := atomic.AddInt32(&active, 1)
			for {
				old := atomic.LoadInt32(&maxActive)
				if now <= old || atomic.CompareAndSwapInt32(&maxActive, old, now) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt32(&active, -1)
		},
	}
	report := New(reader, Options{Namespace: "prod", IncludeLogs: true, MaxConcurrency: 3}).ScanReport(context.Background())
	if len(report.Findings) != len(pods) {
		t.Fatalf("findings=%d want %d", len(report.Findings), len(pods))
	}
	if got := atomic.LoadInt32(&maxActive); got > 3 {
		t.Fatalf("max active logs=%d, want <=3", got)
	}
}

func TestQuantityMi(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"100Gi", 102400, true},
		{"7936Mi", 7936, true},
		{"8126464Ki", 7936, true},
		{"", 0, false},
		{"100", 0, false},
	} {
		got, ok := quantityMi(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("quantityMi(%q) = %d,%v; want %d,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestMilliCores(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"100m", 100, true},
		{"100", 100000, true},
		{"8", 8000, true},
		{"0.5", 500, true},
		{"", 0, false},
		{"abc", 0, false},
	} {
		got, ok := milliCores(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("milliCores(%q) = %d,%v; want %d,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func allocatableTestNode() kube.Node {
	n := kube.Node{Metadata: kube.ObjectMeta{Name: "node-a"}}
	n.Status.Allocatable = map[string]string{"memory": "8Gi", "cpu": "8"}
	return n
}

func pendingPodWithRequests(name string, requests map[string]string) kube.Pod {
	return kube.Pod{
		Metadata: kube.ObjectMeta{Name: name, Namespace: "prod"},
		Spec: kube.PodSpec{Containers: []kube.Container{{
			Name: "greedy", Image: "busybox:1.36",
			Resources: kube.ResourceRequirements{Requests: requests},
		}}},
		Status: kube.PodStatus{Phase: "Pending"},
	}
}

func findingEvidence(f Finding, label string) (string, bool) {
	for _, e := range f.Evidence {
		if e.Label == label {
			return e.Value, true
		}
	}
	return "", false
}

func TestAllocatableShortfallEvidenceEmittedWhenRequestsExceedNode(t *testing.T) {
	reader := fakeReader{
		pods:  kube.PodList{Items: []kube.Pod{pendingPodWithRequests("greedy-0", map[string]string{"cpu": "100", "memory": "100Gi"})}},
		nodes: []kube.Node{allocatableTestNode()},
	}
	report := New(reader, Options{Namespace: "prod"}).ScanReport(context.Background())
	if len(report.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(report.Findings))
	}
	value, ok := findingEvidence(report.Findings[0], "Requests exceed node allocatable")
	if !ok {
		t.Fatalf("expected allocatable-shortfall evidence, got %+v", report.Findings[0].Evidence)
	}
	if want := "requested memory=102400Mi cpu=100000m; largest node allocatable memory=8192Mi cpu=8000m"; value != want {
		t.Fatalf("evidence value = %q, want %q", value, want)
	}
}

func TestAllocatableShortfallSilentWhenPodFits(t *testing.T) {
	reader := fakeReader{
		pods:  kube.PodList{Items: []kube.Pod{pendingPodWithRequests("modest-0", map[string]string{"cpu": "100m", "memory": "64Mi"})}},
		nodes: []kube.Node{allocatableTestNode()},
	}
	report := New(reader, Options{Namespace: "prod"}).ScanReport(context.Background())
	if len(report.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(report.Findings))
	}
	if _, ok := findingEvidence(report.Findings[0], "Requests exceed node allocatable"); ok {
		t.Fatal("a pod that fits an available node must not get allocatable-shortfall evidence")
	}
}

func TestScanReportStopsWorkersOnContextCancellation(t *testing.T) {
	var pods []kube.Pod
	for i := 0; i < 100; i++ {
		pods = append(pods, kube.Pod{
			Metadata: kube.ObjectMeta{Name: fmt.Sprintf("api-%d", i), Namespace: "prod"},
			Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{
				Name:  "api",
				State: map[string]kube.StatusState{"waiting": {Reason: "CrashLoopBackOff"}},
			}}},
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		_ = New(fakeReader{pods: kube.PodList{Items: pods}}, Options{Namespace: "prod", IncludeLogs: true, MaxConcurrency: 2}).ScanReport(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scan did not stop after context cancellation")
	}
}

func TestPodOnlyIncidentScanSkipsPodSecurityHygiene(t *testing.T) {
	reader := fakeReader{
		pods: kube.PodList{},
		items: map[string][]map[string]any{
			"pods": {
				{
					"metadata": map[string]any{"name": "api", "namespace": "prod"},
					"spec": map[string]any{
						"containers": []any{map[string]any{"name": "api"}},
					},
				},
			},
		},
	}
	report := New(reader, Options{Namespace: "prod", Filters: []string{"pod"}}).ScanReport(context.Background())
	if len(report.Findings) != 0 {
		t.Fatalf("pod-only incident scan should skip pod security hygiene, got %#v", report.Findings)
	}
}

func TestRedactFindingForAIRemovesClusterSecrets(t *testing.T) {
	finding := Finding{
		ID:      "prod/api",
		Summary: "postgres://user:pass@db/prod",
		Evidence: []Evidence{{Label: "Secret", Value: `apiVersion: v1
kind: Secret
data:
  password: cGFzc3dvcmQ=
`}},
		Logs: []LogSnippet{{Source: "current", Text: "Authorization: Bearer abcdefghijklmnopqrstuvwxyz123456"}},
	}
	redacted := RedactFindingForAI(finding)
	text := redacted.Summary + redacted.Evidence[0].Value + redacted.Logs[0].Text
	for _, forbidden := range []string{"user:pass", "cGFzc3dvcmQ=", "abcdefghijklmnopqrstuvwxyz123456"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("AI finding leaked %q: %#v", forbidden, redacted)
		}
	}
}

func TestRedactFindingForAIDoesNotMutateOriginalEvidence(t *testing.T) {
	original := Finding{
		Evidence: []Evidence{{Label: "Image", Value: "repo/app@sha256:0123456789abcdef"}},
		Logs:     []LogSnippet{{Source: "current", Text: "password=hunter2"}},
	}
	redacted := RedactFindingForAI(original)
	if original.Evidence[0].Value != "repo/app@sha256:0123456789abcdef" || original.Logs[0].Text != "password=hunter2" {
		t.Fatalf("AI redaction mutated original finding: %#v", original)
	}
	if redacted.Logs[0].Text == original.Logs[0].Text {
		t.Fatalf("expected independent redacted copy: %#v", redacted)
	}
}

func TestExecFormatFindingIDMatchesStatusAndCachesNodes(t *testing.T) {
	pods := []kube.Pod{
		{
			Metadata: kube.ObjectMeta{Name: "api-0", Namespace: "prod"},
			Spec:     kube.PodSpec{NodeName: "node-a"},
			Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{
				Name:  "api",
				State: map[string]kube.StatusState{"waiting": {Reason: "CrashLoopBackOff"}},
			}}},
		},
		{
			Metadata: kube.ObjectMeta{Name: "api-1", Namespace: "prod"},
			Spec:     kube.PodSpec{NodeName: "node-a"},
			Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{
				Name:  "api",
				State: map[string]kube.StatusState{"waiting": {Reason: "CrashLoopBackOff"}},
			}}},
		},
	}
	var nodeCalls int32
	reader := fakeReader{
		pods:      kube.PodList{Items: pods},
		logText:   "standard_init_linux.go: exec format error",
		nodes:     []kube.Node{{Metadata: kube.ObjectMeta{Name: "node-a", Labels: map[string]string{"kubernetes.io/arch": "arm64", "kubernetes.io/os": "linux"}}}},
		nodeCalls: &nodeCalls,
	}
	report := New(reader, Options{Namespace: "prod", IncludeLogs: true}).ScanReport(context.Background())
	if got := len(report.Findings); got != 2 {
		t.Fatalf("expected 2 findings (one per pod), got %d", got)
	}
	for _, f := range report.Findings {
		if f.Status != "ExecFormatError" {
			t.Fatalf("expected ExecFormatError status, got %q", f.Status)
		}
		if want := f.Namespace + "/" + f.PodName + "/" + f.Status; f.ID != want {
			t.Fatalf("finding ID %q does not match final status, want %q", f.ID, want)
		}
	}
	// Two pods, one shared node listing thanks to the per-scan cache.
	if got := atomic.LoadInt32(&nodeCalls); got != 1 {
		t.Fatalf("expected 1 cached node listing across workers, got %d", got)
	}
}

func TestFindingRecurrenceEvidenceFromLogs(t *testing.T) {
	pods := []kube.Pod{{
		Metadata: kube.ObjectMeta{Name: "api-0", Namespace: "prod"},
		Spec:     kube.PodSpec{NodeName: "node-a"},
		Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{
			Name:  "api",
			State: map[string]kube.StatusState{"waiting": {Reason: "CrashLoopBackOff"}},
		}}},
	}}
	reader := fakeReader{
		pods:    kube.PodList{Items: pods},
		logText: "panic: runtime error: invalid memory address",
	}
	report := New(reader, Options{Namespace: "prod", IncludeLogs: true}).ScanReport(context.Background())
	if len(report.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(report.Findings))
	}
	f := report.Findings[0]
	if f.Status != "ApplicationPanic" {
		t.Fatalf("status = %q, want ApplicationPanic", f.Status)
	}
	if f.ID != f.Namespace+"/"+f.PodName+"/"+f.Status {
		t.Fatalf("finding ID %q not aligned with status", f.ID)
	}
	var found bool
	for _, e := range f.Evidence {
		if e.Label == "Log recurrence" {
			found = true
			// fakeReader returns the same text for current and previous, so it recurs.
			if !strings.Contains(e.Value, "persists across restarts") {
				t.Fatalf("recurrence evidence = %q, want persists-across-restarts", e.Value)
			}
		}
	}
	if !found {
		t.Fatalf("expected a Log recurrence evidence row")
	}
}

func TestFindingPreviousOnlyLogEvidence(t *testing.T) {
	pods := []kube.Pod{{
		Metadata: kube.ObjectMeta{Name: "api-0", Namespace: "prod"},
		Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{
			Name:  "api",
			State: map[string]kube.StatusState{"waiting": {Reason: "CrashLoopBackOff"}},
		}}},
	}}
	reader := fakeReader{
		pods:        kube.PodList{Items: pods},
		currentLog:  "server starting",
		previousLog: "x509: certificate has expired",
	}
	report := New(reader, Options{Namespace: "prod", IncludeLogs: true}).ScanReport(context.Background())
	if len(report.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(report.Findings))
	}
	f := report.Findings[0]
	if f.Status != "TLSHandshakeError" {
		t.Fatalf("status = %q, want TLSHandshakeError", f.Status)
	}
	for _, e := range f.Evidence {
		if e.Label == "Log recurrence" {
			if !strings.Contains(e.Value, "previous logs only") {
				t.Fatalf("recurrence evidence = %q, want previous-only wording", e.Value)
			}
			return
		}
	}
	t.Fatalf("expected a Log recurrence evidence row")
}

func TestServiceAnalyzerFallsBackToLegacyEndpoints(t *testing.T) {
	// No endpointslices key present (GetResourceItems returns nil,nil); the
	// analyzer must consult legacy Endpoints rather than fabricate NoEndpoints.
	ctx := scanContextWithItems(map[string][]map[string]any{
		"services": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "api"},
				"spec":     map[string]any{"selector": map[string]any{"app": "api"}},
			},
		},
		"endpoints": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "api"},
				"subsets": []any{
					map[string]any{"addresses": []any{map[string]any{"ip": "10.0.0.10"}}},
				},
			},
		},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeServiceEndpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFindingForResource(t, findings, "api")
}

func TestScanReportLabelsForbiddenEventsAsRBAC(t *testing.T) {
	reader := fakeReader{eventsErr: fmt.Errorf("Error from server (Forbidden): events is forbidden")}
	report := New(reader, Options{}).ScanReport(context.Background())
	found := false
	for _, s := range report.Skipped {
		if s.Name == "events" {
			found = true
			if !s.RBACBlocked {
				t.Fatalf("forbidden events read must be labeled RBACBlocked, got %#v", s)
			}
		}
	}
	if !found {
		t.Fatal("expected an events skip entry")
	}
}

type fakeReader struct {
	pods             kube.PodList
	podsErr          error
	events           []kube.Event
	resource         map[string]any
	items            map[string][]map[string]any
	itemErrs         map[string]error
	runErr           error
	eventsErr        error
	logFn            func()
	logText          string
	currentLog       string
	previousLog      string
	nodes            []kube.Node
	nodeCalls        *int32
	getResourceCalls *int32
	secretItemCalls  *int32
}

func (f fakeReader) GetPods(context.Context, string, bool) (kube.PodList, error) {
	return f.pods, f.podsErr
}

func (f fakeReader) GetPod(context.Context, string, string) (kube.Pod, error) {
	return kube.Pod{}, fmt.Errorf("not implemented")
}

func (f fakeReader) GetResource(context.Context, string, string) (map[string]any, error) {
	if f.getResourceCalls != nil {
		atomic.AddInt32(f.getResourceCalls, 1)
	}
	if f.resource != nil {
		return f.resource, nil
	}
	return nil, fmt.Errorf("not implemented")
}

func (f fakeReader) GetResourceItems(_ context.Context, _ string, _ bool, resource string) ([]map[string]any, error) {
	if resource == "secrets" && f.secretItemCalls != nil {
		atomic.AddInt32(f.secretItemCalls, 1)
	}
	if f.itemErrs != nil && f.itemErrs[resource] != nil {
		return nil, f.itemErrs[resource]
	}
	if f.items != nil {
		return f.items[resource], nil
	}
	return nil, fmt.Errorf("not implemented")
}

func (f fakeReader) GetEvents(context.Context, string, string) ([]kube.Event, error) {
	if f.events != nil {
		return f.events, f.eventsErr
	}
	return nil, f.eventsErr
}

func (f fakeReader) GetNodes(context.Context) ([]kube.Node, error) {
	if f.nodeCalls != nil {
		atomic.AddInt32(f.nodeCalls, 1)
	}
	return f.nodes, nil
}

func (f fakeReader) Logs(_ context.Context, _, _ string, previous bool) (string, error) {
	if f.logFn != nil {
		f.logFn()
	}
	if previous {
		if f.previousLog != "" {
			return f.previousLog, nil
		}
	} else if f.currentLog != "" {
		return f.currentLog, nil
	}
	return f.logText, nil
}

func (f fakeReader) Run(context.Context, ...string) ([]byte, error) {
	if f.runErr != nil {
		return nil, f.runErr
	}
	return []byte("secret/tls"), nil
}

func nodePlatformEvidence(f Finding) (string, bool) {
	for _, e := range f.Evidence {
		if e.Label == "Node platform" {
			return e.Value, true
		}
	}
	return "", false
}

// An ImagePullBackOff pod produces no container logs, so the log-driven
// ExecFormatError branch never attaches node-platform evidence for it. The
// image inferrer and AI patch path both need that evidence to rank
// platform-compatible replacement images, so findingForPod attaches it directly
// for image-pull statuses.
func TestImagePullFindingCarriesNodePlatformEvidence(t *testing.T) {
	reader := fakeReader{
		pods: kube.PodList{Items: []kube.Pod{{
			Metadata: kube.ObjectMeta{Name: "pull-0", Namespace: "prod"},
			Spec:     kube.PodSpec{NodeName: "node-a"},
			Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{
				Name:  "app",
				State: map[string]kube.StatusState{"waiting": {Reason: "ImagePullBackOff"}},
			}}},
		}}},
		nodes: []kube.Node{{Metadata: kube.ObjectMeta{Name: "node-a", Labels: map[string]string{"kubernetes.io/arch": "arm64", "kubernetes.io/os": "linux"}}}},
	}
	report := New(reader, Options{Namespace: "prod"}).ScanReport(context.Background())
	if len(report.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(report.Findings))
	}
	value, ok := nodePlatformEvidence(report.Findings[0])
	if !ok || value != "linux/arm64" {
		t.Fatalf("expected Node platform evidence linux/arm64, got %q ok=%v", value, ok)
	}
}

func TestCrashLoopFindingHasNoNodePlatformEvidence(t *testing.T) {
	reader := fakeReader{
		pods: kube.PodList{Items: []kube.Pod{{
			Metadata: kube.ObjectMeta{Name: "crash-0", Namespace: "prod"},
			Spec:     kube.PodSpec{NodeName: "node-a"},
			Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{
				Name:  "app",
				State: map[string]kube.StatusState{"waiting": {Reason: "CrashLoopBackOff"}},
			}}},
		}}},
		nodes: []kube.Node{{Metadata: kube.ObjectMeta{Name: "node-a", Labels: map[string]string{"kubernetes.io/arch": "arm64", "kubernetes.io/os": "linux"}}}},
	}
	report := New(reader, Options{Namespace: "prod"}).ScanReport(context.Background())
	if len(report.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(report.Findings))
	}
	if value, ok := nodePlatformEvidence(report.Findings[0]); ok {
		t.Fatalf("CrashLoopBackOff must not trigger node-platform lookup, got %q", value)
	}
}
