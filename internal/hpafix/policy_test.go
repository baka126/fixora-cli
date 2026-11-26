package hpafix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePolicy(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validPolicy = `apiVersion: fixora.io/v1alpha1
kind: HPARepairPolicy
metadata: {name: web-policy, namespace: prod}
spec:
  hpa: {name: web-scale}
  target: {kind: Deployment, name: web}
  requests:
    - {container: app, cpu: 250m, memory: 128Mi}
`

func TestLoadPolicyStrictSchema(t *testing.T) {
	for name, body := range map[string]string{
		"valid":              validPolicy,
		"unknown field":      strings.Replace(validPolicy, "spec:", "unexpected: true\nspec:", 1),
		"multiple documents": validPolicy + "---\napiVersion: fixora.io/v1alpha1\n",
		"duplicate key":      strings.Replace(validPolicy, "metadata: {name: web-policy, namespace: prod}", "metadata:\n  name: web-policy\n  name: other\n  namespace: prod", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadPolicy(writePolicy(t, body))
			if (name == "valid") != (err == nil) {
				t.Fatalf("LoadPolicy err=%v", err)
			}
		})
	}
}

func TestLoadPolicyRejectsDuplicateKeysAndSpecialFiles(t *testing.T) {
	path := writePolicy(t, validPolicy)
	link := filepath.Join(t.TempDir(), "link.yaml")
	if err := os.Symlink(path, link); err == nil {
		if _, err := LoadPolicy(link); err == nil {
			t.Fatal("symlink policy accepted")
		}
	}
	tooLarge := writePolicy(t, validPolicy+strings.Repeat("#", (1<<20)+1))
	if _, err := LoadPolicy(tooLarge); err == nil {
		t.Fatal("oversized policy accepted")
	}
}

func TestLoadPolicyRejectsInvalidQuantitiesAndDuplicateContainers(t *testing.T) {
	for _, body := range []string{
		strings.Replace(validPolicy, "cpu: 250m", "cpu: not-a-quantity", 1),
		strings.Replace(validPolicy, "    - {container: app, cpu: 250m, memory: 128Mi}", "    - {container: app, cpu: 250m}\n    - {container: app, memory: 128Mi}", 1),
	} {
		if _, err := LoadPolicy(writePolicy(t, body)); err == nil {
			t.Fatalf("invalid policy accepted:\n%s", body)
		}
	}
}

func TestPolicyRecheckRejectsReplacement(t *testing.T) {
	path := writePolicy(t, validPolicy)
	authority, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(t.TempDir(), "replacement.yaml")
	if err := os.WriteFile(replacement, []byte(validPolicy), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if err := authority.check(); err == nil {
		t.Fatal("replaced policy file retained authority")
	}
}
