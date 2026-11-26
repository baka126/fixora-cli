package rbacfix

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/util/validation"
	syaml "sigs.k8s.io/yaml"
)

const maxPolicyBytes = 1 << 20

type Policy struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		ServiceAccount  string       `json:"serviceAccount"`
		RoleName        string       `json:"roleName"`
		RoleBindingName string       `json:"roleBindingName"`
		Permissions     []Permission `json:"permissions"`
	} `json:"spec"`
}

type Permission struct {
	APIGroup      string   `json:"apiGroup"`
	Resource      string   `json:"resource"`
	Verbs         []string `json:"verbs"`
	ResourceNames []string `json:"resourceNames,omitempty"`
}

type Authority struct {
	path     string
	identity os.FileInfo
	digest   [32]byte
	policy   Policy
}

func LoadPolicy(path string) (*Authority, error) {
	if path == "" || strings.Contains(path, "://") {
		return nil, errors.New("a local RBAC policy file is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	b, identity, err := readPolicy(abs)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var root yaml.Node
	if err = dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("decode RBAC policy: %w", err)
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("policy must be one YAML object")
	}
	if err = checkYAMLNode(&root); err != nil {
		return nil, err
	}
	if err = checkResourceNameField(root.Content[0]); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err = dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("policy must contain exactly one YAML document")
	}
	j, err := syaml.YAMLToJSON(b)
	if err != nil {
		return nil, err
	}
	var p Policy
	jd := json.NewDecoder(bytes.NewReader(j))
	jd.DisallowUnknownFields()
	if err = jd.Decode(&p); err != nil {
		return nil, fmt.Errorf("invalid RBAC policy: %w", err)
	}
	if err = validatePolicy(p); err != nil {
		return nil, err
	}
	return &Authority{path: abs, identity: identity, digest: sha256.Sum256(b), policy: p}, nil
}

func checkResourceNameField(root *yaml.Node) error {
	spec := mappingValue(root, "spec")
	permissions := mappingValue(spec, "permissions")
	if permissions == nil || permissions.Kind != yaml.SequenceNode {
		return nil
	}
	for _, permission := range permissions.Content {
		names := mappingValue(permission, "resourceNames")
		if names != nil && (names.Tag == "!!null" || (names.Kind == yaml.SequenceNode && len(names.Content) == 0)) {
			return errors.New("resourceNames must be omitted or contain at least one name")
		}
	}
	return nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func validatePolicy(p Policy) error {
	if p.APIVersion != "fixora.io/v1alpha1" || p.Kind != "RBACRepairPolicy" {
		return errors.New("apiVersion fixora.io/v1alpha1 and kind RBACRepairPolicy are required")
	}
	if len(validation.IsDNS1123Label(p.Metadata.Namespace)) > 0 || len(validation.IsDNS1123Subdomain(p.Spec.ServiceAccount)) > 0 || len(validation.IsDNS1123Subdomain(p.Spec.RoleName)) > 0 || len(validation.IsDNS1123Subdomain(p.Spec.RoleBindingName)) > 0 {
		return errors.New("valid namespace, ServiceAccount, Role, and RoleBinding names are required")
	}
	if len(p.Spec.Permissions) == 0 {
		return errors.New("at least one exact permission is required")
	}
	seen := map[string]bool{}
	for _, q := range p.Spec.Permissions {
		if q.APIGroup == "*" || q.Resource == "" || q.Resource == "*" || strings.ContainsAny(q.Resource, "* ?#") {
			return errors.New("wildcard or invalid API group/resource is not allowed")
		}
		parts := strings.Split(q.Resource, "/")
		if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
			return errors.New("resource must be an exact resource or resource/subresource")
		}
		if strings.Contains(q.APIGroup, "*") || strings.Contains(q.APIGroup, "/") || (q.APIGroup != "" && len(validation.IsDNS1123Subdomain(q.APIGroup)) > 0) {
			return errors.New("API group must be exact and valid")
		}
		if len(q.Verbs) == 0 {
			return errors.New("each permission requires exact verbs")
		}
		if len(q.ResourceNames) == 0 && q.ResourceNames != nil {
			return errors.New("resourceNames must be omitted or contain at least one name")
		}
		permKey := q.APIGroup + "\x00" + q.Resource + "\x00" + strings.Join(sortedCopy(q.ResourceNames), "\x00")
		if seen["permission:"+permKey] {
			return errors.New("duplicate permission entry")
		}
		seen["permission:"+permKey] = true
		localVerbs := map[string]bool{}
		for _, verb := range q.Verbs {
			if !slices.Contains([]string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection"}, verb) {
				return fmt.Errorf("unsupported or privileged verb %q", verb)
			}
			if localVerbs[verb] {
				return errors.New("duplicate permission verb")
			}
			localVerbs[verb] = true
			if (verb == "create" || verb == "deletecollection") && len(q.ResourceNames) > 0 {
				return fmt.Errorf("verb %q cannot be restricted by resourceNames", verb)
			}
			if isForbidden(q.APIGroup, q.Resource, verb) {
				return fmt.Errorf("permission %s/%s verb %s is outside the supported repair scope", q.APIGroup, q.Resource, verb)
			}
			k := q.APIGroup + "\x00" + q.Resource + "\x00" + verb
			if seen[k] {
				return errors.New("duplicate permission verb")
			}
			seen[k] = true
		}
		nameSet := map[string]bool{}
		for _, name := range q.ResourceNames {
			if len(validation.IsDNS1123Subdomain(name)) > 0 {
				return fmt.Errorf("invalid resource name %q", name)
			}
			if nameSet[name] {
				return fmt.Errorf("duplicate resource name %q", name)
			}
			nameSet[name] = true
		}
	}
	return nil
}

func sortedCopy(items []string) []string {
	out := append([]string(nil), items...)
	sort.Strings(out)
	return out
}

func isForbidden(group, resource, verb string) bool {
	base := strings.SplitN(resource, "/", 2)[0]
	if base == "secrets" || (group == "rbac.authorization.k8s.io") {
		return true
	}
	if base == "serviceaccounts" && (verb == "create" || verb == "update" || verb == "patch" || verb == "delete" || verb == "deletecollection") {
		return true
	}
	if resource == "serviceaccounts/token" {
		return true
	}
	if group == "" && (resource == "pods/exec" || resource == "pods/attach" || resource == "pods/portforward" || resource == "pods/proxy" || resource == "pods/ephemeralcontainers" || resource == "nodes/proxy") {
		return true
	}
	workload := group == "apps" && slices.Contains([]string{"deployments", "replicasets", "statefulsets", "daemonsets"}, base) || group == "batch" && slices.Contains([]string{"jobs", "cronjobs"}, base) || group == "" && slices.Contains([]string{"pods", "podtemplates", "replicationcontrollers"}, base)
	return workload && (verb == "create" || verb == "update" || verb == "patch" || verb == "delete" || verb == "deletecollection")
}

func checkYAMLNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode {
		return errors.New("YAML aliases are not supported in RBAC policy")
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
		if err := checkYAMLNode(c); err != nil {
			return err
		}
	}
	return nil
}

func readPolicy(path string) ([]byte, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, errors.New("policy must be a regular local file")
	}
	f, err := openPolicyFile(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !st.Mode().IsRegular() || !os.SameFile(before, st) || st.Size() > maxPolicyBytes {
		return nil, nil, errors.New("policy must be a regular file no larger than 1 MiB")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxPolicyBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(b) > maxPolicyBytes {
		return nil, nil, errors.New("policy exceeds 1 MiB")
	}
	return b, st, nil
}

func (a *Authority) CheckFresh() error {
	if a == nil || a.path == "" {
		return errors.New("independent RBAC policy required")
	}
	b, st, err := readPolicy(a.path)
	if err != nil {
		return err
	}
	if !os.SameFile(a.identity, st) || sha256.Sum256(b) != a.digest {
		return errors.New("RBAC policy changed; reload and review it again")
	}
	return nil
}

func (a *Authority) Namespace() string {
	if a == nil {
		return ""
	}
	return a.policy.Metadata.Namespace
}
func (a *Authority) ServiceAccount() string {
	if a == nil {
		return ""
	}
	return a.policy.Spec.ServiceAccount
}
func (a *Authority) Digest() string {
	if a == nil {
		return ""
	}
	return hex.EncodeToString(a.digest[:])
}
func (a *Authority) Permissions() []Permission {
	if a == nil {
		return nil
	}
	out := make([]Permission, len(a.policy.Spec.Permissions))
	copy(out, a.policy.Spec.Permissions)
	for i := range out {
		out[i].Verbs = slices.Clone(out[i].Verbs)
		out[i].ResourceNames = slices.Clone(out[i].ResourceNames)
	}
	return out
}
