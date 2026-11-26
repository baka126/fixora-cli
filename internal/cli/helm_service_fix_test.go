package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
	"github.com/fixora/kubectl-fixora/internal/repo"
	"github.com/fixora/kubectl-fixora/internal/servicefix"
	"gopkg.in/yaml.v3"
)

type serviceHelmRenderer struct{ before map[string]any }

func (r serviceHelmRenderer) Render(_ context.Context, _ repo.HelmRenderContext, _ *repo.HelmInputs, edited []byte) ([]byte, error) {
	obj := map[string]any{}
	encoded, _ := json.Marshal(r.before)
	_ = json.Unmarshal(encoded, &obj)
	if len(edited) > 0 {
		var values map[string]any
		if err := yaml.Unmarshal(edited, &values); err != nil {
			return nil, err
		}
		spec := obj["spec"].(map[string]any)
		if selector, ok := values["selector"].(map[string]any); ok {
			spec["selector"] = selector
		}
		if backend, ok := values["backend"]; ok {
			ports := spec["ports"].([]any)
			ports[0].(map[string]any)["targetPort"] = backend
		}
	}
	out, err := yaml.Marshal(obj)
	return out, err
}
func (serviceHelmRenderer) Lint(context.Context, repo.HelmRenderContext, *repo.HelmInputs, []byte) error {
	return nil
}

type serviceHelmDeps struct {
	renderer repo.HelmRenderer
	live     map[string]any
	server   bool
}

func (d *serviceHelmDeps) Renderer() repo.HelmRenderer { return d.renderer }
func (d *serviceHelmDeps) ReadTarget(context.Context, repo.ResourceKey) (map[string]any, error) {
	encoded, _ := json.Marshal(d.live)
	var out map[string]any
	_ = json.Unmarshal(encoded, &out)
	return out, nil
}
func (d *serviceHelmDeps) Scope(context.Context, repo.ResourceKey) (bool, error) { return true, nil }
func (d *serviceHelmDeps) ServerValidate(_ context.Context, raw string) (repo.HelmValidation, error) {
	if !d.server {
		return repo.HelmValidation{}, errors.New("synthetic server validation failure")
	}
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(raw), &obj); err != nil {
		return repo.HelmValidation{}, err
	}
	return repo.HelmValidation{Status: "passed", Admitted: obj}, nil
}
func (*serviceHelmDeps) ShadowVerify(context.Context, fix.Plan, string) (*repairpolicy.Receipt, error) {
	return nil, errors.New("unexpected shadow verification")
}

func helmServiceCLIFixture(t *testing.T) (options, *serviceCLIStub, analyzer.Finding, servicefix.Plan, repo.HelmSourceRequest, *serviceHelmDeps) {
	t.Helper()
	o, runtime, finding := serviceCLISetup(t)
	policy, err := servicefix.LoadPolicy(o.servicePolicy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := servicefix.Prepare(context.Background(), runtime, policy, finding, "")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	chart := filepath.Join(root, "chart")
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{
		filepath.Join(chart, "Chart.yaml"):                "apiVersion: v2\nname: fixture\nversion: 1.0.0\n",
		filepath.Join(chart, "values.yaml"):               "selector: {component: wrong}\nbackend: 9090\n",
		filepath.Join(chart, "templates", "service.yaml"): "# fixture render is provided by test renderer\n",
	} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", root}, {"-C", root, "config", "user.name", "Fixora Tests"}, {"-C", root, "config", "user.email", "fixora-tests@example.invalid"}, {"-C", root, "add", "."}, {"-C", root, "commit", "-qm", "fixture"}} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("initialize fixture git repository: %v: %s", err, output)
		}
	}
	bin := t.TempDir()
	helmPath := filepath.Join(bin, "helm")
	if err := os.WriteFile(helmPath, []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo v3.16.0; exit 0; fi\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var live map[string]any
	raw := `apiVersion: v1
kind: Service
metadata:
  name: entrance
  namespace: team
  uid: service
spec:
  type: ClusterIP
  selector: {component: wrong}
  ports:
  - {name: public, port: 80, protocol: TCP, targetPort: 9090}
`
	if err := yaml.Unmarshal([]byte(raw), &live); err != nil {
		t.Fatal(err)
	}
	ctx := repo.HelmRenderContext{RepoPath: root, ChartPath: "chart", Release: "fixture", Namespace: "team", ValuesFiles: []string{"chart/values.yaml"}, EditValuesPath: "chart/values.yaml", RenderMode: "upgrade", KubeVersion: "1.33.0", APIVersions: []string{"v1/Service"}}
	request := repo.HelmSourceRequest{Context: ctx, Finding: finding, Mappings: []repo.HelmMapping{{FieldPointer: "/spec/selector", ValuesPointer: "/selector"}, {FieldPointer: "/spec/ports/0/targetPort", ValuesPointer: "/backend"}}}
	deps := &serviceHelmDeps{renderer: serviceHelmRenderer{before: live}, live: live, server: true}
	o.repoPath = root
	o.helmChart, o.helmRelease, o.helmEditValues, o.helmRenderMode = ctx.ChartPath, ctx.Release, ctx.EditValuesPath, ctx.RenderMode
	o.helmValues = listFlag{ctx.EditValuesPath}
	o.helmMappings = listFlag{"/spec/selector=/selector", "/spec/ports/0/targetPort=/backend"}
	return o, runtime, finding, plan, request, deps
}

func TestHelmServiceSourcePreviewIsReadOnlyAndOmitsValues(t *testing.T) {
	o, runtime, finding, servicePlan, request, deps := helmServiceCLIFixture(t)
	o.preview = true
	var out, errout strings.Builder
	if code := runHelmServiceSource(context.Background(), &out, &errout, o, runtime, finding, servicePlan, request, deps); code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), errout.String())
	}
	if !strings.Contains(out.String(), "/selector") || !strings.Contains(out.String(), "reconciliation: pending") || !strings.Contains(out.String(), "Temporary route verification:") || strings.Contains(out.String(), "component: api") || runtime.creates == 0 || runtime.deletes == 0 {
		t.Fatalf("unsafe or incomplete preview: %s creates=%d", out.String(), runtime.creates)
	}
}

func TestHelmServicePreviewWithoutYesDoesNotCreateVerifier(t *testing.T) {
	o, runtime, finding, servicePlan, request, deps := helmServiceCLIFixture(t)
	o.preview, o.yes = true, false
	var out, errout strings.Builder
	if code := runHelmServiceSource(context.Background(), &out, &errout, o, runtime, finding, servicePlan, request, deps); code != 0 || runtime.creates != 0 || !strings.Contains(out.String(), "pass --yes") {
		t.Fatalf("preview without verifier consent was not read-only: code=%d out=%s err=%s creates=%d", code, out.String(), errout.String(), runtime.creates)
	}
}

func TestHelmServiceStructuredPreviewOmitsRenderedValues(t *testing.T) {
	o, runtime, finding, servicePlan, request, deps := helmServiceCLIFixture(t)
	o.output, o.preview = "json", false
	var out, errout strings.Builder
	if code := runHelmServiceSource(context.Background(), &out, &errout, o, runtime, finding, servicePlan, request, deps); code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), errout.String())
	}
	if !strings.Contains(out.String(), `"changedKeys"`) || !strings.Contains(out.String(), `"reconciliation": "pending"`) || !strings.Contains(out.String(), `"routeCheck": "http-connectivity-contract"`) || strings.Contains(out.String(), "component: api") || runtime.creates == 0 || runtime.deletes == 0 {
		t.Fatalf("structured preview exposed values or wrote resources: %s", out.String())
	}
}

func TestHelmServiceSourceWriteVerifiesRouteAndDoesNotPatchLiveService(t *testing.T) {
	o, runtime, finding, servicePlan, request, deps := helmServiceCLIFixture(t)
	o.preview, o.sourcePatch, o.yes = false, true, true
	var out, errout strings.Builder
	if code := runHelmServiceSource(context.Background(), &out, &errout, o, runtime, finding, servicePlan, request, deps); code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), errout.String())
	}
	if runtime.creates == 0 || runtime.deletes == 0 || runtime.patches != 0 || !strings.Contains(out.String(), "reconciliation is pending") {
		t.Fatalf("unexpected delivery outcome: %s runtime=%+v", out.String(), runtime)
	}
	values, err := os.ReadFile(filepath.Join(request.Context.RepoPath, request.Context.EditValuesPath))
	if err != nil || !strings.Contains(string(values), "component: api") || !strings.Contains(string(values), "backend: web") {
		t.Fatalf("certified values not written: %s err=%v", values, err)
	}
}

func TestHelmServiceRefusesDirtySourceBeforeCreatingVerifier(t *testing.T) {
	for _, tc := range []struct {
		name   string
		staged bool
	}{{"unstaged-values", false}, {"staged-values", true}} {
		t.Run(tc.name, func(t *testing.T) {
			o, runtime, finding, servicePlan, request, deps := helmServiceCLIFixture(t)
			o.preview, o.sourcePatch, o.yes = false, true, true
			valuesPath := filepath.Join(request.Context.RepoPath, request.Context.EditValuesPath)
			valuesBefore, _ := os.ReadFile(valuesPath)
			valuesBefore = append(valuesBefore, []byte("operatorNote: keep-me\n")...)
			if err := os.WriteFile(valuesPath, valuesBefore, 0600); err != nil {
				t.Fatal(err)
			}
			if tc.staged {
				if output, err := exec.Command("git", "-C", request.Context.RepoPath, "add", request.Context.EditValuesPath).CombinedOutput(); err != nil {
					t.Fatalf("stage fixture edit: %v: %s", err, output)
				}
			}
			var out, errout strings.Builder
			if code := runHelmServiceSource(context.Background(), &out, &errout, o, runtime, finding, servicePlan, request, deps); code == 0 || !strings.Contains(errout.String(), "clean working tree") || runtime.creates != 0 {
				t.Fatalf("dirty source was not refused before verification: code=%d out=%s err=%s creates=%d", code, out.String(), errout.String(), runtime.creates)
			}
			valuesAfter, _ := os.ReadFile(valuesPath)
			if string(valuesBefore) != string(valuesAfter) {
				t.Fatal("values changed despite dirty source refusal")
			}
		})
	}
}

func TestHelmServiceClusterDeliveryRemainsBlocked(t *testing.T) {
	o, runtime, finding, servicePlan, request, deps := helmServiceCLIFixture(t)
	o.delivery, o.yes = "cluster", true
	var out, errout strings.Builder
	if code := runHelmServiceSource(context.Background(), &out, &errout, o, runtime, finding, servicePlan, request, deps); code == 0 || runtime.creates != 0 || !strings.Contains(errout.String(), "source-only") {
		t.Fatalf("cluster delivery was not blocked: code=%d out=%s err=%s", code, out.String(), errout.String())
	}
}

func TestHelmServiceInvalidMappingDoesNotWrite(t *testing.T) {
	o, runtime, finding, servicePlan, request, deps := helmServiceCLIFixture(t)
	request.Mappings = []repo.HelmMapping{{FieldPointer: "/spec/type", ValuesPointer: "/type"}}
	o.sourcePatch, o.yes = true, true
	before, _ := os.ReadFile(filepath.Join(request.Context.RepoPath, request.Context.EditValuesPath))
	var out, errout strings.Builder
	if code := runHelmServiceSource(context.Background(), &out, &errout, o, runtime, finding, servicePlan, request, deps); code == 0 || runtime.creates != 0 {
		t.Fatalf("unsafe mapping passed: code=%d out=%s err=%s", code, out.String(), errout.String())
	}
	after, _ := os.ReadFile(filepath.Join(request.Context.RepoPath, request.Context.EditValuesPath))
	if string(before) != string(after) {
		t.Fatal(fmt.Sprintf("source changed on rejected mapping: %s", after))
	}
}

func TestHelmServiceRepoRequiresExplicitRenderContext(t *testing.T) {
	o, runtime, finding, _, _, _ := helmServiceCLIFixture(t)
	o.helmChart, o.helmRelease, o.helmEditValues, o.helmRenderMode = "", "", "", ""
	o.helmValues, o.helmMappings = nil, nil
	o.delivery, o.preview = "pr", false
	var out, errout strings.Builder
	if code := runServiceFixRuntimeWithKubectl(context.Background(), &out, &errout, o, runtime, kube.Kubectl{}, finding); code != 0 || !strings.Contains(out.String(), "explicit chart context") || runtime.creates != 0 {
		t.Fatalf("expected explicit context guidance: code=%d stdout=%s stderr=%s creates=%d", code, out.String(), errout.String(), runtime.creates)
	}
}
