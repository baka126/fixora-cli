package storagefix

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

type policyDocument struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		PVC struct {
			Name string `json:"name"`
		} `json:"pvc"`
		Action           string `json:"action"`
		StorageClassName string `json:"storageClassName,omitempty"`
		RequestedStorage string `json:"requestedStorage,omitempty"`
		MaximumStorage   string `json:"maxStorage,omitempty"`
	} `json:"spec"`
}

// Authority represents a freshly loaded, exact-match local storage policy.
// Its fields are intentionally private so it cannot be constructed by AI output.
type Authority struct {
	path     string
	digest   [32]byte
	identity os.FileInfo
	intent   policyDocument
}

func LoadPolicy(path string) (*Authority, error) {
	if strings.TrimSpace(path) == "" || strings.Contains(path, "://") {
		return nil, errors.New("local PVC policy file required")
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
		return nil, errors.New("PVC policy must be one YAML object")
	}
	if err = checkPolicyNode(&node); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err = d.Decode(&extra); err != io.EOF {
		return nil, errors.New("exactly one PVC policy document is required")
	}
	raw, err := syaml.YAMLToJSON(b)
	if err != nil {
		return nil, errors.New("invalid PVC policy YAML")
	}
	var p policyDocument
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("invalid PVC policy fields: %w", err)
	}
	if p.APIVersion != "fixora.io/v1alpha1" || p.Kind != "PVCRepairPolicy" || len(validation.IsDNS1123Subdomain(p.Metadata.Name)) != 0 || len(validation.IsDNS1123Label(p.Metadata.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(p.Spec.PVC.Name)) != 0 {
		return nil, errors.New("exact valid PVC policy identity required")
	}
	switch p.Spec.Action {
	case "setStorageClass":
		if len(validation.IsDNS1123Subdomain(p.Spec.StorageClassName)) != 0 || p.Spec.RequestedStorage != "" || p.Spec.MaximumStorage != "" {
			return nil, errors.New("setStorageClass requires one exact valid class and no size fields")
		}
	case "expand":
		if p.Spec.StorageClassName != "" || p.Spec.RequestedStorage == "" || p.Spec.MaximumStorage == "" {
			return nil, errors.New("expand requires requestedStorage and maxStorage only")
		}
		target, targetErr := resource.ParseQuantity(p.Spec.RequestedStorage)
		maximum, maxErr := resource.ParseQuantity(p.Spec.MaximumStorage)
		if targetErr != nil || maxErr != nil || target.Sign() <= 0 || maximum.Sign() <= 0 || target.Cmp(maximum) > 0 {
			return nil, errors.New("positive requestedStorage within positive maxStorage required")
		}
	default:
		return nil, errors.New("unsupported PVC repair action")
	}
	return &Authority{path: abs, digest: sha256.Sum256(b), identity: identity, intent: p}, nil
}

func (a *Authority) check() error {
	if a == nil || a.path == "" || a.identity == nil {
		return errors.New("fresh PVC policy authority required")
	}
	b, identity, err := readPolicy(a.path)
	if err != nil || !os.SameFile(a.identity, identity) || sha256.Sum256(b) != a.digest {
		return errors.New("PVC policy changed; prepare again")
	}
	return nil
}

func readPolicy(path string) ([]byte, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxPolicyBytes {
		return nil, nil, errors.New("PVC policy must be a regular file no larger than 1 MiB")
	}
	f, err := openPolicyFile(path)
	if err != nil {
		return nil, nil, errors.New("PVC policy must be opened as a regular non-symlink file")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() > maxPolicyBytes {
		return nil, nil, errors.New("PVC policy file was replaced or is not regular")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxPolicyBytes+1))
	if err != nil || len(b) > maxPolicyBytes {
		return nil, nil, errors.New("PVC policy exceeds 1 MiB")
	}
	return b, after, nil
}

func checkPolicyNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.New("PVC policy aliases and anchors are not supported")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Value == "<<" || seen[k.Value] {
				return errors.New("duplicate or non-string PVC policy key")
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
