package networkpolicyfix

import (
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validAuthorityYAML = `apiVersion: fixora.io/v1alpha1
kind: NetworkPolicyRepairPolicy
spec:
  target: {namespace: fixora-demo, name: db-policy}
  workload: {kind: Deployment, name: database}
  networkPolicySpec:
    podSelector: {matchLabels: {app: db}}
    policyTypes: [Ingress, Egress]
    ingress:
      - from:
          - namespaceSelector: {matchLabels: {env: prod}}
            podSelector: {matchLabels: {role: frontend}}
        ports: [{protocol: TCP, port: 5432}]
    egress:
      - to:
          - namespaceSelector: {matchLabels: {env: prod}}
            podSelector: {matchLabels: {role: frontend}}
        ports: [{protocol: TCP, port: 5432}]
  verification:
    probeImage: ghcr.io/fixora/probe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    timeout: 5s
    cases:
      - name: ingress-allow
        direction: ingress
        sourcePodLabels: {role: frontend}
        sourceNamespaceLabels: {env: prod}
        destinationPodLabels: {app: db}
        destinationNamespaceLabels: {team: demo}
        port: 5432
        expected: allow
      - name: ingress-deny
        direction: ingress
        sourcePodLabels: {role: other}
        sourceNamespaceLabels: {env: prod}
        destinationPodLabels: {app: db}
        destinationNamespaceLabels: {team: demo}
        port: 5432
        expected: deny
      - name: egress-allow
        direction: egress
        sourcePodLabels: {app: db}
        sourceNamespaceLabels: {team: demo}
        destinationPodLabels: {role: frontend}
        destinationNamespaceLabels: {env: prod}
        port: 5432
        expected: allow
      - name: egress-deny
        direction: egress
        sourcePodLabels: {app: db}
        sourceNamespaceLabels: {team: demo}
        destinationPodLabels: {role: other}
        destinationNamespaceLabels: {env: prod}
        port: 5432
        expected: deny
`

func writeAuthority(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAuthorityAcceptsCompleteIngressAndEgressIntent(t *testing.T) {
	a, err := LoadAuthority(writeAuthority(t, validAuthorityYAML))
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Target(); got != (Target{Namespace: "fixora-demo", Name: "db-policy"}) {
		t.Fatalf("target = %+v", got)
	}
	if got := a.Workload(); got != (WorkloadRef{Kind: "Deployment", Name: "database"}) {
		t.Fatalf("workload = %+v", got)
	}
	if len(a.Cases()) != 4 || a.ProbeImage() == "" {
		t.Fatalf("verification intent not loaded: %+v", a.Cases())
	}
	if err := a.CheckUnchanged(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityProbeTimeoutMatchesProbeProtocol(t *testing.T) {
	if _, err := LoadAuthority(writeAuthority(t, strings.Replace(validAuthorityYAML, "timeout: 5s", "timeout: 10s", 1))); err != nil {
		t.Fatalf("10s probe timeout rejected: %v", err)
	}
	if _, err := LoadAuthority(writeAuthority(t, strings.Replace(validAuthorityYAML, "timeout: 5s", "timeout: 11s", 1))); err == nil {
		t.Fatal("timeout beyond serviceprobe protocol accepted")
	}
}

func TestPodSelectorOnlyPeerUsesPolicyNamespaceSemantics(t *testing.T) {
	peer := networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "client"}}}
	c := ConnectivityCase{Direction: "ingress", SourcePodLabels: map[string]string{"role": "client"}, SourceNamespaceLabels: map[string]string{"env": "other"}, DestinationNamespaceLabels: map[string]string{"env": "target"}}
	if peerMatchesCase(peer, "ingress", c) {
		t.Fatal("podSelector-only peer matched a different namespace")
	}
	c.SourceNamespaceLabels = map[string]string{"env": "target"}
	if !peerMatchesCase(peer, "ingress", c) {
		t.Fatal("same-namespace podSelector peer did not match")
	}
}

func TestDocumentedNetworkPolicyAuthorityExampleLoads(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "guides", "examples", "networkpolicy-authority.yaml")
	a, err := LoadAuthority(path)
	if err != nil {
		t.Fatalf("documented example is invalid: %v", err)
	}
	if a.Target() != (Target{Namespace: "application", Name: "api-policy"}) {
		t.Fatalf("unexpected example target: %+v", a.Target())
	}
}

func TestLoadAuthorityRejectsUnsafePolicyDocuments(t *testing.T) {
	ingressRule := "    ingress:\n      - from:\n          - namespaceSelector: {matchLabels: {env: prod}}\n            podSelector: {matchLabels: {role: frontend}}\n        ports: [{protocol: TCP, port: 5432}]\n"
	egressRule := "    egress:\n      - to:\n          - namespaceSelector: {matchLabels: {env: prod}}\n            podSelector: {matchLabels: {role: frontend}}\n        ports: [{protocol: TCP, port: 5432}]\n"
	cases := map[string]string{
		"unknown field":                  strings.Replace(validAuthorityYAML, "  target:", "  guessed: true\n  target:", 1),
		"duplicate field":                strings.Replace(validAuthorityYAML, "  workload:", "  target: {namespace: fixora-demo, name: another}\n  workload:", 1),
		"wrong api version":              strings.Replace(validAuthorityYAML, "fixora.io/v1alpha1", "fixora.io/v2", 1),
		"wrong kind":                     strings.Replace(validAuthorityYAML, "NetworkPolicyRepairPolicy", "NetworkPolicy", 1),
		"unsupported workload":           strings.Replace(validAuthorityYAML, "kind: Deployment", "kind: Job", 1),
		"bad target namespace":           strings.Replace(validAuthorityYAML, "namespace: fixora-demo", "namespace: bad_namespace", 1),
		"empty pod selector":             strings.Replace(validAuthorityYAML, "podSelector: {matchLabels: {app: db}}", "podSelector: {}", 1),
		"implicit policy types":          strings.Replace(validAuthorityYAML, "policyTypes: [Ingress, Egress]\n", "", 1),
		"namespace-name selector":        strings.Replace(validAuthorityYAML, "env: prod", "kubernetes.io/metadata.name: fixora-demo", 1),
		"missing egress rule field":      strings.Replace(validAuthorityYAML, egressRule, "", 1),
		"null ingress rules":             strings.Replace(validAuthorityYAML, ingressRule, "    ingress: null\n", 1),
		"empty ingress rules":            strings.Replace(validAuthorityYAML, ingressRule, "    ingress: []\n", 1),
		"ipBlock":                        strings.Replace(validAuthorityYAML, "namespaceSelector: {matchLabels: {env: prod}}\n            podSelector: {matchLabels: {role: frontend}}", "ipBlock: {cidr: 10.0.0.0/8}", 1),
		"named port":                     strings.Replace(validAuthorityYAML, "port: 5432", "port: db", 1),
		"endPort":                        strings.Replace(validAuthorityYAML, "port: 5432", "port: 5432, endPort: 5433", 1),
		"udp":                            strings.Replace(validAuthorityYAML, "protocol: TCP", "protocol: UDP", 1),
		"empty peers":                    strings.Replace(validAuthorityYAML, "from:\n          - namespaceSelector: {matchLabels: {env: prod}}\n            podSelector: {matchLabels: {role: frontend}}", "from: []", 1),
		"empty ports":                    strings.Replace(validAuthorityYAML, "ports: [{protocol: TCP, port: 5432}]", "ports: []", 1),
		"missing positive rule coverage": strings.Replace(validAuthorityYAML, "        expected: allow\n", "        expected: deny\n", 1),
		"unpinned probe image":           strings.Replace(validAuthorityYAML, "ghcr.io/fixora/probe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "ghcr.io/fixora/probe:latest", 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadAuthority(writeAuthority(t, body)); err == nil {
				t.Fatal("unsafe policy authority accepted")
			}
		})
	}
	if _, err := LoadAuthority(writeAuthority(t, validAuthorityYAML+"---\n"+validAuthorityYAML)); err == nil {
		t.Fatal("multiple YAML documents accepted")
	}
}

func TestLoadAuthorityRejectsSymlinkOversizedAndChangedFiles(t *testing.T) {
	path := writeAuthority(t, validAuthorityYAML)
	a, err := LoadAuthority(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(validAuthorityYAML+"\n# changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.CheckUnchanged(); err == nil {
		t.Fatal("changed authority file retained approval")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	if err := os.WriteFile(target, []byte(validAuthorityYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthority(link); err == nil {
		t.Fatal("symlink authority accepted")
	}
	large := filepath.Join(dir, "large.yaml")
	if err := os.WriteFile(large, []byte(strings.Repeat("#", maxAuthorityBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthority(large); err == nil {
		t.Fatal("oversized authority accepted")
	}
}

func TestAuthorityReturnsCopiesAndRedactsProposal(t *testing.T) {
	a, err := LoadAuthority(writeAuthority(t, validAuthorityYAML))
	if err != nil {
		t.Fatal(err)
	}
	spec := a.NetworkPolicySpec()
	spec.PodSelector.MatchLabels["app"] = "changed"
	cases := a.Cases()
	cases[0].SourcePodLabels["role"] = "changed"
	if a.NetworkPolicySpec().PodSelector.MatchLabels["app"] != "db" || a.Cases()[0].SourcePodLabels["role"] != "frontend" {
		t.Fatal("public copies mutated authority")
	}
	proposal, err := a.ProposalContext("fixora-demo", "db-policy")
	if err != nil || !strings.Contains(proposal, "operator-supplied complete NetworkPolicy") || strings.Contains(proposal, "sha256:") {
		t.Fatalf("proposal context was not bounded/redacted: %q (%v)", proposal, err)
	}
}
