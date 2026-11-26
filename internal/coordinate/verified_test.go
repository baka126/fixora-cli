package coordinate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/ops"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"gopkg.in/yaml.v3"
)

type certificateReader struct {
	kube.Reader
	pod kube.Pod
}

func (r certificateReader) GetPod(context.Context, string, string) (kube.Pod, error) {
	return r.pod, nil
}
func certifiedFixture(t *testing.T, name string) CertifiedStep {
	t.Helper()
	before, _ := kube.WorkloadPatch("Deployment", name, "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "old:v1"}}})
	before["metadata"].(map[string]any)["uid"] = "owner-" + name
	patch, _ := kube.WorkloadPatch("Deployment", name, "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "new:v2"}}})
	data, _ := yaml.Marshal(patch)
	text := string(data)
	snapshot := repairpolicy.Snapshot{Target: repairpolicy.Target{Kind: "Deployment", Name: name, Namespace: "demo", UID: "owner-" + name}, Object: before}
	proof, err := repairpolicy.BindCommon(snapshot, text, "image", func(string, string, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	pod := kube.Pod{Metadata: kube.ObjectMeta{Name: "shadow", Namespace: "demo", UID: "shadow-" + name, Labels: map[string]string{"fixora.io/sandbox": "true", "fixora.io/session": "session"}}, Spec: kube.PodSpec{Containers: []kube.Container{{Name: "app", Image: "new:v2"}}}, Status: kube.PodStatus{Phase: "Running", Conditions: []kube.Condition{{Type: "Ready", Status: "True"}}, ContainerStatuses: []kube.ContainerStatus{{Name: "app", Ready: true}}}}
	receipt, err := repairpolicy.VerifyCommon(context.Background(), certificateReader{pod: pod}, proof, &pod, text)
	if err != nil {
		t.Fatal(err)
	}
	inverse, err := BuildInversePatch(before, text)
	if err != nil {
		t.Fatal(err)
	}
	plan := fix.Plan{Resource: "Deployment/" + name, Namespace: "demo", Strategy: "image", CanApply: true, ApplyEligible: true, TargetUID: "owner-" + name, PatchTemplate: text, PolicyProof: proof}
	return CertifiedStep{Step: Step{Ref: plan.Resource, Kind: "Deployment", Name: name, Namespace: "demo", Plan: plan}, Proof: proof, Receipt: receipt, InversePatch: inverse, Before: snapshot}
}

type verifiedFake struct {
	calls        []string
	classes      map[string]string
	applyError   string
	restoreError string
}

func (d *verifiedFake) Preflight(_ context.Context, c CertifiedStep) (ops.TargetSnapshot, error) {
	d.calls = append(d.calls, "preflight "+c.Step.Name)
	return ops.TargetSnapshot{UID: c.Step.Plan.TargetUID, ResourceVersion: "7"}, nil
}
func (d *verifiedFake) Apply(_ context.Context, c CertifiedStep, _ ops.TargetSnapshot) error {
	d.calls = append(d.calls, "apply "+c.Step.Name)
	if c.Step.Name == d.applyError {
		return errors.New("apply failed")
	}
	return nil
}
func (d *verifiedFake) Verify(_ context.Context, c CertifiedStep) (ops.RolloutOutcome, error) {
	d.calls = append(d.calls, "verify "+c.Step.Name)
	class := d.classes[c.Step.Name]
	if class == "" {
		class = ops.RolloutHealthy
	}
	return ops.RolloutOutcome{Class: class, Summary: class}, nil
}
func (d *verifiedFake) RestoreFields(_ context.Context, c CertifiedStep) (ops.RolloutOutcome, error) {
	d.calls = append(d.calls, "restore "+c.Step.Name)
	if c.Step.Name == d.restoreError {
		return ops.RolloutOutcome{}, errors.New("conflict")
	}
	return ops.RolloutOutcome{Class: RollbackRestored}, nil
}
func TestSerializedCertifiedStepCannotAuthorizeApply(t *testing.T) {
	steps := []CertifiedStep{certifiedFixture(t, "a"), certifiedFixture(t, "b")}
	data, _ := json.Marshal(steps)
	var replay []CertifiedStep
	_ = json.Unmarshal(data, &replay)
	d := &verifiedFake{}
	report := RunVerified(context.Background(), replay, d, func() bool { return true }, func() bool { return true })
	if !report.Aborted || report.Mutated || len(d.calls) > 0 {
		t.Fatal(report, d.calls)
	}
}
func TestVerifiedSagaStopsOnUnknownAndReportsExactMutations(t *testing.T) {
	d := &verifiedFake{classes: map[string]string{"a": ops.RolloutUnknown}}
	report := RunVerified(context.Background(), []CertifiedStep{certifiedFixture(t, "a"), certifiedFixture(t, "b")}, d, func() bool { return true }, func() bool { return false })
	if !report.Failed || !report.Mutated || report.Steps[0].Healthy || report.Steps[1].Applied || report.Steps[0].RollbackStatus != "available" {
		t.Fatal(report)
	}
	if strings.Contains(strings.Join(d.calls, ";"), "apply b") {
		t.Fatal(d.calls)
	}
}

func TestGraphCertifiedStepsAllowConnectedWorkloadsAndBindCandidates(t *testing.T) {
	first, second := certifiedFixture(t, "api"), certifiedFixture(t, "worker")
	apiNode := graphNodeForCertifiedStep(first)
	workerNode := graphNodeForCertifiedStep(second)
	config := shadow.GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: "demo", Name: "shared", UID: "cm-uid", ResourceVersion: "9"}
	graph := shadow.DependencyGraph{Root: apiNode.ID(), Nodes: []shadow.GraphNode{apiNode, workerNode, config}, Edges: []shadow.GraphEdge{{From: apiNode.ID(), To: config.ID(), Type: shadow.GraphConfigMapEdge}, {From: workerNode.ID(), To: config.ID(), Type: shadow.GraphConfigMapEdge}}}
	result := shadow.GraphResult{Verified: true, Status: shadow.VerificationVerified, Namespace: "fixora-shadow-run", NamespaceUID: "namespace-uid", NamespaceResourceVersion: "1", CleanupStatus: "deleted",
		Workloads:          []shadow.GraphWorkloadResult{{Source: apiNode.ID(), Pod: "api-shadow", PodUID: "api-pod", PodResourceVersion: "2", Verified: true, Status: shadow.VerificationVerified}, {Source: workerNode.ID(), Pod: "worker-shadow", PodUID: "worker-pod", PodResourceVersion: "3", Verified: true, Status: shadow.VerificationVerified}},
		SourceDependencies: []shadow.GraphNode{config}, Dependencies: []shadow.GraphNode{{APIVersion: "v1", Kind: "ConfigMap", Namespace: "fixora-shadow-run", Name: "shared", UID: "shadow-cm", ResourceVersion: "1"}},
		NetworkPolicies: []shadow.GraphNode{{Kind: "NetworkPolicy", Namespace: "fixora-shadow-run", Name: "fixora-default-deny", UID: "deny", ResourceVersion: "1"}, {Kind: "NetworkPolicy", Namespace: "fixora-shadow-run", Name: "fixora-graph-access", UID: "allow", ResourceVersion: "2"}}}
	source, err := GraphSourceSnapshot(graph)
	if err != nil {
		t.Fatal(err)
	}
	result.GraphDigest = source.Digest
	cert, err := NewGraphCertificate(graph, result)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckGraphCertifiedSteps([]CertifiedStep{first, second}, cert, source); err != nil {
		t.Fatalf("connected graph candidates should pass their graph certificate: %v", err)
	}
	second.Step.Plan.PatchTemplate += "# changed\n"
	if err := CheckGraphCertifiedSteps([]CertifiedStep{first, second}, cert, source); err == nil {
		t.Fatal("candidate bytes differing from the graph receipt must block")
	}
}

func graphNodeForCertifiedStep(step CertifiedStep) shadow.GraphNode {
	return shadow.GraphNode{APIVersion: "apps/v1", Kind: "Deployment", Namespace: step.Step.Namespace, Name: step.Step.Name, UID: step.Step.Plan.TargetUID, ResourceVersion: "1", CandidatePatch: step.Step.Plan.PatchYAML()}
}

func graphVerifiedFixture(t *testing.T) ([]CertifiedStep, GraphCertificate, GraphSnapshot) {
	t.Helper()
	steps := []CertifiedStep{certifiedFixture(t, "api"), certifiedFixture(t, "worker")}
	apiNode, workerNode := graphNodeForCertifiedStep(steps[0]), graphNodeForCertifiedStep(steps[1])
	config := shadow.GraphNode{APIVersion: "v1", Kind: "ConfigMap", Namespace: "demo", Name: "shared", UID: "cm-uid", ResourceVersion: "9"}
	graph := shadow.DependencyGraph{Root: apiNode.ID(), Nodes: []shadow.GraphNode{apiNode, workerNode, config}, Edges: []shadow.GraphEdge{{From: apiNode.ID(), To: config.ID(), Type: shadow.GraphConfigMapEdge}, {From: workerNode.ID(), To: config.ID(), Type: shadow.GraphConfigMapEdge}}}
	result := shadow.GraphResult{Verified: true, Status: shadow.VerificationVerified, Namespace: "fixora-shadow-run", NamespaceUID: "namespace-uid", NamespaceResourceVersion: "1", CleanupStatus: "deleted",
		Workloads:          []shadow.GraphWorkloadResult{{Source: apiNode.ID(), Pod: "api-shadow", PodUID: "api-pod", PodResourceVersion: "2", Verified: true, Status: shadow.VerificationVerified}, {Source: workerNode.ID(), Pod: "worker-shadow", PodUID: "worker-pod", PodResourceVersion: "3", Verified: true, Status: shadow.VerificationVerified}},
		SourceDependencies: []shadow.GraphNode{config}, Dependencies: []shadow.GraphNode{{APIVersion: "v1", Kind: "ConfigMap", Namespace: "fixora-shadow-run", Name: "shared", UID: "shadow-cm", ResourceVersion: "1"}},
		NetworkPolicies: []shadow.GraphNode{{Kind: "NetworkPolicy", Namespace: "fixora-shadow-run", Name: "fixora-default-deny", UID: "deny", ResourceVersion: "1"}, {Kind: "NetworkPolicy", Namespace: "fixora-shadow-run", Name: "fixora-graph-access", UID: "allow", ResourceVersion: "2"}}}
	snapshot, err := GraphSourceSnapshot(graph)
	if err != nil {
		t.Fatal(err)
	}
	result.GraphDigest = snapshot.Digest
	cert, err := NewGraphCertificate(graph, result)
	if err != nil {
		t.Fatal(err)
	}
	return steps, cert, snapshot
}

func TestGraphVerifiedPreflightsCompleteSetBeforeAnyWrite(t *testing.T) {
	steps, cert, snapshot := graphVerifiedFixture(t)
	d := &verifiedFake{}
	reads := 0
	readCurrent := func(context.Context) (GraphSnapshot, error) {
		reads++
		current := snapshot
		if reads == 3 {
			current.Nodes = append([]GraphSourceIdentity(nil), snapshot.Nodes...)
			current.Nodes[0].ResourceVersion = "changed"
		}
		return current, nil
	}
	confirmed := false
	report := RunGraphVerified(context.Background(), steps, cert, readCurrent, d, func() bool { confirmed = true; return true }, func() bool { return false })
	if !confirmed || !report.Aborted || report.Mutated || containsCall(d.calls, "apply ") {
		t.Fatalf("stale graph must block before first mutation: %#v calls=%v", report, d.calls)
	}
}

func TestGraphVerifiedSagaPreflightsAllBeforeApply(t *testing.T) {
	steps, cert, snapshot := graphVerifiedFixture(t)
	d := &verifiedFake{}
	reads := 0
	readCurrent := func(context.Context) (GraphSnapshot, error) { reads++; return snapshot, nil }
	report := RunGraphVerified(context.Background(), steps, cert, readCurrent, d, func() bool { return true }, func() bool { return false })
	if report.Aborted || report.Failed || !report.Mutated || len(d.calls) == 0 {
		t.Fatalf("expected graph apply saga: %#v calls=%v", report, d.calls)
	}
	preflightsBeforeApply, firstApply := 0, -1
	for i, call := range d.calls {
		if firstApply < 0 && strings.HasPrefix(call, "apply ") {
			firstApply = i
			continue
		}
		if firstApply < 0 && strings.HasPrefix(call, "preflight ") {
			preflightsBeforeApply++
		}
	}
	if preflightsBeforeApply < len(steps) || firstApply < 0 {
		t.Fatalf("all targets must preflight before first write: %v", d.calls)
	}
	if report.ShadowVerified == nil || !*report.ShadowVerified || report.LiveVerified == nil || !*report.LiveVerified || report.ExternalDependenciesTested == nil || *report.ExternalDependenciesTested || report.ShadowCleanup != "deleted" {
		t.Fatalf("graph verification surfaces must be distinct in the report: %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"externalDependenciesTested":false`) || !strings.Contains(string(encoded), `"shadowCleanup":"deleted"`) {
		t.Fatalf("structured report omitted graph boundary fields: %s", encoded)
	}
}

func TestGraphVerifiedRejectsMissingCertificateBeforePreflight(t *testing.T) {
	steps, _, snapshot := graphVerifiedFixture(t)
	d := &verifiedFake{}
	report := RunGraphVerified(context.Background(), steps, GraphCertificate{}, func(context.Context) (GraphSnapshot, error) { return snapshot, nil }, d, func() bool { return true }, func() bool { return true })
	if !report.Aborted || report.Mutated || len(d.calls) != 0 {
		t.Fatalf("missing graph certificate must block before preflight or writes: %#v %v", report, d.calls)
	}
}

func TestGraphVerifiedRollsBackAppliedPrefixAfterFailure(t *testing.T) {
	steps, cert, snapshot := graphVerifiedFixture(t)
	d := &verifiedFake{applyError: "worker"}
	rollbackAsked := false
	report := RunGraphVerified(context.Background(), steps, cert, func(context.Context) (GraphSnapshot, error) { return snapshot, nil }, d, func() bool { return true }, func() bool { rollbackAsked = true; return true })
	if !report.Failed || !report.Mutated || !rollbackAsked || !report.Steps[0].RolledBack || report.Steps[1].Applied {
		t.Fatalf("failed graph saga did not restore only its applied prefix: %#v calls=%v", report, d.calls)
	}
}

func TestGraphVerifiedRollsBackAfterLiveRolloutFailure(t *testing.T) {
	steps, cert, snapshot := graphVerifiedFixture(t)
	d := &verifiedFake{classes: map[string]string{"api": ops.RolloutUnknown}}
	rollbackAsked := false
	report := RunGraphVerified(context.Background(), steps, cert, func(context.Context) (GraphSnapshot, error) { return snapshot, nil }, d, func() bool { return true }, func() bool { rollbackAsked = true; return true })
	if !report.Failed || !rollbackAsked || !report.Steps[0].RolledBack || containsCall(d.calls, "apply worker") {
		t.Fatalf("rollout failure must stop later applies and offer rollback: %#v calls=%v", report, d.calls)
	}
}

func TestGraphVerifiedKeepsAppliedPrefixWhenRollbackDeclined(t *testing.T) {
	steps, cert, snapshot := graphVerifiedFixture(t)
	d := &verifiedFake{classes: map[string]string{"api": ops.RolloutUnknown}}
	report := RunGraphVerified(context.Background(), steps, cert, func(context.Context) (GraphSnapshot, error) { return snapshot, nil }, d, func() bool { return true }, func() bool { return false })
	if !report.Failed || !report.Mutated || report.Steps[0].RolledBack || report.Steps[0].RollbackStatus != "available" {
		t.Fatalf("rollback refusal should preserve explicit applied prefix: %#v", report)
	}
}

func containsCall(calls []string, prefix string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}
func TestSecondStepFailureOffersReverseFieldRollback(t *testing.T) {
	d := &verifiedFake{classes: map[string]string{"b": ops.RolloutUnknown}}
	report := RunVerified(context.Background(), []CertifiedStep{certifiedFixture(t, "a"), certifiedFixture(t, "b")}, d, func() bool { return true }, func() bool { return true })
	if !report.Steps[0].RolledBack || !report.Steps[1].RolledBack {
		t.Fatal(report)
	}
	calls := strings.Join(d.calls, ";")
	if !strings.HasSuffix(calls, "restore b;restore a") {
		t.Fatal(calls)
	}
}
func TestCoordinateRejectsDuplicateAndFutureOnlyStep(t *testing.T) {
	a := certifiedFixture(t, "a")
	future := certifiedFixture(t, "b")
	future.Step.Kind = "CronJob"
	for _, steps := range [][]CertifiedStep{{a, a}, {a, future}} {
		d := &verifiedFake{}
		report := RunVerified(context.Background(), steps, d, func() bool { return true }, func() bool { return true })
		if !report.Aborted || len(d.calls) > 0 {
			t.Fatal(report, d.calls)
		}
	}
}
func TestRollbackConsentAndPartialFailureAreTruthful(t *testing.T) {
	d := &verifiedFake{classes: map[string]string{"b": ops.RolloutUnknown}, restoreError: "b"}
	report := RunVerified(context.Background(), []CertifiedStep{certifiedFixture(t, "a"), certifiedFixture(t, "b")}, d, func() bool { return true }, func() bool { return true })
	if report.Steps[1].RolledBack || report.Steps[1].RollbackStatus != "refused-or-failed" || !report.Steps[0].RolledBack {
		t.Fatal(report)
	}
}

func TestCoordinateCancellationAtConfirmationMutatesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &verifiedFake{}
	report := RunVerified(ctx, []CertifiedStep{certifiedFixture(t, "a"), certifiedFixture(t, "b")}, d, func() bool { cancel(); return true }, func() bool { return false })
	if !report.Aborted || report.Mutated || strings.Contains(strings.Join(d.calls, ";"), "apply") {
		t.Fatal(report, d.calls)
	}
}

func TestCoordinateFailedApplyReportsUnconfirmedOutcome(t *testing.T) {
	d := &verifiedFake{applyError: "b"}
	report := RunVerified(context.Background(), []CertifiedStep{certifiedFixture(t, "a"), certifiedFixture(t, "b")}, d, func() bool { return true }, func() bool { return false })
	if !report.Failed || report.Steps[1].Applied || !strings.Contains(ReportText(report), "apply-unconfirmed") {
		t.Fatal(report, ReportText(report))
	}
}
