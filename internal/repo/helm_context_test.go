package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func helmSourceFixture(t *testing.T) HelmRenderContext {
	t.Helper()
	root := t.TempDir()
	for path, data := range map[string]string{"chart/Chart.yaml": "apiVersion: v2\nname: arbitrary\nversion: 1.0.0\n", "chart/values.yaml": "setting: old\n", "chart/templates/workload.yaml": "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: worker\nspec:\n  template:\n    spec:\n      containers:\n      - name: app\n        image: {{ .Values.setting }}\n", "common.yaml": "setting: base\n", "production.yaml": "# retained\nsetting: old\nother: untouched\n"} {
		abs := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(data), 0640); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then echo v3.19.0; exit; fi\nprintf '%s\\n' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "helm"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return HelmRenderContext{RepoPath: root, ChartPath: "chart", Release: "chosen", Namespace: "app", ValuesFiles: []string{"common.yaml", "production.yaml"}, EditValuesPath: "production.yaml", RenderMode: "upgrade", KubeVersion: "1.36.0", APIVersions: []string{"apps/v1"}}
}
func TestHelmContextExplicitInputsAndLastLayer(t *testing.T) {
	c := helmSourceFixture(t)
	in, err := CaptureHelmInputs(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.CheckFresh(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(c.RepoPath, "common.yaml"), []byte("setting: changed\n"), 0640)
	if in.CheckFresh() == nil {
		t.Fatal("stale values accepted")
	}
	for _, field := range []string{"release", "namespace", "mode", "layer", "outside", "url", "symlink"} {
		t.Run(field, func(t *testing.T) {
			c := helmSourceFixture(t)
			switch field {
			case "release":
				c.Release = ""
			case "namespace":
				c.Namespace = ""
			case "mode":
				c.RenderMode = ""
			case "layer":
				c.EditValuesPath = "common.yaml"
			case "outside":
				c.ChartPath = "../outside"
			case "url":
				c.ValuesFiles[0] = "https://example.com/values.yaml"
			case "symlink":
				os.Symlink(filepath.Join(c.RepoPath, "production.yaml"), filepath.Join(c.RepoPath, "link.yaml"))
				c.ValuesFiles[1] = "link.yaml"
				c.EditValuesPath = "link.yaml"
			}
			if _, err := CaptureHelmInputs(c); err == nil {
				t.Fatal("unsafe context accepted")
			}
		})
	}
}
func TestHelmRendererUsesOrderedContextAndNoNetworkModes(t *testing.T) {
	c := helmSourceFixture(t)
	in, err := CaptureHelmInputs(c)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := NewHelmRenderer().Render(context.Background(), c, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{"chosen", "--namespace\napp", "--is-upgrade", "--kube-version\n1.36.0", "--api-versions\napps/v1"} {
		if !strings.Contains(got, want) {
			t.Fatal(got)
		}
	}
	if strings.Index(got, "common.yaml") > strings.Index(got, "production.yaml") {
		t.Fatal("values order changed")
	}
	for _, bad := range []string{"--dependency-update", "--enable-dns", "--post-renderer", "--skip-schema-validation", "--dry-run=server"} {
		if strings.Contains(got, bad) {
			t.Fatal(got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewHelmRenderer().Render(ctx, c, in, nil); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestHelmRendererRefusesOversizedExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate((256 << 20) + 1)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = hashHelmFile(path); err == nil {
		t.Fatal("partial executable hash accepted")
	}
}
