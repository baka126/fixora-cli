package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
	"github.com/fixora/kubectl-fixora/internal/repo"
	"gopkg.in/yaml.v3"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHelmCLIOrderedExplicitFlags(t *testing.T) {
	o, _, err := parseFlags([]string{"--repo", "/tmp/source", "--helm-chart", "chart", "--helm-release", "example", "--helm-values", "base.yaml", "--helm-values", "overlay.yaml", "--helm-edit-values", "overlay.yaml", "--helm-render-mode", "upgrade", "--helm-kube-version", "1.33.0", "--helm-api-version", "apps/v1", "--helm-map", "/spec/template/spec/containers/0/image=/custom"})
	if err != nil {
		t.Fatal(err)
	}
	c, m, err := helmSourceContext(o, "app")
	if err != nil {
		t.Fatal(err)
	}
	if c.Release != "example" || c.Namespace != "app" || c.RenderMode != "upgrade" || !reflect.DeepEqual(c.ValuesFiles, []string{"base.yaml", "overlay.yaml"}) || len(m) != 1 {
		t.Fatal(c, m)
	}
}

type helmCLIStub struct {
	live             map[string]any
	renders, shadows int
}

func (d *helmCLIStub) Renderer() repo.HelmRenderer { return d }
func (d *helmCLIStub) Render(_ context.Context, _ repo.HelmRenderContext, _ *repo.HelmInputs, b []byte) ([]byte, error) {
	d.renders++
	target := helmCLITarget
	if bytes.Contains(b, []byte("choice: new")) {
		target = strings.Replace(target, "image: old", "image: new", 1)
	}
	return []byte(target), nil
}
func (d *helmCLIStub) Lint(context.Context, repo.HelmRenderContext, *repo.HelmInputs, []byte) error {
	return nil
}
func (d *helmCLIStub) ReadTarget(context.Context, repo.ResourceKey) (map[string]any, error) {
	return d.live, nil
}
func (d *helmCLIStub) Scope(context.Context, repo.ResourceKey) (bool, error) { return true, nil }
func (d *helmCLIStub) ServerValidate(_ context.Context, s string) (repo.HelmValidation, error) {
	var obj map[string]any
	err := yaml.Unmarshal([]byte(s), &obj)
	return repo.HelmValidation{Status: "passed", Admitted: obj}, err
}
func (d *helmCLIStub) ShadowVerify(context.Context, fix.Plan, string) (*repairpolicy.Receipt, error) {
	d.shadows++
	return nil, errors.New("unexpected shadow")
}

const helmCLITarget = "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: app, namespace: demo}\nspec:\n  template:\n    spec:\n      containers:\n      - {name: worker, image: old}\n"

func helmCLIRequest(t *testing.T) (repo.HelmSourceRequest, *helmCLIStub, string) {
	t.Helper()
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "chart"), 0700)
	os.WriteFile(filepath.Join(root, "chart", "Chart.yaml"), []byte("apiVersion: v2\nname: fixture\nversion: 1.0.0\n"), 0600)
	dest := filepath.Join(root, "chart", "values.yaml")
	os.WriteFile(dest, []byte("choice: old\n# preserved\nprivate: synthetic-unrelated\n"), 0640)
	var live map[string]any
	yaml.Unmarshal([]byte(helmCLITarget), &live)
	live["metadata"].(map[string]any)["uid"] = "uid"
	req := repo.HelmSourceRequest{Context: repo.HelmRenderContext{RepoPath: root, ChartPath: "chart", Release: "fixture", Namespace: "demo", EditValuesPath: "chart/values.yaml", RenderMode: "install"}, Finding: analyzer.Finding{ResourceKind: "Deployment", ResourceName: "app", Namespace: "demo", ResourceUID: "uid"}, Plan: fix.Plan{Resource: "Deployment/app", Namespace: "demo", Strategy: "image", TargetUID: "uid", PatchTemplate: strings.Replace(helmCLITarget, "image: old", "image: new", 1)}}
	req.Finding.GitOps.ManagedBy = "helm"
	req.Plan.Confidence = 99
	req.Plan.BlockedReasons = []string{fix.SourceOwnershipReviewReason}
	return req, &helmCLIStub{live: live}, dest
}
func TestHelmCLIPreviewNoWriteEvenWithYes(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required")
	}
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			req, d, dest := helmCLIRequest(t)
			before, _ := os.ReadFile(dest)
			var out, errout bytes.Buffer
			code := deliverHelmSource(context.Background(), &out, &errout, options{output: format, preview: format == "text", yes: true, sourcePatch: true, commit: true}, req, d)
			after, _ := os.ReadFile(dest)
			if code != 0 || !bytes.Equal(before, after) || d.shadows != 0 {
				t.Fatal(code, out.String(), errout.String())
			}
			if strings.Contains(out.String(), "synthetic-unrelated") || strings.Contains(out.String(), "shadow-verified") {
				t.Fatal(out.String())
			}
		})
	}
}
func TestHelmCLISourceReviewClaimsAndConsent(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required")
	}
	req, d, dest := helmCLIRequest(t)
	var out, errout bytes.Buffer
	opts := options{output: "text", sourcePatch: true}
	if code := deliverHelmSource(context.Background(), &out, &errout, opts, req, d); code != 2 {
		t.Fatal(code)
	}
	opts.yes = true
	out.Reset()
	errout.Reset()
	if code := deliverHelmSource(context.Background(), &out, &errout, opts, req, d); code != 0 {
		t.Fatal(code, errout.String())
	}
	after, _ := os.ReadFile(dest)
	if !bytes.Contains(after, []byte("choice: new")) || !strings.Contains(out.String(), "reconciliation: pending") || d.shadows != 0 {
		t.Fatal(out.String())
	}
}

type helmDiscoveryStub struct{ baselineReader }

func (helmDiscoveryStub) Run(_ context.Context, args ...string) ([]byte, error) {
	if reflect.DeepEqual(args, []string{"get", "--raw", "/version"}) {
		return []byte(`{"gitVersion":"v1.33.0"}`), nil
	}
	if reflect.DeepEqual(args, []string{"api-versions"}) {
		return []byte("v1\napps/v1\n"), nil
	}
	if reflect.DeepEqual(args, []string{"get", "--raw", "/apis/apps/v1"}) {
		return []byte(`{"resources":[{"name":"deployments","kind":"Deployment","namespaced":true}]}`), nil
	}
	if reflect.DeepEqual(args, []string{"get", "--raw", "/api/v1"}) {
		return []byte(`{"resources":[{"name":"namespaces","kind":"Namespace","namespaced":false}]}`), nil
	}
	return nil, errors.New("synthetic discovery unavailable")
}
func TestHelmCLICapabilitiesIncludeKindChecks(t *testing.T) {
	var c repo.HelmRenderContext
	if err := helmCapabilities(context.Background(), helmDiscoveryStub{}, &c); err != nil {
		t.Fatal(err)
	}
	if !hasString(c.APIVersions, "apps/v1/Deployment") || !hasString(c.APIVersions, "v1/Namespace") {
		t.Fatal(c.APIVersions)
	}
}

func TestHelmCLIPRFileScopeAndRemoteFailure(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required")
	}
	req, d, dest := helmCLIRequest(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", req.Context.RepoPath}, args...)...)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(string(b), err)
		}
		return strings.TrimSpace(string(b))
	}
	git("init")
	git("config", "user.name", "Synthetic")
	git("config", "user.email", "test@example.invalid")
	git("add", "chart")
	git("commit", "-m", "initial")
	var out, errout bytes.Buffer
	o := options{output: "text", yes: true, delivery: "pr", visited: map[string]bool{"delivery": true}, branch: "fixora/source-test"}
	if code := deliverHelmSource(context.Background(), &out, &errout, o, req, d); code != 1 || !strings.Contains(errout.String(), "source committed; PR delivery incomplete") {
		t.Fatal(code, out.String(), errout.String())
	}
	changed := git("diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	if changed != "chart/values.yaml" {
		t.Fatal(changed)
	}
	data, _ := os.ReadFile(dest)
	if !bytes.Contains(data, []byte("choice: new")) {
		t.Fatal("local source lost")
	}
	if strings.Contains(out.String(), "PR opened: true") {
		t.Fatal("false PR success")
	}
}
func TestHelmCLINoOpAndCancelHaveNoWrites(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required")
	}
	for _, kind := range []string{"noop", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			req, d, dest := helmCLIRequest(t)
			ctx := context.Background()
			if kind == "noop" {
				req.Plan.PatchTemplate = helmCLITarget
			} else {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before, _ := os.ReadFile(dest)
			var out, errout bytes.Buffer
			code := deliverHelmSource(ctx, &out, &errout, options{output: "text", yes: true, sourcePatch: true, commit: true}, req, d)
			after, _ := os.ReadFile(dest)
			if !bytes.Equal(before, after) || d.shadows != 0 {
				t.Fatal("side effect")
			}
			if kind == "noop" && (code != 0 || !strings.Contains(out.String(), "source-already-correct")) {
				t.Fatal(code, out.String(), errout.String())
			}
			if kind == "cancel" && code == 0 {
				t.Fatal("cancellation ignored")
			}
		})
	}
}

type helmCLIPostWriteStub struct {
	*helmCLIStub
	dest string
}

func (d helmCLIPostWriteStub) ReadTarget(ctx context.Context, key repo.ResourceKey) (map[string]any, error) {
	raw, _ := json.Marshal(d.live)
	var obj map[string]any
	json.Unmarshal(raw, &obj)
	b, _ := os.ReadFile(d.dest)
	if bytes.Contains(b, []byte("choice: new")) {
		obj["metadata"].(map[string]any)["uid"] = "replacement"
	}
	return obj, nil
}
func TestHelmCLIPostWriteFailureReportsSourceChange(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required")
	}
	req, base, dest := helmCLIRequest(t)
	d := helmCLIPostWriteStub{base, dest}
	var out, errout bytes.Buffer
	code := deliverHelmSource(context.Background(), &out, &errout, options{output: "text", yes: true, sourcePatch: true, commit: true}, req, d)
	if code != 1 || !strings.Contains(errout.String(), "Source was written") || !strings.Contains(errout.String(), "No commit or PR was attempted") {
		t.Fatal(code, errout.String())
	}
}
func TestHelmCLIHealthySkipsSourceWork(t *testing.T) {
	var out, errout bytes.Buffer
	code := runHelmSourceDelivery(context.Background(), &out, &errout, options{output: "text", yes: true, commit: true}, nil, kube.Kubectl{}, analyzer.Finding{Status: "OwnedPodsHealthy", Namespace: "app"}, fix.Plan{Resource: "Deployment/app"})
	if code != 0 || !strings.Contains(out.String(), "healthy") {
		t.Fatal(code, out.String(), errout.String())
	}
}
