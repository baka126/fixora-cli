package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/coordinate"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/ops"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"gopkg.in/yaml.v3"
)

type certificationReader struct {
	kube.Reader
	objects map[string]map[string]any
	pod     kube.Pod
}

func (r *certificationReader) GetResource(_ context.Context, _, ref string) (map[string]any, error) {
	obj := r.objects[ref]
	if obj == nil {
		return nil, errors.New("not found")
	}
	return obj, nil
}
func (r *certificationReader) GetPod(context.Context, string, string) (kube.Pod, error) {
	return r.pod, nil
}
func certificationFixture(t *testing.T) (*certificationReader, []coordinate.Step) {
	t.Helper()
	r := &certificationReader{objects: map[string]map[string]any{}}
	steps := []coordinate.Step{}
	for _, name := range []string{"a", "b"} {
		obj, _ := kube.WorkloadPatch("Deployment", name, "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "ghcr.io/example/app:v1"}}})
		obj["metadata"].(map[string]any)["uid"] = "owner-" + name
		r.objects["deployment/"+name] = obj
		patch, _ := kube.WorkloadPatch("Deployment", name, "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "ghcr.io/example/app:v2"}}})
		raw, _ := yaml.Marshal(patch)
		f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: name, Namespace: "demo", ResourceUID: "owner-" + name}
		plan := fix.Plan{Resource: "Deployment/" + name, Namespace: "demo", Strategy: "image", TargetUID: "owner-" + name, CanApply: true, ApplyEligible: true, PatchTemplate: string(raw)}
		steps = append(steps, coordinate.Step{Ref: plan.Resource, Kind: "Deployment", Name: name, Namespace: "demo", Finding: f, Plan: plan})
	}
	return r, steps
}
func certificateVerifier(r *certificationReader, calls *[]string, fail string) coordinateShadowVerifier {
	return func(ctx context.Context, _ *kube.TypedClient, req shadow.Request) (shadow.Result, error) {
		*calls = append(*calls, "shadow "+req.Resource)
		if req.Resource == fail {
			return shadow.Result{Verified: false}, nil
		}
		r.pod = kube.Pod{Metadata: kube.ObjectMeta{Name: "shadow", Namespace: "demo", UID: "shadow-" + req.Resource, Labels: map[string]string{"fixora.io/sandbox": "true", "fixora.io/session": "session"}}, Spec: kube.PodSpec{Containers: []kube.Container{{Name: "app", Image: "ghcr.io/example/app:v2"}}}, Status: kube.PodStatus{Phase: "Running", Conditions: []kube.Condition{{Type: "Ready", Status: "True"}}, ContainerStatuses: []kube.ContainerStatus{{Name: "app", Ready: true}}}}
		receipt, err := repairpolicy.VerifyCommon(ctx, r, req.Plan.PolicyProof, &r.pod, req.Patch)
		return shadow.Result{Verified: err == nil, PolicyReceipt: receipt, VerifiedPatch: req.Patch}, err
	}
}

type certificateDelivery struct{ calls *[]string }

func (d certificateDelivery) Preflight(_ context.Context, c coordinate.CertifiedStep) (ops.TargetSnapshot, error) {
	*d.calls = append(*d.calls, "preflight "+c.Step.Ref)
	return ops.TargetSnapshot{UID: c.Step.Plan.TargetUID, ResourceVersion: "1"}, nil
}
func (d certificateDelivery) Apply(_ context.Context, c coordinate.CertifiedStep, _ ops.TargetSnapshot) error {
	*d.calls = append(*d.calls, "apply "+c.Step.Ref)
	return nil
}
func (d certificateDelivery) Verify(context.Context, coordinate.CertifiedStep) (ops.RolloutOutcome, error) {
	return ops.RolloutOutcome{Class: ops.RolloutHealthy}, nil
}
func (d certificateDelivery) RestoreFields(context.Context, coordinate.CertifiedStep) (ops.RolloutOutcome, error) {
	return ops.RolloutOutcome{}, errors.New("not requested")
}
func TestCoordinateAllShadowsPassBeforeFirstApply(t *testing.T) {
	r, steps := certificationFixture(t)
	calls := []string{}
	certified, err := certifyCoordinateWith(context.Background(), r, nil, options{}, steps, certificateVerifier(r, &calls, ""))
	if err != nil {
		t.Fatal(err)
	}
	report := coordinate.RunVerified(context.Background(), certified, certificateDelivery{calls: &calls}, func() bool { return true }, func() bool { return false })
	if report.Aborted || report.Failed || !report.Mutated {
		t.Fatal(report)
	}
	if len(calls) < 4 || calls[0] != "shadow Deployment/a" || calls[1] != "shadow Deployment/b" {
		t.Fatal(calls)
	}
}
func TestCoordinateFailedShadowMutatesNoProductionWorkload(t *testing.T) {
	r, steps := certificationFixture(t)
	calls := []string{}
	certified, err := certifyCoordinateWith(context.Background(), r, nil, options{}, steps, certificateVerifier(r, &calls, "Deployment/b"))
	if err == nil || len(certified) != 0 {
		t.Fatal("failed shadow certified set")
	}
	for _, call := range calls {
		if len(call) >= 5 && call[:5] == "apply" {
			t.Fatal(calls)
		}
	}
}
func TestCoordinateDuplicateAndImmutableTargetsFailBeforeShadows(t *testing.T) {
	for _, bad := range []string{"duplicate", "job", "source", "edited"} {
		r, steps := certificationFixture(t)
		switch bad {
		case "duplicate":
			steps[1] = steps[0]
		case "job":
			steps[1].Kind = "Job"
		case "source":
			steps[1].Finding.GitOps.ManagedBy = "helm"
		case "edited":
			steps[1].Plan.PatchTemplate = "malformed"
		}
		calls := []string{}
		if _, err := certifyCoordinateWith(context.Background(), r, nil, options{}, steps, certificateVerifier(r, &calls, "")); err == nil || len(calls) != 0 {
			t.Fatalf("%s reached shadow execution: %v %v", bad, err, calls)
		}
	}
}

func TestCoordinateProposalShowsCertifiedPatchAndRedactsSecrets(t *testing.T) {
	_, steps := certificationFixture(t)
	steps[0].Plan.PatchTemplate = "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: a\n  namespace: demo\nspec:\n  template:\n    spec:\n      containers:\n      - name: app\n        env:\n        - name: API_TOKEN\n          value: private-test-token\n"
	var out strings.Builder
	writeCertifiedCoordinateProposal(&out, options{redact: true, noColor: true}, []coordinate.CertifiedStep{{Step: steps[0]}})
	if !strings.Contains(out.String(), "Deployment/a") || !strings.Contains(out.String(), "API_TOKEN") || strings.Contains(out.String(), "private-test-token") {
		t.Fatal(out.String())
	}
}

func TestCoordinateRollbackRefusesNewSourceOwnership(t *testing.T) {
	r, steps := certificationFixture(t)
	obj := r.objects["deployment/a"]
	obj["metadata"].(map[string]any)["labels"] = map[string]any{"app.kubernetes.io/managed-by": "Helm"}
	raw, _ := json.Marshal(obj)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "mutation")
	script := "#!/bin/sh\nif [ \"$1\" = get ]; then\ncat <<'FIXORA_OBJECT'\n" + string(raw) + "\nFIXORA_OBJECT\nelse\necho mutation > '" + logPath + "'\nfi\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := (coordinateVerifiedDeps{k: kube.Kubectl{}}).RestoreFields(context.Background(), coordinate.CertifiedStep{Step: steps[0]})
	if err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("new source owner was not checked: %v", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("rollback reached mutation")
	}
}

func TestCoordinateRejectsControllerDependencyCycleBeforeShadows(t *testing.T) {
	r, steps := certificationFixture(t)
	for name, peer := range map[string]string{"a": "b", "b": "a"} {
		r.objects["deployment/"+name]["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": peer, "uid": "owner-" + peer, "controller": true}}
	}
	calls := []string{}
	if _, err := certifyCoordinateWith(context.Background(), r, nil, options{}, steps, certificateVerifier(r, &calls, "")); err == nil || len(calls) != 0 {
		t.Fatalf("dependent cycle reached shadows: %v %v", err, calls)
	}
}

func TestCoordinatePreviewDoesNotInitializeShadowOrDeliver(t *testing.T) {
	r, steps := certificationFixture(t)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing-config"))
	t.Setenv("PATH", t.TempDir())
	var out, errOut strings.Builder
	code := runPreparedCoordinateSteps(context.Background(), &out, &errOut, options{preview: true, yes: true, noColor: true}, r, kube.Kubectl{}, steps)
	if code != 0 || !strings.Contains(out.String(), "candidate") || strings.Contains(out.String(), "; shadow verified") {
		t.Fatalf("preview %d: %s / %s", code, out.String(), errOut.String())
	}
}
