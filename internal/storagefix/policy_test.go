package storagefix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPolicyAcceptsExactClassAndExpansionAuthority(t *testing.T) {
	for _, body := range []string{
		`apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec:
  pvc: {name: data}
  action: setStorageClass
  storageClassName: gp3
`,
		`apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec:
  pvc: {name: data}
  action: expand
  requestedStorage: 8Gi
  maxStorage: 10Gi
`,
	} {
		if _, err := LoadPolicy(writeStoragePolicy(t, body)); err != nil {
			t.Fatalf("LoadPolicy rejected valid policy: %v", err)
		}
	}
}

func TestLoadPolicyRejectsUnknownDuplicateAndUnboundedPolicy(t *testing.T) {
	valid := `apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec:
  pvc: {name: data}
  action: expand
  requestedStorage: 8Gi
  maxStorage: 10Gi
`
	cases := map[string]string{
		"unknown field":          strings.Replace(valid, "requestedStorage: 8Gi", "requestedStorage: 8Gi\n  guess: yes", 1),
		"duplicate key":          strings.Replace(valid, "action: expand", "action: expand\n  action: setStorageClass", 1),
		"missing maximum":        strings.Replace(valid, "  maxStorage: 10Gi\n", "", 1),
		"target exceeds maximum": strings.Replace(valid, "requestedStorage: 8Gi", "requestedStorage: 12Gi", 1),
		"invalid action":         strings.Replace(valid, "action: expand", "action: delete", 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadPolicy(writeStoragePolicy(t, body)); err == nil {
				t.Fatal("LoadPolicy accepted unsafe policy")
			}
		})
	}
}

func TestLoadPolicyRejectsSymlinkAndOversizedFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	if err := os.WriteFile(target, []byte("not a policy"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicy(link); err == nil {
		t.Fatal("LoadPolicy accepted a symlink")
	}
	large := filepath.Join(dir, "large.yaml")
	if err := os.WriteFile(large, []byte(strings.Repeat("#", maxPolicyBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicy(large); err == nil {
		t.Fatal("LoadPolicy accepted an oversized file")
	}
}

func TestAuthorityRejectsPolicyFileChangeAfterLoad(t *testing.T) {
	path := writeStoragePolicy(t, `apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec: {pvc: {name: data}, action: setStorageClass, storageClassName: gp3}
`)
	a, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(`apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata: {name: demo, namespace: fixora-demo}
spec: {pvc: {name: data}, action: setStorageClass, storageClassName: gp3-fast}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = a.check(); err == nil {
		t.Fatal("loaded authority survived a changed policy file")
	}
}

func writeStoragePolicy(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
