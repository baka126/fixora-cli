package pdbfix

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"os"
	"path/filepath"
	syaml "sigs.k8s.io/yaml"
	"strings"
)

type policy struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		PDB struct {
			Name string `json:"name"`
		} `json:"pdb"`
		Workload struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"workload"`
		Selector *metav1.LabelSelector `json:"selector"`
		Budget   struct {
			MinAvailable   *intstr.IntOrString `json:"minAvailable"`
			MaxUnavailable *intstr.IntOrString `json:"maxUnavailable"`
		} `json:"budget"`
		Availability struct {
			MinimumHealthyReplicas       *int32 `json:"minimumHealthyReplicas"`
			MaximumConcurrentDisruptions *int32 `json:"maximumConcurrentDisruptions"`
		} `json:"availability"`
	} `json:"spec"`
}
type Authority struct {
	path         string
	rawDigest    [32]byte
	intent       policy
	intentDigest [32]byte
}

func hash(v any) [32]byte { b, _ := json.Marshal(v); return sha256.Sum256(b) }
func readPolicy(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return nil, errors.New("policy must be a regular local file no larger than 1 MiB")
	}
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if len(b) > 1<<20 {
		return nil, errors.New("policy exceeds 1 MiB")
	}
	return b, e
}
func LoadPolicy(path string) (*Authority, error) {
	if strings.Contains(path, "://") {
		return nil, errors.New("local PDB policy file required")
	}
	abs, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	abs, e = filepath.EvalSymlinks(abs)
	if e != nil {
		return nil, e
	}
	b, e := readPolicy(abs)
	if e != nil {
		return nil, e
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	var n yaml.Node
	if e = d.Decode(&n); e != nil {
		return nil, e
	}
	if len(n.Content) != 1 || n.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("policy must be a YAML object")
	}
	if e = checkNode(&n); e != nil {
		return nil, e
	}
	var extra yaml.Node
	if d.Decode(&extra) != io.EOF {
		return nil, errors.New("one policy document required")
	}
	j, e := syaml.YAMLToJSON(b)
	if e != nil {
		return nil, e
	}
	var p policy
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&p); e != nil {
		return nil, fmt.Errorf("invalid PDB policy: %w", e)
	}
	if p.APIVersion != "fixora.io/v1alpha1" || p.Kind != "PDBRepairPolicy" || len(validation.IsDNS1123Subdomain(p.Metadata.Name)) > 0 || len(validation.IsDNS1123Label(p.Metadata.Namespace)) > 0 || len(validation.IsDNS1123Subdomain(p.Spec.PDB.Name)) > 0 || len(validation.IsDNS1123Subdomain(p.Spec.Workload.Name)) > 0 {
		return nil, errors.New("exact valid PDB/workload identity required")
	}
	if p.Spec.Workload.Kind != "Deployment" && p.Spec.Workload.Kind != "StatefulSet" {
		return nil, errors.New("PDB repair supports Deployment or StatefulSet")
	}
	if p.Spec.Selector == nil || len(p.Spec.Selector.MatchLabels)+len(p.Spec.Selector.MatchExpressions) == 0 {
		return nil, errors.New("non-empty approved selector required")
	}
	if _, e = selector(p.Spec.Selector); e != nil {
		return nil, e
	}
	a := p.Spec.Availability
	if a.MinimumHealthyReplicas == nil || a.MaximumConcurrentDisruptions == nil || *a.MinimumHealthyReplicas < 1 || *a.MaximumConcurrentDisruptions < 0 {
		return nil, errors.New("explicit positive healthy floor and nonnegative disruption ceiling required")
	}
	spec := policyv1.PodDisruptionBudgetSpec{MinAvailable: p.Spec.Budget.MinAvailable, MaxUnavailable: p.Spec.Budget.MaxUnavailable}
	if _, e = EvaluateBudget(spec, 1, 1); e != nil {
		return nil, e
	}
	return &Authority{path: abs, rawDigest: sha256.Sum256(b), intent: p, intentDigest: hash(p)}, nil
}
func checkNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode {
		return errors.New("policy aliases unsupported")
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
		if e := checkNode(c); e != nil {
			return e
		}
	}
	return nil
}
func (a *Authority) check() error {
	if a == nil || a.path == "" || hash(a.intent) != a.intentDigest {
		return errors.New("independent approved PDB policy required")
	}
	b, e := readPolicy(a.path)
	if e != nil {
		return e
	}
	if sha256.Sum256(b) != a.rawDigest {
		return errors.New("PDB approval file changed; reload and verify again")
	}
	return nil
}
func (a *Authority) Target() Target {
	if a == nil {
		return Target{}
	}
	return Target{a.intent.Spec.Workload.Kind, a.intent.Spec.Workload.Name}
}
func (a *Authority) ProposalContext(ns, name string) (string, error) {
	if e := a.check(); e != nil {
		return "", e
	}
	if ns != a.intent.Metadata.Namespace || name != a.intent.Spec.PDB.Name {
		return "", errors.New("PDB policy target mismatch")
	}
	b, e := json.Marshal(a.intent.Spec)
	return "Independently approved PDB intent (proposal constraints, not apply authority): " + string(b), e
}
