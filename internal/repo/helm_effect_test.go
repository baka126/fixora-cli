package repo

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type helmEffectFixture struct {
	live                             map[string]any
	collateral, unstable, serverFail bool
	calls                            int
	serverPending                    bool
	admissionRewrite                 bool
	baselineReplicas                 bool
}

func (d *helmEffectFixture) Renderer() HelmRenderer { return d }
func (d *helmEffectFixture) ReadTarget(context.Context, ResourceKey) (map[string]any, error) {
	return copyHelmObject(d.live), nil
}
func (d *helmEffectFixture) Scope(_ context.Context, k ResourceKey) (bool, error) {
	return testHelmScope(k)
}
func (d *helmEffectFixture) Render(_ context.Context, _ HelmRenderContext, _ *HelmInputs, edited []byte) ([]byte, error) {
	d.calls++
	out := helmImageTarget
	if d.baselineReplicas {
		out = strings.Replace(out, "spec:\n", "spec:\n  replicas: 1\n", 1)
	}
	if edited != nil {
		var values map[string]any
		_ = yaml.Unmarshal(edited, &values)
		if values["setting"] == "new" {
			out = strings.Replace(out, "image: old", "image: new", 1)
		}
		if d.collateral {
			out += "---\napiVersion: v1\nkind: Service\nmetadata: {name: collateral}\nspec: {ports: [{port: 1234}]}\n"
		}
	}
	if d.unstable && d.calls%2 == 0 {
		out = strings.Replace(out, "image: old", "image: changing", 1)
	}
	return []byte(out), nil
}
func (d *helmEffectFixture) Lint(context.Context, HelmRenderContext, *HelmInputs, []byte) error {
	return nil
}
func (d *helmEffectFixture) ServerValidate(_ context.Context, p string) (HelmValidation, error) {
	if d.serverFail {
		return HelmValidation{}, errors.New("synthetic denial")
	}
	if d.serverPending {
		return HelmValidation{Status: "pending"}, nil
	}
	var obj map[string]any
	_ = yaml.Unmarshal([]byte(p), &obj)
	if d.admissionRewrite {
		obj["spec"].(map[string]any)["replicas"] = 99
	}
	return HelmValidation{Status: "passed", Admitted: obj}, nil
}
func (d *helmEffectFixture) ShadowVerify(context.Context, fix.Plan, string) (*repairpolicy.Receipt, error) {
	return nil, errors.New("not requested")
}
func helmEffectRequest(t *testing.T) (HelmSourceRequest, *helmEffectFixture) {
	c := helmSourceFixture(t)
	var live map[string]any
	_ = yaml.Unmarshal([]byte(helmImageTarget), &live)
	live["metadata"].(map[string]any)["uid"] = "owner"
	return HelmSourceRequest{Context: c, Finding: analyzer.Finding{ResourceKind: "Deployment", ResourceName: "worker", Namespace: "app", ResourceUID: "owner"}, Plan: fix.Plan{Resource: "Deployment/worker", Namespace: "app", Strategy: "image", ApplyEligible: true, CanApply: true, TargetUID: "owner", PatchTemplate: helmImagePatch}}, &helmEffectFixture{live: live}
}
func TestHelmEffectExactTargetAndUnchangedInventory(t *testing.T) {
	req, d := helmEffectRequest(t)
	plan, err := PrepareHelmSource(context.Background(), req, d)
	if err != nil || plan.Proof == nil || plan.Status != "source-validated" || plan.ServerValidation != "passed" {
		t.Fatal(plan, err)
	}
	if err = plan.Proof.CheckFresh(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"collateral", "unstable", "server"} {
		t.Run(bad, func(t *testing.T) {
			req, d := helmEffectRequest(t)
			switch bad {
			case "collateral":
				d.collateral = true
			case "unstable":
				d.unstable = true
			case "server":
				d.serverFail = true
			}
			plan, err := PrepareHelmSource(context.Background(), req, d)
			if err == nil && plan.Proof != nil {
				t.Fatal("unsafe effect certified", plan)
			}
		})
	}
}
func TestHelmEffectFreshnessAndReplay(t *testing.T) {
	req, d := helmEffectRequest(t)
	plan, err := PrepareHelmSource(context.Background(), req, d)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(plan)
	var saved HelmSourcePlan
	_ = json.Unmarshal(data, &saved)
	if saved.Proof != nil {
		t.Fatal("proof serialized")
	}
	if strings.Contains(string(data), "other: untouched") {
		t.Fatal("unrelated values exposed")
	}
	d.live["metadata"].(map[string]any)["uid"] = "replacement"
	if plan.Proof.CheckFresh(context.Background(), d) == nil {
		t.Fatal("replacement accepted")
	}
}
func TestHelmEffectPendingAndRuntimeNotForged(t *testing.T) {
	req, d := helmEffectRequest(t)
	d.serverPending = true
	plan, err := PrepareHelmSource(context.Background(), req, d)
	if err != nil || plan.ServerValidation != "pending" || plan.RuntimeVerification == "shadow-verified" {
		t.Fatal(plan, err)
	}
	req.VerifyRuntime = true
	if plan, err := PrepareHelmSource(context.Background(), req, d); err == nil && plan.Proof != nil {
		t.Fatal("missing runtime receipt accepted")
	}
}

func TestHelmEffectUniqueMappingIgnoresRedundantUnusedKeys(t *testing.T) {
	req, d := helmEffectRequest(t)
	os.WriteFile(filepath.Join(req.Context.RepoPath, req.Context.EditValuesPath), []byte("setting: old\nunrelated: old\n"), 0640)
	plan, err := PrepareHelmSource(context.Background(), req, d)
	if err != nil || plan.Proof == nil {
		t.Fatal(plan, err)
	}
}
func TestHelmEffectCannotCreateAnotherContainer(t *testing.T) {
	req, d := helmEffectRequest(t)
	req.Plan.PatchTemplate = strings.Replace(helmImagePatch, "name: app", "name: other", 1)
	_, err := PrepareHelmSource(context.Background(), req, d)
	if err == nil {
		t.Fatal("unknown container candidate accepted")
	}
}

func TestHelmEffectRefusesAdmissionRewritesOutsidePodSpec(t *testing.T) {
	req, d := helmEffectRequest(t)
	d.admissionRewrite = true
	// Make the baseline contain an explicit replica count, not merely a server default.
	d.live["spec"].(map[string]any)["replicas"] = 1
	d.baselineReplicas = true
	plan, err := PrepareHelmSource(context.Background(), req, d)
	if err == nil || plan.Proof != nil {
		t.Fatal("admission rewritten replica count certified")
	}
}

func TestHelmEffectPreservesNonSourcePolicyBlockers(t *testing.T) {
	for _, bad := range []string{"recovered", "capacity", "ownership", "not-source"} {
		t.Run(bad, func(t *testing.T) {
			req, d := helmEffectRequest(t)
			switch bad {
			case "recovered":
				req.Plan.Recovered = true
			case "capacity":
				req.Plan.BlockedReasons = []string{"DaemonSet fleet capacity requires review"}
			case "ownership":
				req.Plan.Guardrails = []string{"ownership-unverified"}
			case "not-source":
				req.Plan.ApplyEligible = false
				req.Plan.CanApply = false
			}
			plan, err := PrepareHelmSource(context.Background(), req, d)
			if err == nil || plan.Proof != nil {
				t.Fatal("blocked candidate certified")
			}
		})
	}
}
