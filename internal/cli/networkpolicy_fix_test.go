package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestNetworkPolicyRouteRequiresExactTargetAndAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, resource, namespace, policy string
		code                              int
		want                              string
	}{
		{"kind/name", "Deployment/api", "prod", "authority.yaml", 2, "NetworkPolicy/<name>"},
		{"namespace", "NetworkPolicy/db", "", "authority.yaml", 2, "-n <namespace>"},
		{"authority", "NetworkPolicy/db", "prod", "", 2, "--networkpolicy-policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errout bytes.Buffer
			code := runNetworkPolicyFix(t.Context(), &out, &errout, options{namespace: tc.namespace, networkPolicyPolicy: tc.policy}, tc.resource)
			if code != tc.code || !strings.Contains(errout.String(), tc.want) {
				t.Fatalf("code=%d err=%q", code, errout.String())
			}
		})
	}
}

func TestNetworkPolicyRouteRejectsMixedDeliveryModes(t *testing.T) {
	var out, errout bytes.Buffer
	code := runNetworkPolicyFix(t.Context(), &out, &errout, options{namespace: "prod", networkPolicyPolicy: "policy.yaml", repoPath: "./repo"}, "NetworkPolicy/db")
	if code != 2 || !strings.Contains(errout.String(), "cannot be combined") {
		t.Fatalf("code=%d err=%q", code, errout.String())
	}
}

func TestNetworkPolicyMachineReadableApplyRequiresExplicitYes(t *testing.T) {
	var out, errout bytes.Buffer
	code := runNetworkPolicyFix(t.Context(), &out, &errout, options{namespace: "prod", networkPolicyPolicy: "policy.yaml", apply: true, output: "json"}, "NetworkPolicy/db")
	if code != 2 || !strings.Contains(errout.String(), "requires --yes") {
		t.Fatalf("code=%d err=%q", code, errout.String())
	}
}
