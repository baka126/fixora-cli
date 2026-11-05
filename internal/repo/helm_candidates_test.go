package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const helmImageTarget = "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: worker, namespace: app}\nspec:\n  template:\n    spec:\n      containers:\n      - {name: app, image: old}\n"
const helmImagePatch = "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: worker, namespace: app}\nspec:\n  template:\n    spec:\n      containers:\n      - {name: app, image: new}\n"

func TestHelmCandidatesDynamicImageAndCommentPreservation(t *testing.T) {
	c := helmSourceFixture(t)
	in, err := CaptureHelmInputs(c)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := ParseHelmInventory([]byte(helmImageTarget), "app", testHelmScope)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := ProposeHelmValues(in, inv, ResourceKey{"apps/v1", "Deployment", "app", "worker"}, helmImagePatch, nil)
	if err != nil || len(candidates) == 0 {
		t.Fatal(candidates, err)
	}
	found := false
	for _, v := range candidates {
		if strings.Contains(string(v.edited), "setting: new") {
			found = true
			if !strings.Contains(string(v.edited), "# retained") || !strings.Contains(string(v.edited), "other: untouched") {
				t.Fatal(string(v.edited))
			}
		}
	}
	if !found {
		t.Fatal("dynamic key not proposed")
	}
	data, _ := os.ReadFile(filepath.Join(c.RepoPath, c.EditValuesPath))
	if strings.Contains(string(data), "setting: new") {
		t.Fatal("candidate generator wrote source")
	}
}
func TestHelmCandidatesExplicitPointersAndUnsafeYaml(t *testing.T) {
	c := helmSourceFixture(t)
	in, _ := CaptureHelmInputs(c)
	inv, _ := ParseHelmInventory([]byte(helmImageTarget), "app", testHelmScope)
	key := ResourceKey{"apps/v1", "Deployment", "app", "worker"}
	maps := []HelmMapping{{"/spec/template/spec/containers/0/image", "/setting"}}
	got, err := ProposeHelmValues(in, inv, key, helmImagePatch, maps)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	for _, bad := range []string{"setting: old\nsetting: other\n", "setting: &shared old\nother: *shared\n"} {
		os.WriteFile(filepath.Join(c.RepoPath, c.EditValuesPath), []byte(bad), 0640)
		in, _ := CaptureHelmInputs(c)
		if _, err := ProposeHelmValues(in, inv, key, helmImagePatch, maps); err == nil {
			t.Fatal("unsafe values accepted")
		}
	}
}
func TestHelmCandidatesDerivedImageParts(t *testing.T) {
	c := helmSourceFixture(t)
	os.WriteFile(filepath.Join(c.RepoPath, c.EditValuesPath), []byte("arbitraryBase: example/app\narbitraryRevision: v1\n"), 0640)
	in, _ := CaptureHelmInputs(c)
	inv, _ := ParseHelmInventory([]byte(strings.ReplaceAll(helmImageTarget, "image: old", "image: example/app:v1")), "app", testHelmScope)
	got, err := ProposeHelmValues(in, inv, ResourceKey{"apps/v1", "Deployment", "app", "worker"}, strings.ReplaceAll(helmImagePatch, "image: new", "image: example/app:v2"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if strings.Contains(string(c.edited), "arbitraryRevision: v2") {
			return
		}
	}
	t.Fatal("derived key candidate missing")
}

func TestHelmCandidatesDynamicResourceProbeAndConfigMap(t *testing.T) {
	for _, tc := range []struct{ field, old, next, key string }{
		{"resources: {requests: {memory: 32Mi}}", "32Mi", "64Mi", "peculiarDemand"},
		{"readinessProbe: {httpGet: {port: 8080}}", "8080", "9090", "strangeSocket"},
		{"env: [{name: HOST, valueFrom: {configMapKeyRef: {name: app-confg, key: host}}}]", "app-confg", "app-config", "unusualReference"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			c := helmSourceFixture(t)
			os.WriteFile(filepath.Join(c.RepoPath, c.EditValuesPath), []byte(tc.key+": "+tc.old+"\n"), 0640)
			in, _ := CaptureHelmInputs(c)
			base := strings.Replace(helmImageTarget, "image: old", "image: old, "+tc.field, 1)
			patch := strings.Replace(base, tc.old, tc.next, 1)
			inv, err := ParseHelmInventory([]byte(base), "app", testHelmScope)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ProposeHelmValues(in, inv, ResourceKey{"apps/v1", "Deployment", "app", "worker"}, patch, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range got {
				if strings.Contains(string(v.edited), tc.key+": "+tc.next) {
					return
				}
			}
			t.Fatal("dynamic scalar edit missing")
		})
	}
}

func TestHelmCandidatesRefuseSensitiveNamedEnvValues(t *testing.T) {
	value := []any{map[string]any{"name": "DATABASE_PASSWORD", "value": "synthetic-plaintext"}, map[string]any{"name": "HOST", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "settings", "key": "HOST"}}}}
	if !sensitiveHelmValue(value) {
		t.Fatal("parent candidate may expose unchanged sensitive env literal")
	}
}
