package repo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"k8s.io/apimachinery/pkg/util/validation"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
)

type HelmRenderContext struct {
	RepoPath, ChartPath, Release, Namespace, EditValuesPath string
	ValuesFiles                                             []string
	RenderMode, KubeVersion                                 string
	APIVersions                                             []string
}
type HelmInputs struct {
	context                                      HelmRenderContext
	files                                        map[string][]byte
	fingerprint, rendererHash, renderer, version string
}
type HelmRenderer interface {
	Render(context.Context, HelmRenderContext, *HelmInputs, []byte) ([]byte, error)
	Lint(context.Context, HelmRenderContext, *HelmInputs, []byte) error
}
type localHelmRenderer struct{}

func NewHelmRenderer() HelmRenderer { return localHelmRenderer{} }

const helmValuesLimit = 1 << 20
const helmBundleLimit = 64 << 20
const helmOutputLimit = 16 << 20

var unstableHelmTemplate = regexp.MustCompile(`\b(lookup|now|randAlphaNum|randAlpha|randNumeric|randAscii|uuidv4|randBytes)\b`)

func CaptureHelmInputs(c HelmRenderContext) (*HelmInputs, error) {
	if len(c.Release) > 53 || len(validation.IsDNS1123Subdomain(c.Release)) > 0 || len(validation.IsDNS1123Label(c.Namespace)) > 0 || (c.RenderMode != "install" && c.RenderMode != "upgrade") {
		return nil, errors.New("explicit Helm release, namespace and install/upgrade mode are required")
	}
	root, err := filepath.Abs(c.RepoPath)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, errors.New("repository is unreadable")
	}
	c.RepoPath = root
	c.ChartPath = filepath.Clean(c.ChartPath)
	c.EditValuesPath = filepath.Clean(c.EditValuesPath)
	c.ValuesFiles = append([]string(nil), c.ValuesFiles...)
	for i, path := range c.ValuesFiles {
		c.ValuesFiles[i] = filepath.Clean(path)
	}
	c.APIVersions = append([]string(nil), c.APIVersions...)
	for _, path := range append([]string{c.ChartPath, c.EditValuesPath}, c.ValuesFiles...) {
		if _, err := confinedHelmPath(root, path); err != nil {
			return nil, err
		}
	}
	if len(c.ValuesFiles) == 0 {
		if c.EditValuesPath != filepath.Join(c.ChartPath, "values.yaml") {
			return nil, errors.New("explicit chart defaults are required without values layers")
		}
	} else if c.ValuesFiles[len(c.ValuesFiles)-1] != c.EditValuesPath {
		return nil, errors.New("editable values file must be the last layer")
	}
	files, err := captureHelmFiles(c)
	if err != nil {
		return nil, err
	}
	renderer, err := exec.LookPath("helm")
	if err != nil {
		return nil, errors.New("helm is unavailable")
	}
	renderer, err = filepath.EvalSymlinks(renderer)
	if err != nil {
		return nil, errors.New("Helm executable is unreadable")
	}
	hash, err := hashHelmFile(renderer)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, renderer, "version", "--short").Output()
	if err != nil || len(version) > 4096 {
		return nil, errors.New("Helm version could not be determined")
	}
	in := &HelmInputs{context: c, files: files, renderer: renderer, rendererHash: hash, version: string(version)}
	in.fingerprint = helmHash(files)
	return in, nil
}
func confinedHelmPath(root, path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "://") {
		return "", errors.New("Helm inputs must be repository-relative local paths")
	}
	clean := filepath.Clean(path)
	for _, part := range strings.Split(clean, string(os.PathSeparator)) {
		if part == ".git" {
			return "", errors.New("Git metadata is not a Helm input")
		}
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errors.New("Helm path escapes repository")
	}
	target := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil || resolved != target {
		return "", errors.New("Helm input is missing or uses a symlink")
	}
	return target, nil
}
func captureHelmFiles(c HelmRenderContext) (map[string][]byte, error) {
	files := map[string][]byte{}
	total := 0
	add := func(path string) error {
		abs, err := confinedHelmPath(c.RepoPath, path)
		if err != nil {
			return err
		}
		info, err := os.Stat(abs)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("Helm input is not a regular file")
		}
		limit := helmBundleLimit
		for _, v := range append([]string{c.EditValuesPath}, c.ValuesFiles...) {
			if path == v {
				limit = helmValuesLimit
			}
		}
		if info.Size() > int64(limit) {
			return errors.New("Helm input exceeds size limit")
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return errors.New("Helm input is unreadable")
		}
		if err = validateHelmSourceFile(path, data); err != nil {
			return err
		}
		total += len(data)
		if total > helmBundleLimit {
			return errors.New("Helm bundle exceeds size limit")
		}
		files[path] = data
		return nil
	}
	chart, err := confinedHelmPath(c.RepoPath, c.ChartPath)
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(filepath.Join(chart, "Chart.yaml")); err != nil {
		return nil, errors.New("selected chart lacks Chart.yaml")
	}
	err = filepath.WalkDir(chart, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return errors.New("chart tree is unreadable")
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("chart symlinks require source review")
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(c.RepoPath, path)
		return add(rel)
	})
	if err != nil {
		return nil, err
	}
	for _, path := range append(append([]string{}, c.ValuesFiles...), c.EditValuesPath) {
		if _, exists := files[path]; !exists {
			if err = add(path); err != nil {
				return nil, err
			}
		}
	}
	return files, nil
}
func validateHelmSourceFile(path string, data []byte) error {
	if strings.HasSuffix(path, ".tgz") {
		return validateHelmArchive(data)
	}
	if strings.Contains(filepath.ToSlash(path), "/templates/") && unstableHelmTemplate.Match(data) {
		return errors.New("chart uses nondeterministic or lookup-dependent functions")
	}
	if strings.HasSuffix(path, "schema.json") {
		var v any
		if json.Unmarshal(data, &v) != nil {
			return errors.New("chart schema is invalid")
		}
		if err := checkSchemaReferences(v); err != nil {
			return err
		}
	}
	return nil
}
func checkSchemaReferences(v any) error {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if k == "$ref" {
				ref, _ := v.(string)
				if !strings.HasPrefix(ref, "#") {
					return errors.New("non-fragment schema references require source review")
				}
			}
			if err := checkSchemaReferences(v); err != nil {
				return err
			}
		}
	case []any:
		for _, v := range x {
			if err := checkSchemaReferences(v); err != nil {
				return err
			}
		}
	}
	return nil
}
func hashHelmFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("Helm executable is unreadable")
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, 256<<20)); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
func (in HelmInputs) CheckFresh() error {
	files, err := captureHelmFiles(in.context)
	if err != nil || helmHash(files) != in.fingerprint {
		return errors.New("Helm source inputs changed; prepare again")
	}
	hash, err := hashHelmFile(in.renderer)
	if err != nil || hash != in.rendererHash {
		return errors.New("Helm executable changed")
	}
	return nil
}
func (r localHelmRenderer) Render(ctx context.Context, c HelmRenderContext, in *HelmInputs, edited []byte) ([]byte, error) {
	return runCapturedHelm(ctx, c, in, edited, false)
}
func (r localHelmRenderer) Lint(ctx context.Context, c HelmRenderContext, in *HelmInputs, edited []byte) error {
	_, err := runCapturedHelm(ctx, c, in, edited, true)
	return err
}
func runCapturedHelm(ctx context.Context, c HelmRenderContext, in *HelmInputs, edited []byte, lint bool) ([]byte, error) {
	if in == nil {
		return nil, errors.New("captured Helm inputs required")
	}
	normalized := c
	normalized.RepoPath = in.context.RepoPath
	normalized.ChartPath = filepath.Clean(normalized.ChartPath)
	normalized.EditValuesPath = filepath.Clean(normalized.EditValuesPath)
	normalized.ValuesFiles = append([]string(nil), c.ValuesFiles...)
	for i, path := range normalized.ValuesFiles {
		normalized.ValuesFiles[i] = filepath.Clean(path)
	}
	if !reflect.DeepEqual(normalized, in.context) {
		return nil, errors.New("Helm context differs from captured inputs")
	}
	if err := in.CheckFresh(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "fixora-helm-render-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for path, data := range in.files {
		if path == c.EditValuesPath && edited != nil {
			data = edited
		}
		target := filepath.Join(dir, path)
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		if err = os.WriteFile(target, data, 0600); err != nil {
			return nil, err
		}
	}
	args := []string{"template", c.Release, filepath.Join(dir, c.ChartPath), "--namespace", c.Namespace, "--include-crds"}
	if lint {
		args = []string{"lint", filepath.Join(dir, c.ChartPath), "--strict"}
	} else {
		if c.RenderMode == "upgrade" {
			args = append(args, "--is-upgrade")
		}
		for _, v := range c.APIVersions {
			args = append(args, "--api-versions", v)
		}
	}
	if c.KubeVersion != "" {
		args = append(args, "--kube-version", c.KubeVersion)
	}
	for _, path := range c.ValuesFiles {
		args = append(args, "--values", filepath.Join(dir, path))
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, in.renderer, args...)
	out := &limitedHelmBuffer{}
	cmd.Stdout = out
	errOut := &limitedHelmBuffer{}
	cmd.Stderr = errOut
	if err = cmd.Run(); err != nil {
		return nil, errors.New("Helm render/validation failed (check chart locally for details)")
	}
	if out.exceeded || errOut.exceeded {
		return nil, errors.New("Helm output exceeds limit")
	}
	return out.Bytes(), nil
}

type limitedHelmBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *limitedHelmBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := helmOutputLimit - b.Len()
	if remaining < len(p) {
		b.exceeded = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
