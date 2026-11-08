package kube

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGuardedApplyFileCarriesMutationPreconditions(t *testing.T) {
	source := filepath.Join(t.TempDir(), "patch.yaml")
	original := []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: app, namespace: ns}\nspec:\n  containers:\n  - name: app\n    image: busybox:1.36\n")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	guarded, cleanup, err := GuardedApplyFile(source, "u", "1234")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, _ := os.ReadFile(guarded)
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	meta := obj["metadata"].(map[string]any)
	if meta["uid"] != "u" || meta["resourceVersion"] != "1234" {
		t.Fatal(meta)
	}
	unchanged, _ := os.ReadFile(source)
	if string(unchanged) != string(original) {
		t.Fatal("source patch changed")
	}
	if _, _, err := GuardedApplyFile(source, "u", ""); err == nil {
		t.Fatal("missing version accepted")
	}
}
