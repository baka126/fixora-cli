// Package servicefix keeps Service repair authority separate from workload repair authority.
package servicefix

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/image"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	syaml "sigs.k8s.io/yaml"
)

const maxPolicyBytes = 1 << 20
const maxBackends = 64
const maxAddresses = 128

type policy struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Service struct {
			Name string `json:"name"`
		} `json:"service"`
		Backend struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"backend"`
		Selector map[string]string `json:"selector"`
		Port     struct {
			Name       string             `json:"name"`
			Port       int32              `json:"port"`
			TargetPort intstr.IntOrString `json:"targetPort"`
		} `json:"port"`
		Verification struct {
			Method         string `json:"method"`
			Path           string `json:"path"`
			ExpectedStatus int    `json:"expectedStatus"`
			Timeout        string `json:"timeout"`
			ProbeImage     string `json:"probeImage"`
		} `json:"verification"`
	} `json:"spec"`
}
type Authority struct {
	path, digest string
	intent       policy
	timeout      time.Duration
}

func LoadPolicy(path string) (*Authority, error) {
	if strings.Contains(path, "://") {
		return nil, errors.New("Service policy must be a local file")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, errors.New("policy directory unavailable")
	}
	abs = filepath.Join(parent, filepath.Base(abs))
	data, err := readPolicy(abs)
	if err != nil {
		return nil, err
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	var n yaml.Node
	if d.Decode(&n) != nil || len(n.Content) != 1 || n.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("Service policy must be one YAML object")
	}
	if err = checkPolicyNode(&n); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err = d.Decode(&extra); err != io.EOF {
		return nil, errors.New("one policy document required")
	}
	raw, err := syaml.YAMLToJSON(data)
	if err != nil {
		return nil, errors.New("invalid policy YAML")
	}
	var p policy
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&p); err != nil {
		return nil, errors.New("invalid or unknown Service policy fields")
	}
	if p.APIVersion != "fixora.io/v1alpha1" || p.Kind != "ServiceRepairPolicy" || len(validation.IsDNS1123Subdomain(p.Metadata.Name)) > 0 || len(validation.IsDNS1123Label(p.Metadata.Namespace)) > 0 || len(validation.IsDNS1123Subdomain(p.Spec.Service.Name)) > 0 || len(validation.IsDNS1123Subdomain(p.Spec.Backend.Name)) > 0 {
		return nil, errors.New("exact Service policy identity required")
	}
	if p.Spec.Backend.Kind != "Deployment" && p.Spec.Backend.Kind != "StatefulSet" && p.Spec.Backend.Kind != "DaemonSet" {
		return nil, errors.New("unsupported approved backend kind")
	}
	if len(p.Spec.Selector) == 0 {
		return nil, errors.New("explicit backend selector required")
	}
	for k, v := range p.Spec.Selector {
		if len(validation.IsQualifiedName(k)) > 0 || len(validation.IsValidLabelValue(v)) > 0 {
			return nil, errors.New("invalid approved selector")
		}
	}
	if p.Spec.Port.Port < 1 || p.Spec.Port.Port > 65535 || p.Spec.Port.Name != "" && len(validation.IsValidPortName(p.Spec.Port.Name)) > 0 || !validTargetPort(p.Spec.Port.TargetPort) {
		return nil, errors.New("exact valid Service port and targetPort required")
	}
	v := p.Spec.Verification
	switch v.Method {
	case "tcp":
		if v.Path != "" || v.ExpectedStatus != 0 {
			return nil, errors.New("TCP contract cannot contain HTTP fields")
		}
	case "http":
		u, e := url.ParseRequestURI(v.Path)
		if e != nil || !strings.HasPrefix(v.Path, "/") || strings.HasPrefix(v.Path, "//") || strings.Contains(v.Path, "#") || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || v.ExpectedStatus < 200 || v.ExpectedStatus > 399 {
			return nil, errors.New("safe explicit HTTP health contract required")
		}
	default:
		return nil, errors.New("explicit TCP or HTTP verification required")
	}
	timeout := 5 * time.Second
	if v.Timeout != "" {
		timeout, err = time.ParseDuration(v.Timeout)
		if err != nil {
			return nil, errors.New("invalid probe timeout")
		}
	}
	if timeout < time.Second || timeout > 10*time.Second {
		return nil, errors.New("probe timeout must be 1–10 seconds")
	}
	if !regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`).MatchString(v.ProbeImage) {
		return nil, errors.New("operator-approved digest-pinned probe image required")
	}
	if _, err = image.Repository(v.ProbeImage); err != nil {
		return nil, errors.New("invalid probe image")
	}
	return &Authority{path: abs, digest: digest(data), intent: p, timeout: timeout}, nil
}
func readPolicy(path string) ([]byte, error) {
	s, err := os.Lstat(path)
	if err != nil || !s.Mode().IsRegular() || s.Size() > maxPolicyBytes {
		return nil, errors.New("policy must be a readable regular file of at most 1MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("policy unreadable")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxPolicyBytes+1))
	if err != nil || len(b) > maxPolicyBytes {
		return nil, errors.New("policy read exceeded limit")
	}
	return b, nil
}
func (a *Authority) check() error {
	if a == nil || a.digest == "" {
		return errors.New("fresh local Service policy authority required")
	}
	b, err := readPolicy(a.path)
	if err != nil || digest(b) != a.digest {
		return errors.New("Service policy changed; prepare again")
	}
	return nil
}
func validTargetPort(p intstr.IntOrString) bool {
	if p.Type == intstr.Int {
		return p.IntVal >= 1 && p.IntVal <= 65535
	}
	return p.Type == intstr.String && len(validation.IsValidPortName(p.StrVal)) == 0
}
func digest(v any) string {
	b, ok := v.([]byte)
	if !ok {
		b, _ = json.Marshal(v)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func checkPolicyNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.New("policy aliases/anchors require review")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Value == "<<" || seen[k.Value] {
				return errors.New("duplicate or non-string policy key")
			}
			seen[k.Value] = true
		}
	}
	for _, c := range n.Content {
		if err := checkPolicyNode(c); err != nil {
			return err
		}
	}
	return nil
}

// ProposalContext exposes approved routing as data, never verifier commands or authority.
func (a *Authority) ProposalContext(kind, name, namespace string) (string, error) {
	if err := a.check(); err != nil {
		return "", err
	}
	p := a.intent
	if kind != "Service" || name != p.Spec.Service.Name || namespace != p.Metadata.Namespace {
		return "", errors.New("Service proposal context must match the exact policy target")
	}
	raw, err := json.Marshal(map[string]any{"service": p.Spec.Service.Name, "namespace": p.Metadata.Namespace, "backend": p.Spec.Backend, "selector": p.Spec.Selector, "port": p.Spec.Port})
	if err != nil || len(raw) > 4096 {
		return "", errors.New("approved Service proposal context exceeds provider evidence limit")
	}
	return string(raw), nil
}
