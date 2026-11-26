package servicefix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validPolicy = `apiVersion: fixora.io/v1alpha1
kind: ServiceRepairPolicy
metadata:
  name: route-repair
  namespace: sample
spec:
  service: {name: gateway}
  backend: {kind: Deployment, name: arbitrary-app}
  selector: {workload: arbitrary}
  port: {name: http, port: 80, targetPort: http}
  verification:
    method: http
    path: /ready
    expectedStatus: 200
    timeout: 5s
    probeImage: example.invalid/fixora-probe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`

func policyFile(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestServicePolicyStrictIntent(t *testing.T) {
	a, err := LoadPolicy(policyFile(t, validPolicy))
	if err != nil || a == nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ old, new string }{
		{"targetPort: http", "targetPort: '8080'"}, {"selector: {workload: arbitrary}", "selector: {}"}, {"expectedStatus: 200", "expectedStatus: 500"}, {"path: /ready", "path: //other/ready"}, {"method: http", "method: tcp"}, {"timeout: 5s", "timeout: 100s"}, {"backend: {kind: Deployment", "backend: {kind: Job"}, {"spec:\n", "spec:\n  credentials: forbidden\n"}, {"name: route-repair", "name: route-repair\n  name: duplicate"}, {"selector: {workload: arbitrary}", "selector: &s {workload: arbitrary}"},
	} {
		t.Run(change.new, func(t *testing.T) {
			if _, err := LoadPolicy(policyFile(t, strings.Replace(validPolicy, change.old, change.new, 1))); err == nil {
				t.Fatal("unsafe policy accepted")
			}
		})
	}
	if _, err := LoadPolicy(policyFile(t, validPolicy+"---\n"+validPolicy)); err == nil {
		t.Fatal("multiple documents")
	}
	p := policyFile(t, validPolicy)
	link := p + ".link"
	os.Symlink(p, link)
	if _, err := LoadPolicy(link); err == nil {
		t.Fatal("symlink")
	}
	if _, err := LoadPolicy(policyFile(t, strings.Repeat(" ", 1<<20)+validPolicy)); err == nil {
		t.Fatal("oversized")
	}
}
func TestServicePolicyRejectsHTTPFragment(t *testing.T) {
	if _, err := LoadPolicy(policyFile(t, strings.Replace(validPolicy, "path: /ready", "path: /ready#fragment", 1))); err == nil {
		t.Fatal("fragment accepted")
	}
}
