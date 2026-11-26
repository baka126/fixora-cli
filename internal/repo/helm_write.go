package repo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
)

// WriteValidatedHelmSource writes only bytes bound by the in-memory certificate.
// Review delivery may omit runtime verification, but never bypass render validation.
func WriteValidatedHelmSource(ctx context.Context, plan HelmSourcePlan, d HelmSourceDeps, reviewOnly bool) (SourcePatch, error) {
	out := SourcePatch{Mode: "helm", Reconciliation: "pending"}
	p := plan.Proof
	if p == nil || plan.Status != "source-validated" || p.inputs == nil || plan.Path != p.inputs.context.EditValuesPath || plan.Preview != p.preview || !reflect.DeepEqual(plan.ChangedKeys, p.keys) || plan.ServerValidation != p.server || plan.RuntimeVerification != p.runtime {
		return out, errors.New("unchanged certified Helm source plan required")
	}
	if !reviewOnly && (!p.request.Plan.ApplyEligible || !p.request.Plan.CanApply || p.server != "passed" || p.runtime != "shadow-verified") {
		return out, errors.New("automatic source delivery requires admission and shadow verification")
	}
	if err := p.CheckFresh(ctx, d); err != nil {
		return out, err
	}
	dest := filepath.Join(p.inputs.context.RepoPath, p.inputs.context.EditValuesPath)
	lock, err := os.OpenFile(dest+".fixora.lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return out, errors.New("source write lock unavailable")
	}
	lock.Close()
	defer os.Remove(dest + ".fixora.lock")
	// Lock and temporary files must not enter the captured chart inventory.
	info, err := os.Stat(dest)
	if err != nil {
		return out, err
	}
	temp, err := os.CreateTemp(filepath.Dir(dest), ".fixora-values-*")
	if err != nil {
		return out, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err = temp.Chmod(info.Mode().Perm()); err == nil {
		_, err = temp.Write(p.candidate.edited)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return out, err
	}
	// Exclude our own cooperative transaction files during freshness checks.
	if err = p.checkWriteFresh(ctx, d, []string{dest + ".fixora.lock", tempPath}, false); err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if err = os.Rename(tempPath, dest); err != nil {
		return out, err
	}
	out.Path = dest
	out.Preview = p.preview
	out.SourceStatus = "source-written"
	out.ServerValidation = p.server
	out.RuntimeVerification = p.runtime
	out.Actions = []string{"Write certified Helm values; deployment reconciliation is pending"}
	if err = p.checkWriteFresh(ctx, d, []string{dest + ".fixora.lock"}, true); err != nil {
		return out, err
	}
	return out, nil
}

// CheckWritten revalidates the exact source bytes before a source commit.
func (p HelmSourceProof) CheckWritten(ctx context.Context, d HelmSourceDeps) error {
	return p.checkWriteFresh(ctx, d, nil, true)
}
func (p HelmSourceProof) checkWriteFresh(ctx context.Context, d HelmSourceDeps, ignore []string, written bool) error {
	if p.inputs == nil || p.binding == "" || p.binding != p.contentBinding() || d == nil {
		return errors.New("fresh source certificate required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	files, err := captureHelmFiles(p.inputs.context)
	if err != nil {
		return err
	}
	for _, path := range ignore {
		rel, err := filepath.Rel(p.inputs.context.RepoPath, path)
		if err != nil {
			return err
		}
		delete(files, rel)
	}
	expected := make(map[string][]byte, len(p.inputs.files))
	for k, v := range p.inputs.files {
		expected[k] = v
	}
	if written {
		expected[p.inputs.context.EditValuesPath] = p.candidate.edited
	}
	if helmHash(files) != helmHash(expected) {
		return errors.New("Helm source changed during delivery")
	}
	hash, err := hashHelmFile(p.inputs.renderer)
	if err != nil || hash != p.inputs.rendererHash {
		return errors.New("Helm executable changed")
	}
	live, err := d.ReadTarget(ctx, p.key)
	if err != nil {
		return errors.New("live target unreadable")
	}
	binding, err := helmLiveBinding(live, p.key, p.request)
	if err != nil || binding != p.liveBinding {
		return errors.New("live target changed during source delivery")
	}
	if p.runtime == "shadow-verified" {
		return p.receipt.Check(p.runtimeProof, p.request.Plan.PatchYAML())
	}
	return nil
}
