package shadow

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
	syaml "sigs.k8s.io/yaml"
)

const graphStoragePolicyMaxBytes = 1 << 20

type graphStoragePolicyDocument struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		MaxTotalLogicalCapacity string                  `json:"maxTotalLogicalCapacity"`
		PVCs                    []graphStoragePolicyPVC `json:"pvcs"`
	} `json:"spec"`
}

type graphStoragePolicyPVC struct {
	Name                    string `json:"name"`
	VolumeSnapshotClassName string `json:"volumeSnapshotClassName"`
	RestoreStorageClassName string `json:"restoreStorageClassName"`
	MaxLogicalCapacity      string `json:"maxLogicalCapacity"`
	AllowCrashConsistent    *bool  `json:"allowCrashConsistent"`
}

// GraphStorageRule is an exact local authorization for one PVC. It contains
// class names and logical capacity limits, never PVC data or CSI handles.
type GraphStorageRule struct {
	PVCName                 string
	VolumeSnapshotClassName string
	RestoreStorageClassName string
	MaxLogicalBytes         int64
	MaxTotalLogicalBytes    int64
	AllowCrashConsistent    bool
}

// GraphStorageAuthority is constructed only by LoadGraphStoragePolicy. Its
// file identity and digest prevent a modified policy from continuing to
// authorize snapshot operations.
type GraphStorageAuthority struct {
	path      string
	digest    [32]byte
	identity  os.FileInfo
	namespace string
	maxTotal  int64
	pvcs      map[string]GraphStorageRule
}

// LoadGraphStoragePolicy loads a strict local policy document with exact
// namespace/PVC entries and bounded logical storage capacity.
func LoadGraphStoragePolicy(path string) (*GraphStorageAuthority, error) {
	if strings.TrimSpace(path) == "" || strings.Contains(path, "://") {
		return nil, errors.New("local graph storage policy file required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("graph storage policy path is invalid")
	}
	body, identity, err := readGraphStoragePolicy(abs)
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&root); err != nil || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("graph storage policy must be one YAML object")
	}
	if err := checkGraphStoragePolicyNode(&root); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("exactly one graph storage policy document is required")
	}
	jsonBytes, err := syaml.YAMLToJSON(body)
	if err != nil {
		return nil, errors.New("invalid graph storage policy YAML")
	}
	var doc graphStoragePolicyDocument
	strict := json.NewDecoder(bytes.NewReader(jsonBytes))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&doc); err != nil {
		return nil, errors.New("invalid graph storage policy fields")
	}
	if doc.APIVersion != "fixora.io/v1alpha1" || doc.Kind != "GraphStorageShadowPolicy" ||
		len(validation.IsDNS1123Subdomain(doc.Metadata.Name)) != 0 || len(validation.IsDNS1123Label(doc.Metadata.Namespace)) != 0 {
		return nil, errors.New("graph storage policy requires a valid identity")
	}
	maxTotal, err := parseGraphStorageBytes(doc.Spec.MaxTotalLogicalCapacity)
	if err != nil {
		return nil, errors.New("graph storage policy requires a positive total logical capacity")
	}
	if len(doc.Spec.PVCs) == 0 || len(doc.Spec.PVCs) > GraphNodeLimit {
		return nil, fmt.Errorf("graph storage policy must list between 1 and %d PVCs", GraphNodeLimit)
	}
	authority := &GraphStorageAuthority{
		path: abs, digest: sha256.Sum256(body), identity: identity,
		namespace: doc.Metadata.Namespace, maxTotal: maxTotal,
		pvcs: make(map[string]GraphStorageRule, len(doc.Spec.PVCs)),
	}
	for _, entry := range doc.Spec.PVCs {
		if len(validation.IsDNS1123Subdomain(entry.Name)) != 0 ||
			len(validation.IsDNS1123Subdomain(entry.VolumeSnapshotClassName)) != 0 ||
			len(validation.IsDNS1123Subdomain(entry.RestoreStorageClassName)) != 0 {
			return nil, errors.New("graph storage policy contains an invalid PVC or storage class name")
		}
		if _, duplicate := authority.pvcs[entry.Name]; duplicate {
			return nil, errors.New("graph storage policy contains duplicate PVC entries")
		}
		maxLogical, err := parseGraphStorageBytes(entry.MaxLogicalCapacity)
		if err != nil || maxLogical > maxTotal {
			return nil, errors.New("PVC logical capacity must be positive and within the graph total limit")
		}
		if entry.AllowCrashConsistent == nil {
			return nil, errors.New("graph storage policy must explicitly set allowCrashConsistent")
		}
		authority.pvcs[entry.Name] = GraphStorageRule{
			PVCName: entry.Name, VolumeSnapshotClassName: entry.VolumeSnapshotClassName,
			RestoreStorageClassName: entry.RestoreStorageClassName, MaxLogicalBytes: maxLogical,
			MaxTotalLogicalBytes: maxTotal, AllowCrashConsistent: *entry.AllowCrashConsistent,
		}
	}
	return authority, nil
}

// CheckFresh refuses authorization after the local policy file has changed or
// been replaced since it was loaded.
func (a *GraphStorageAuthority) CheckFresh() error {
	if a == nil || a.path == "" || a.identity == nil || len(a.pvcs) == 0 {
		return errors.New("fresh graph storage policy authority required")
	}
	body, identity, err := readGraphStoragePolicy(a.path)
	if err != nil || !os.SameFile(a.identity, identity) || sha256.Sum256(body) != a.digest {
		return errors.New("graph storage policy changed; prepare again")
	}
	return nil
}

// Digest returns the lowercase SHA-256 policy digest bound to this authority.
func (a *GraphStorageAuthority) Digest() string {
	if a == nil {
		return ""
	}
	return fmt.Sprintf("%x", a.digest)
}

// Authorize validates a single exact PVC against this authority.
func (a *GraphStorageAuthority) Authorize(namespace, pvcName string, requestedBytes int64) (GraphStorageRule, error) {
	rules, err := a.AuthorizeGraph(namespace, map[string]int64{pvcName: requestedBytes})
	if err != nil {
		return GraphStorageRule{}, err
	}
	return rules[pvcName], nil
}

// AuthorizeGraph validates all requested claims together against per-PVC and
// aggregate logical capacity limits.
func (a *GraphStorageAuthority) AuthorizeGraph(namespace string, requested map[string]int64) (map[string]GraphStorageRule, error) {
	if err := a.CheckFresh(); err != nil {
		return nil, err
	}
	if namespace != a.namespace || len(requested) == 0 {
		return nil, errors.New("graph storage policy namespace or PVC set does not match")
	}
	names := make([]string, 0, len(requested))
	for name := range requested {
		names = append(names, name)
	}
	sort.Strings(names)
	var total int64
	authorized := make(map[string]GraphStorageRule, len(requested))
	for _, name := range names {
		bytes := requested[name]
		rule, ok := a.pvcs[name]
		if !ok || bytes <= 0 || bytes > rule.MaxLogicalBytes {
			return nil, fmt.Errorf("PVC %s is not authorized for this graph storage request", name)
		}
		if !rule.AllowCrashConsistent {
			return nil, fmt.Errorf("PVC %s policy does not allow crash-consistent snapshots", name)
		}
		if total > a.maxTotal-bytes {
			return nil, errors.New("graph PVC logical capacity exceeds the policy total limit")
		}
		total += bytes
		authorized[name] = rule
	}
	return authorized, nil
}

func parseGraphStorageBytes(raw string) (int64, error) {
	quantity, err := resource.ParseQuantity(strings.TrimSpace(raw))
	if err != nil || quantity.Sign() <= 0 {
		return 0, errors.New("positive storage quantity required")
	}
	value, ok := quantity.AsInt64()
	if !ok || value <= 0 {
		return 0, errors.New("storage quantity is not a positive int64")
	}
	return value, nil
}

func readGraphStoragePolicy(path string) ([]byte, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > graphStoragePolicyMaxBytes {
		return nil, nil, errors.New("graph storage policy must be a regular file no larger than 1 MiB")
	}
	file, err := openGraphStoragePolicyFile(path)
	if err != nil {
		return nil, nil, errors.New("graph storage policy must not be a symlink or special file")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() > graphStoragePolicyMaxBytes {
		return nil, nil, errors.New("graph storage policy file changed or is not regular")
	}
	body, err := io.ReadAll(io.LimitReader(file, graphStoragePolicyMaxBytes+1))
	if err != nil || len(body) > graphStoragePolicyMaxBytes {
		return nil, nil, errors.New("graph storage policy exceeds 1 MiB")
	}
	return body, after, nil
}

func checkGraphStoragePolicyNode(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return errors.New("graph storage policy aliases and anchors are not supported")
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" || seen[key.Value] {
				return errors.New("graph storage policy has duplicate or non-string keys")
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := checkGraphStoragePolicyNode(child); err != nil {
			return err
		}
	}
	return nil
}
