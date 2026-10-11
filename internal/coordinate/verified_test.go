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
