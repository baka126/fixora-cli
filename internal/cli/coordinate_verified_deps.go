package cli

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/coordinate"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/ops"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
)

type coordinateVerifiedDeps struct {
	k       kube.Kubectl
	timeout time.Duration
}
type preflightRecordingReader struct {
	kube.Reader
	target repairpolicy.Target
	object map[string]any
	pod    *kube.Pod
}

func (r *preflightRecordingReader) GetResource(ctx context.Context, ns, ref string) (map[string]any, error) {
	obj, err := r.Reader.GetResource(ctx, ns, ref)
	if err == nil && r.object == nil && ns == r.target.Namespace && strings.EqualFold(ref, r.target.Kind+"/"+r.target.Name) {
		r.object = obj
	}
	return obj, err
}
func (r *preflightRecordingReader) GetPod(ctx context.Context, ns, name string) (kube.Pod, error) {
	pod, err := r.Reader.GetPod(ctx, ns, name)
	if err == nil && name == r.target.PodName && ns == r.target.Namespace {
		r.pod = &pod
	}
	return pod, err
}
func (d coordinateVerifiedDeps) Preflight(ctx context.Context, c coordinate.CertifiedStep) (ops.TargetSnapshot, error) {
	if err := c.Check(); err != nil {
		return ops.TargetSnapshot{}, err
	}
	if err := repairpolicy.Revalidate(ctx, d.k, c.Proof, c.Step.Plan.PatchYAML()); err != nil {
		return ops.TargetSnapshot{}, err
	}
	recorder := &preflightRecordingReader{Reader: d.k, target: c.Proof.Target()}
	snapshot, err := ops.PreflightControllerSnapshot(ctx, recorder, c.Step.Finding, c.Step.Plan)
	if err != nil {
		return snapshot, err
	}
	if err = c.Proof.Check(repairpolicy.Snapshot{Target: c.Proof.Target(), Object: recorder.object, Pod: recorder.pod}, c.Step.Plan.PatchYAML()); err != nil {
		return snapshot, err
	}
	file, cleanup, err := guardedPolicyFile(c.Step.Plan.PatchYAML(), snapshot.UID, snapshot.ResourceVersion)
	if err != nil {
		return snapshot, err
	}
	defer cleanup()
	return snapshot, d.k.DryRunApply(ctx, file)
}
func (d coordinateVerifiedDeps) Apply(ctx context.Context, c coordinate.CertifiedStep, snapshot ops.TargetSnapshot) error {
	if err := c.Check(); err != nil {
		return err
	}
	if snapshot.UID != c.Step.Plan.TargetUID || snapshot.ResourceVersion == "" {
		return errors.New("guarded apply identity/version missing")
	}
	file, cleanup, err := guardedPolicyFile(c.Step.Plan.PatchYAML(), snapshot.UID, snapshot.ResourceVersion)
	if err != nil {
		return err
	}
	defer cleanup()
	return d.k.Apply(ctx, file)
}
func (d coordinateVerifiedDeps) Verify(ctx context.Context, c coordinate.CertifiedStep) (ops.RolloutOutcome, error) {
	outcome := liveOutcome(ctx, d.k, c.Step.Finding, c.Step.Plan, d.timeout)
	if outcome.Class != ops.RolloutHealthy {
		return outcome, nil
	}
	current, err := d.k.GetResource(ctx, c.Step.Namespace, strings.ToLower(c.Step.Kind)+"/"+c.Step.Name)
	if err != nil {
		return ops.RolloutOutcome{Class: ops.RolloutUnknown}, err
	}
	if err = coordinate.CheckAppliedFields(current, c.Step.Plan.PatchYAML(), c.Step.Plan.TargetUID); err != nil {
		return ops.RolloutOutcome{Class: ops.RolloutUnknown}, err
	}
	return outcome, nil
}
func (d coordinateVerifiedDeps) RestoreFields(ctx context.Context, c coordinate.CertifiedStep) (ops.RolloutOutcome, error) {
	current, err := d.k.GetResource(ctx, c.Step.Namespace, strings.ToLower(c.Step.Kind)+"/"+c.Step.Name)
	if err != nil {
		return ops.RolloutOutcome{}, err
	}
	if err = ops.ValidateClusterOwnership(current); err != nil {
		return ops.RolloutOutcome{}, err
	}
	if err = coordinate.CheckRollbackFields(current, c.Before.Object, c.Step.Plan.PatchYAML(), c.Step.Plan.TargetUID); err != nil {
		return ops.RolloutOutcome{}, err
	}
	meta, _ := current["metadata"].(map[string]any)
	version, _ := meta["resourceVersion"].(string)
	if version == "" {
		return ops.RolloutOutcome{}, errors.New("rollback resource version missing")
	}
	file, cleanup, err := guardedPolicyFile(c.InversePatch, c.Step.Plan.TargetUID, version)
	if err != nil {
		return ops.RolloutOutcome{}, err
	}
	defer cleanup()
	if _, err = d.k.Run(ctx, "patch", strings.ToLower(c.Step.Kind)+"/"+c.Step.Name, "-n", c.Step.Namespace, "--type=strategic", "--patch-file", file); err != nil {
		return ops.RolloutOutcome{}, err
	}
	restored, err := d.k.GetResource(ctx, c.Step.Namespace, strings.ToLower(c.Step.Kind)+"/"+c.Step.Name)
	if err != nil {
		return ops.RolloutOutcome{}, err
	}
	if err = coordinate.CheckAppliedFields(restored, c.InversePatch, c.Step.Plan.TargetUID); err != nil {
		return ops.RolloutOutcome{}, err
	}
	return ops.RolloutOutcome{Class: coordinate.RollbackRestored, Summary: "original touched fields restored; original workload incident may remain"}, nil
}
