package repo

import (
	"strings"
	"testing"
)

func testHelmScope(key ResourceKey) (bool, error) { return key.Kind != "Namespace", nil }
func TestHelmInventoryExactIdentityAndNamespace(t *testing.T) {
	a := "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: worker, namespace: app}\nspec: {replicas: 1}\n"
	inv, err := ParseHelmInventory([]byte(a), "default", testHelmScope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inv.Target(ResourceKey{"apps/v1", "Deployment", "app", "worker"}); err != nil {
		t.Fatal(err)
	}
	if _, err := inv.Target(ResourceKey{"apps/v1", "Deployment", "default", "worker"}); err == nil {
		t.Fatal("namespace guessed")
	}
	if _, err := inv.Target(ResourceKey{"apps/v1", "Deployment", "app", "chosen-worker"}); err == nil {
		t.Fatal("fuzzy name matched")
	}
	b := "spec: {replicas: 1}\nmetadata: {namespace: app, name: worker}\nkind: Deployment\napiVersion: apps/v1\n"
	other, err := ParseHelmInventory([]byte(b), "default", testHelmScope)
	if err != nil || other.Digest() != inv.Digest() {
		t.Fatal("map order changed digest", err)
	}
	for _, bad := range []string{a + "---\n" + a, "apiVersion: v1\nkind: List\nitems: []\n", "apiVersion: v1\nkind: Secret\nmetadata: {name: secret}\ndata: {private: private-secret-marker}\nmetadata: {name: other}\n"} {
		_, err := ParseHelmInventory([]byte(bad), "app", testHelmScope)
		if err == nil || strings.Contains(err.Error(), "private-secret-marker") {
			t.Fatal("bad or leaking parse", err)
		}
	}
}
