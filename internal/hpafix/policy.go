package hpafix

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
	syaml "sigs.k8s.io/yaml"
)

const maxPolicyBytes = 1 << 20

func LoadPolicy(path string) (*Authority, error) {
	if path == "" || strings.Contains(path, "://") {
		return nil, errors.New("local HPA policy file required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	b, identity, err := readPolicy(abs)
	if err != nil {
		return nil, err
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	var node yaml.Node
	if err = d.Decode(&node); err != nil || len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("HPA policy must be one YAML object")
	}
	if err = checkPolicyNode(&node); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err = d.Decode(&extra); err != io.EOF {
		return nil, errors.New("exactly one HPA policy document is required")
	}
	raw, err := syaml.YAMLToJSON(b)
	if err != nil {
		return nil, errors.New("invalid HPA policy YAML")
	}
	var p policyDocument
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("invalid HPA policy fields: %w", err)
	}
	if p.APIVersion != "fixora.io/v1alpha1" || p.Kind != "HPARepairPolicy" || !validDNS(p.Metadata.Name) || !validDNS(p.Metadata.Namespace) || !validDNS(p.Spec.HPA.Name) || !validDNS(p.Spec.Target.Name) {
		return nil, errors.New("exact valid HPA and target policy identity required")
	}
	if p.Spec.Target.Kind != "Deployment" && p.Spec.Target.Kind != "StatefulSet" {
		return nil, errors.New("HPA repair supports Deployment or StatefulSet targets")
	}
	if len(p.Spec.Requests) == 0 {
		return nil, errors.New("at least one exact container request is required")
	}
	seen := map[string]bool{}
	for _, req := range p.Spec.Requests {
		if len(validation.IsDNS1123Label(req.Container)) != 0 || seen[req.Container] {
			return nil, errors.New("request entries require unique valid container names")
		}
		seen[req.Container] = true
		if req.CPU == "" && req.Memory == "" {
			return nil, errors.New("each request entry must specify CPU or memory")
		}
		for _, quantity := range []string{req.CPU, req.Memory} {
			if quantity != "" {
				q, e := resource.ParseQuantity(quantity)
				if e != nil || q.Sign() <= 0 {
					return nil, errors.New("invalid nonnegative Kubernetes resource quantity")
				}
			}
		}
	}
	return &Authority{path: abs, digest: sha256.Sum256(b), identity: identity, intent: p}, nil
}

func (a *Authority) check() error {
	if a == nil || a.path == "" {
		return errors.New("fresh HPA policy authority required")
	}
	b, identity, err := readPolicy(a.path)
	if err != nil || !os.SameFile(a.identity, identity) || sha256.Sum256(b) != a.digest {
		return errors.New("HPA policy changed; prepare again")
	}
	return nil
}

func readPolicy(path string) ([]byte, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > maxPolicyBytes {
		return nil, nil, errors.New("HPA policy must be a regular file no larger than 1 MiB")
	}
	f, err := openPolicyFile(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() > maxPolicyBytes {
		return nil, nil, errors.New("HPA policy file was replaced or is not regular")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxPolicyBytes+1))
	if err != nil || len(b) > maxPolicyBytes {
		return nil, nil, errors.New("HPA policy exceeds 1 MiB")
	}
	return b, after, nil
}

func checkPolicyNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.New("HPA policy aliases and anchors are not supported")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Value == "<<" || seen[k.Value] {
				return errors.New("duplicate or non-string HPA policy key")
			}
			seen[k.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := checkPolicyNode(child); err != nil {
			return err
		}
	}
	return nil
}

func validDNS(s string) bool { return len(validation.IsDNS1123Subdomain(s)) == 0 }
