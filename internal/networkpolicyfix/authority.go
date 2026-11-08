package networkpolicyfix

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
	"reflect"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	syaml "sigs.k8s.io/yaml"
)

var probeImageDigest = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*@sha256:[0-9a-f]{64}$`)

func LoadAuthority(path string) (*Authority, error) {
	if strings.TrimSpace(path) == "" || strings.Contains(path, "://") {
		return nil, errors.New("local NetworkPolicy authority file required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("policy path unavailable")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, errors.New("policy directory unavailable")
	}
	abs = filepath.Join(parent, filepath.Base(abs))
	data, identity, err := readAuthority(abs)
	if err != nil {
		return nil, err
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	var node yaml.Node
	if d.Decode(&node) != nil || len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("NetworkPolicy authority must be one YAML object")
	}
	if err := checkAuthorityNode(&node); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return nil, errors.New("exactly one NetworkPolicy authority document is required")
	}
	raw, err := syaml.YAMLToJSON(data)
	if err != nil {
		return nil, errors.New("invalid NetworkPolicy authority YAML")
	}
	var intent authorityDocument
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&intent); err != nil {
		return nil, errors.New("invalid or unknown NetworkPolicy authority fields")
	}
	if err := validateAuthority(&intent); err != nil {
		return nil, err
	}
	var spec networkingv1.NetworkPolicySpec
	specDecoder := json.NewDecoder(bytes.NewReader(intent.Spec.NetworkPolicySpec))
	specDecoder.DisallowUnknownFields()
	if err := specDecoder.Decode(&spec); err != nil {
		return nil, errors.New("unsupported or unknown NetworkPolicy spec field")
	}
	if err := validateNetworkPolicySpec(spec, intent.Spec.NetworkPolicySpec, intent.Spec.Verification.Cases); err != nil {
		return nil, err
	}
	timeout, err := time.ParseDuration(intent.Spec.Verification.Timeout)
	if err != nil || timeout < time.Second || timeout > 10*time.Second {
		return nil, errors.New("NetworkPolicy probe timeout must be between 1s and 10s")
	}
	return &Authority{path: abs, digest: sha256.Sum256(data), identity: identity, intent: intent, policy: *spec.DeepCopy(), policyJSON: append(json.RawMessage(nil), intent.Spec.NetworkPolicySpec...), timeout: timeout}, nil
}

func validateAuthority(p *authorityDocument) error {
	if p.APIVersion != "fixora.io/v1alpha1" || p.Kind != "NetworkPolicyRepairPolicy" {
		return errors.New("NetworkPolicyRepairPolicy fixora.io/v1alpha1 identity required")
	}
	t := p.Spec.Target
	if len(validation.IsDNS1123Label(t.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(t.Name)) != 0 {
		return errors.New("exact valid NetworkPolicy target required")
	}
	w := p.Spec.Workload
	if len(validation.IsDNS1123Subdomain(w.Name)) != 0 || (w.Kind != "Deployment" && w.Kind != "StatefulSet" && w.Kind != "DaemonSet") {
		return errors.New("approved Deployment, StatefulSet, or DaemonSet required")
	}
	if len(p.Spec.NetworkPolicySpec) == 0 {
		return errors.New("complete NetworkPolicy spec required")
	}
	v := p.Spec.Verification
	if !probeImageDigest.MatchString(v.ProbeImage) || len(v.Cases) < 2 || len(v.Cases) > 128 {
		return errors.New("digest-pinned probe image and bounded connectivity cases required")
	}
	return nil
}

func validateNetworkPolicySpec(spec networkingv1.NetworkPolicySpec, raw json.RawMessage, cases []ConnectivityCase) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return errors.New("invalid NetworkPolicy spec")
	}
	for _, key := range []string{"podSelector", "policyTypes", "ingress", "egress"} {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("NetworkPolicy spec requires explicit non-null %s", key)
		}
	}
	if !selectorNonEmpty(spec.PodSelector) {
		return errors.New("non-empty NetworkPolicy podSelector required")
	}
	if _, err := metav1.LabelSelectorAsSelector(&spec.PodSelector); err != nil {
		return errors.New("invalid NetworkPolicy podSelector")
	}
	if len(spec.PolicyTypes) == 0 || len(spec.PolicyTypes) > 2 {
		return errors.New("explicit ingress and/or egress policyTypes required")
	}
	directions := map[string]bool{}
	for _, pt := range spec.PolicyTypes {
		if pt != networkingv1.PolicyTypeIngress && pt != networkingv1.PolicyTypeEgress || directions[string(pt)] {
			return errors.New("unsupported or duplicate NetworkPolicy policyType")
		}
		directions[string(pt)] = true
	}
	if (len(spec.Ingress) > 0) != directions[string(networkingv1.PolicyTypeIngress)] || (len(spec.Egress) > 0) != directions[string(networkingv1.PolicyTypeEgress)] {
		return errors.New("explicit policyTypes must match non-empty ingress and egress rule lists")
	}
	for i, rule := range spec.Ingress {
		if err := validatePeersAndPorts(rule.From, rule.Ports); err != nil {
			return fmt.Errorf("unsupported ingress rule %d: %w", i+1, err)
		}
	}
	for i, rule := range spec.Egress {
		if err := validatePeersAndPorts(rule.To, rule.Ports); err != nil {
			return fmt.Errorf("unsupported egress rule %d: %w", i+1, err)
		}
	}
	seen := map[string]bool{}
	counts := map[string]map[string]int{"ingress": {}, "egress": {}}
	for _, c := range cases {
		if len(validation.IsDNS1123Label(c.Name)) != 0 || seen[c.Name] || (c.Direction != "ingress" && c.Direction != "egress") || (c.Expected != "allow" && c.Expected != "deny") || c.Port < 1 || c.Port > 65535 {
			return errors.New("invalid or duplicate connectivity case")
		}
		seen[c.Name] = true
		if err := validateLabelMap(c.SourcePodLabels); err != nil {
			return errors.New("invalid source pod labels")
		}
		if err := validateLabelMap(c.DestinationPodLabels); err != nil {
			return errors.New("invalid destination pod labels")
		}
		if err := validateLabelMap(c.SourceNamespaceLabels); err != nil {
			return errors.New("invalid source namespace labels")
		}
		if err := validateLabelMap(c.DestinationNamespaceLabels); err != nil {
			return errors.New("invalid destination namespace labels")
		}
		if _, found := c.SourceNamespaceLabels["kubernetes.io/metadata.name"]; found {
			return errors.New("namespace-name-dependent test cases are unsupported")
		}
		if _, found := c.DestinationNamespaceLabels["kubernetes.io/metadata.name"]; found {
			return errors.New("namespace-name-dependent test cases are unsupported")
		}
		if c.Direction == "ingress" && !directions[string(networkingv1.PolicyTypeIngress)] || c.Direction == "egress" && !directions[string(networkingv1.PolicyTypeEgress)] {
			return errors.New("test case direction is not isolated by policyTypes")
		}
		targetLabels := c.DestinationPodLabels
		if c.Direction == "egress" {
			targetLabels = c.SourcePodLabels
		}
		if !selectorMatches(&spec.PodSelector, targetLabels) {
			return errors.New("test case target pod labels do not match policy podSelector")
		}
		counts[c.Direction][c.Expected]++
	}
	for _, direction := range spec.PolicyTypes {
		name := strings.ToLower(string(direction))
		if counts[name]["allow"] == 0 || counts[name]["deny"] == 0 {
			return errors.New("each isolated direction requires an allow and deny case")
		}
	}
	for _, c := range cases {
		matches := caseMatchesAnyRule(c, spec)
		if c.Expected == "deny" && matches {
			return errors.New("denied test case matches an approved allow rule")
		}
		if c.Expected == "allow" && !matches {
			return errors.New("allowed test case matches no approved allow rule")
		}
	}
	for i := range spec.Ingress {
		if !coveredByAllowCase("ingress", i, cases, spec) {
			return fmt.Errorf("ingress rule %d lacks an allow test case", i+1)
		}
	}
	for i := range spec.Egress {
		if !coveredByAllowCase("egress", i, cases, spec) {
			return fmt.Errorf("egress rule %d lacks an allow test case", i+1)
		}
	}
	return nil
}

func validatePeersAndPorts(peers []networkingv1.NetworkPolicyPeer, ports []networkingv1.NetworkPolicyPort) error {
	if len(peers) == 0 || len(ports) == 0 {
		return errors.New("explicit peers and numeric TCP ports required")
	}
	for _, peer := range peers {
		if peer.IPBlock != nil || (peer.PodSelector == nil && peer.NamespaceSelector == nil) {
			return errors.New("only explicit Pod/namespace selector peers are supported")
		}
		if peer.PodSelector != nil {
			if _, err := metav1.LabelSelectorAsSelector(peer.PodSelector); err != nil {
				return errors.New("invalid peer podSelector")
			}
		}
		if peer.NamespaceSelector != nil {
			if _, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector); err != nil {
				return errors.New("invalid peer namespaceSelector")
			}
			if selectorUsesNamespaceName(peer.NamespaceSelector) {
				return errors.New("namespace-name-dependent selectors are unsupported")
			}
		}
	}
	for _, port := range ports {
		if port.Protocol == nil || *port.Protocol != "TCP" || port.Port == nil || port.Port.Type != intstr.Int || port.Port.IntVal < 1 || port.Port.IntVal > 65535 || port.EndPort != nil {
			return errors.New("only explicit numeric TCP ports without endPort are supported")
		}
	}
	return nil
}

func coveredByAllowCase(direction string, ruleIndex int, cases []ConnectivityCase, spec networkingv1.NetworkPolicySpec) bool {
	for _, c := range cases {
		if c.Direction != direction || c.Expected != "allow" {
			continue
		}
		var peers []networkingv1.NetworkPolicyPeer
		var ports []networkingv1.NetworkPolicyPort
		if direction == "ingress" {
			peers, ports = spec.Ingress[ruleIndex].From, spec.Ingress[ruleIndex].Ports
		} else {
			peers, ports = spec.Egress[ruleIndex].To, spec.Egress[ruleIndex].Ports
		}
		for _, port := range ports {
			if port.Port == nil || port.Port.IntVal != c.Port {
				continue
			}
			for _, peer := range peers {
				if peerMatchesCase(peer, direction, c) {
					return true
				}
			}
		}
	}
	return false
}

func peerMatchesCase(peer networkingv1.NetworkPolicyPeer, direction string, c ConnectivityCase) bool {
	podLabels, nsLabels := c.SourcePodLabels, c.SourceNamespaceLabels
	policyNamespaceLabels := c.DestinationNamespaceLabels
	if direction == "egress" {
		podLabels, nsLabels = c.DestinationPodLabels, c.DestinationNamespaceLabels
		policyNamespaceLabels = c.SourceNamespaceLabels
	}
	// A peer with only podSelector is scoped to the policy's namespace by
	// Kubernetes semantics; it cannot match a Pod in the other test namespace.
	if peer.PodSelector != nil && peer.NamespaceSelector == nil && !reflect.DeepEqual(nsLabels, policyNamespaceLabels) {
		return false
	}
	if peer.PodSelector != nil && !selectorMatches(peer.PodSelector, podLabels) {
		return false
	}
	if peer.NamespaceSelector != nil && !selectorMatches(peer.NamespaceSelector, nsLabels) {
		return false
	}
	return true
}

func caseMatchesAnyRule(c ConnectivityCase, spec networkingv1.NetworkPolicySpec) bool {
	if c.Direction == "ingress" {
		for i := range spec.Ingress {
			if caseMatchesRule(c, "ingress", i, spec) {
				return true
			}
		}
	} else {
		for i := range spec.Egress {
			if caseMatchesRule(c, "egress", i, spec) {
				return true
			}
		}
	}
	return false
}

func caseMatchesRule(c ConnectivityCase, direction string, i int, spec networkingv1.NetworkPolicySpec) bool {
	var peers []networkingv1.NetworkPolicyPeer
	var ports []networkingv1.NetworkPolicyPort
	if direction == "ingress" {
		peers, ports = spec.Ingress[i].From, spec.Ingress[i].Ports
	} else {
		peers, ports = spec.Egress[i].To, spec.Egress[i].Ports
	}
	for _, port := range ports {
		if port.Port != nil && port.Port.IntVal == c.Port {
			for _, peer := range peers {
				if peerMatchesCase(peer, direction, c) {
					return true
				}
			}
		}
	}
	return false
}

func selectorNonEmpty(selector metav1.LabelSelector) bool {
	return len(selector.MatchLabels) > 0 || len(selector.MatchExpressions) > 0
}

func selectorMatches(selector *metav1.LabelSelector, values map[string]string) bool {
	parsed, err := metav1.LabelSelectorAsSelector(selector)
	return err == nil && parsed.Matches(labels.Set(values))
}

func selectorUsesNamespaceName(selector *metav1.LabelSelector) bool {
	if selector == nil {
		return false
	}
	if _, ok := selector.MatchLabels["kubernetes.io/metadata.name"]; ok {
		return true
	}
	for _, requirement := range selector.MatchExpressions {
		if requirement.Key == "kubernetes.io/metadata.name" {
			return true
		}
	}
	return false
}

func validateLabelMap(value map[string]string) error {
	selector := metav1.LabelSelector{MatchLabels: value}
	_, err := metav1.LabelSelectorAsSelector(&selector)
	return err
}

func (a *Authority) Target() Target {
	if a == nil {
		return Target{}
	}
	return a.intent.Spec.Target
}
func (a *Authority) Workload() WorkloadRef {
	if a == nil {
		return WorkloadRef{}
	}
	return a.intent.Spec.Workload
}
func (a *Authority) NetworkPolicySpec() networkingv1.NetworkPolicySpec {
	if a == nil {
		return networkingv1.NetworkPolicySpec{}
	}
	return *a.policy.DeepCopy()
}
func (a *Authority) SpecJSON() []byte {
	if a == nil {
		return nil
	}
	return append([]byte(nil), a.policyJSON...)
}
func (a *Authority) Cases() []ConnectivityCase {
	if a == nil {
		return nil
	}
	out := make([]ConnectivityCase, len(a.intent.Spec.Verification.Cases))
	for i, c := range a.intent.Spec.Verification.Cases {
		out[i] = c
		out[i].SourcePodLabels = cloneLabels(c.SourcePodLabels)
		out[i].SourceNamespaceLabels = cloneLabels(c.SourceNamespaceLabels)
		out[i].DestinationPodLabels = cloneLabels(c.DestinationPodLabels)
		out[i].DestinationNamespaceLabels = cloneLabels(c.DestinationNamespaceLabels)
	}
	return out
}
func (a *Authority) ProbeImage() string {
	if a == nil {
		return ""
	}
	return a.intent.Spec.Verification.ProbeImage
}
func (a *Authority) Timeout() time.Duration {
	if a == nil {
		return 0
	}
	return a.timeout
}

func (a *Authority) CheckUnchanged() error {
	if a == nil || a.path == "" || a.identity == nil {
		return errors.New("fresh NetworkPolicy authority required")
	}
	data, identity, err := readAuthority(a.path)
	if err != nil || !os.SameFile(a.identity, identity) || sha256.Sum256(data) != a.digest {
		return errors.New("NetworkPolicy authority changed; prepare again")
	}
	return nil
}

func (a *Authority) ProposalContext(namespace, name string) (string, error) {
	if a == nil || namespace != a.intent.Spec.Target.Namespace || name != a.intent.Spec.Target.Name {
		return "", errors.New("proposal target does not match operator authority")
	}
	return fmt.Sprintf("The operator-supplied complete NetworkPolicy for %s/%s is authoritative. AI may explain evidence but cannot change policy rules, connectivity cases, or delivery approval.", namespace, name), nil
}

func cloneLabels(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func checkAuthorityNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.New("NetworkPolicy authority aliases and anchors are unsupported")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" || seen[key.Value] {
				return errors.New("duplicate or non-string NetworkPolicy authority key")
			}
			seen[key.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := checkAuthorityNode(child); err != nil {
			return err
		}
	}
	return nil
}

func readAuthority(path string) ([]byte, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxAuthorityBytes {
		return nil, nil, errors.New("NetworkPolicy authority must be a regular file no larger than 1 MiB")
	}
	f, err := openAuthorityFile(path)
	if err != nil {
		return nil, nil, errors.New("NetworkPolicy authority must be opened without following links")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() > maxAuthorityBytes {
		return nil, nil, errors.New("NetworkPolicy authority file changed or is not regular")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxAuthorityBytes+1))
	if err != nil || len(data) > maxAuthorityBytes {
		return nil, nil, errors.New("NetworkPolicy authority exceeds 1 MiB")
	}
	return data, after, nil
}

func authorityNonce(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}
