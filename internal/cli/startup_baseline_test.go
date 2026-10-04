package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
)

type baselineReader struct {
	kube.Reader
	object map[string]any
}

func (r baselineReader) GetResource(_ context.Context, _, _ string) (map[string]any, error) {
	return r.object, nil
}

const validStartupBaseline = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: worker
  namespace: demo
spec:
  template:
    spec:
      containers:
      - name: app
        image: busybox:1.36
        command: [httpd, -f, -p, "8080"]
`

func startupFixture(t *testing.T, baseline string) (string, baselineReader, analyzer.Finding, fix.Plan) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "baseline.yaml")
	if err := os.WriteFile(p, []byte(baseline), 0600); err != nil {
		t.Fatal(err)
	}
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "worker", Namespace: "demo", Status: "CrashLoopBackOff"}
	c := map[string]any{"name": "app", "image": "busybox:1.36", "command": []any{"sh", "-c", "exit 1"}}
	spec := map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{c}}}}
	r := baselineReader{object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "worker", "namespace": "demo"}, "spec": spec}}

	return p, r, f, fix.BuildPlan(f)
}
func TestStartupBaselineProducesOnlyOperatorStartupPatch(t *testing.T) {
	path, r, f, p := startupFixture(t, validStartupBaseline)
	got, err := withStartupBaseline(context.Background(), r, path, f, p)
	if err != nil || !got.ApplyEligible || got.Strategy != "restore-command" {
		t.Fatalf("got %+v error %v", got, err)
	}
	for _, part := range []string{"httpd", "args: []", "command:", "name: app"} {
		if !strings.Contains(got.PatchTemplate, part) {
			t.Fatalf("missing %q: %s", part, got.PatchTemplate)
		}
	}
	for _, part := range []string{"image:", "securityContext:", "readinessProbe:"} {
		if strings.Contains(got.PatchTemplate, part) {
			t.Fatalf("unexpected %q: %s", part, got.PatchTemplate)
		}
	}
}
func TestStartupBaselineRejectsUnprovenIdentityAndSecretReferences(t *testing.T) {
	for _, bad := range []string{strings.Replace(validStartupBaseline, "name: worker", "name: other", 1), strings.Replace(validStartupBaseline, "busybox:1.36", "busybox:latest", 1), validStartupBaseline + "---\nkind: Secret\n", strings.Replace(validStartupBaseline, "command: [httpd, -f, -p, \"8080\"]", "command: null", 1), validStartupBaseline + "        envFrom: [{secretRef: {name: config}}]\n", strings.Replace(validStartupBaseline, "kind: Deployment", "kind: Pod", 1), strings.Replace(validStartupBaseline, "namespace: demo", "namespace: other", 1)} {
		path, r, f, p := startupFixture(t, bad)
		if _, err := withStartupBaseline(context.Background(), r, path, f, p); err == nil {
			t.Fatalf("accepted invalid baseline: %s", bad)
		}
	}
}

func TestStartupBaselineRejectsChangedLiveIdentity(t *testing.T) {
	for _, scenario := range []string{"namespace", "image", "container", "second-container", "nil-command", "missing-path", "nil-reader", "plan-target", "secret-volume", "empty-command", "numeric-args"} {
		baseline := validStartupBaseline
		if scenario == "secret-volume" {
			baseline += "      volumes: [{name: credentials, secret: {secretName: credentials}}]\n"
		}
		if scenario == "empty-command" {
			baseline = strings.Replace(baseline, "command: [httpd, -f, -p, \"8080\"]", "command: []", 1)
		}
		if scenario == "numeric-args" {
			baseline += "        args: [8080]\n"
		}
		path, r, f, p := startupFixture(t, baseline)
		containers := r.object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		container := containers["containers"].([]any)[0].(map[string]any)
		var reader kube.Reader = r
		switch scenario {
		case "namespace":
			r.object["metadata"].(map[string]any)["namespace"] = "other"
		case "image":
			container["image"] = "busybox:latest"
		case "container":
			container["name"] = "other"
		case "second-container":
			containers["containers"] = append(containers["containers"].([]any), map[string]any{"name": "sidecar", "image": "busybox:1.36"})
		case "nil-command":
			container["command"] = nil
		case "missing-path":
			path = ""
		case "nil-reader":
			reader = nil
		case "plan-target":
			p.Resource = "Deployment/other"
		}
		if _, err := withStartupBaseline(context.Background(), reader, path, f, p); err == nil {
			t.Fatalf("accepted %s", scenario)
		}
	}
}

func TestStartupBaselineKeepsRecoveredWorkloadReviewOnly(t *testing.T) {
	path, r, f, p := startupFixture(t, validStartupBaseline)
	f.Recovered = true
	got, err := withStartupBaseline(context.Background(), r, path, f, p)
	if err != nil || got.ApplyEligible || got.CanApply || len(got.BlockedReasons) == 0 {
		t.Fatalf("recovered plan: %+v error %v", got, err)
	}
}

func TestStartupBaselineDeclinesUnchangedStartup(t *testing.T) {
	path, r, f, p := startupFixture(t, validStartupBaseline)
	c := r.object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	c["command"] = []any{"httpd", "-f", "-p", "8080"}
	if _, err := withStartupBaseline(context.Background(), r, path, f, p); err == nil {
		t.Fatal("unchanged startup should not be a repair")
	}
}
