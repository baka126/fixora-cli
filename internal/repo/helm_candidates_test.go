package repo

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const helmImageTarget = "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: worker, namespace: app}\nspec:\n  template:\n    spec:\n      containers:\n      - {name: app, image: old}\n"
const helmImagePatch = "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: worker, namespace: app}\nspec:\n  template:\n    spec:\n      containers:\n      - {name: app, image: new}\n"

const helmServiceTarget = "apiVersion: v1\nkind: Service\nmetadata: {name: api, namespace: app}\nspec:\n  type: ClusterIP\n  selector: {app: old}\n  ports:\n  - {name: http, port: 80, protocol: TCP, targetPort: 8080}\n"
const helmServicePatch = "apiVersion: v1\nkind: Service\nmetadata: {name: api, namespace: app}\nspec:\n  selector:\n    $patch: replace\n    app: new\n  ports:\n  - {name: http, port: 80, protocol: TCP, targetPort: 9090}\n"

func TestHelmCandidatesExactServiceRoutingMappings(t *testing.T) {
	c := helmSourceFixture(t)
	if err := os.WriteFile(filepath.Join(c.RepoPath, c.EditValuesPath), []byte("selector: {app: old}\nbackend: 8080\n"), 0640); err != nil {
		t.Fatal(err)
	}
	in, err := CaptureHelmInputs(c)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := ParseHelmInventory([]byte(helmServiceTarget), "app", testHelmScope)
	if err != nil {
		t.Fatal(err)
	}
	key := ResourceKey{"v1", "Service", "app", "api"}
	mappings := []HelmMapping{{"/spec/selector", "/selector"}, {"/spec/ports/0/targetPort", "/backend"}}
	candidates, err := ProposeHelmValues(in, inv, key, helmServicePatch, mappings)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidate=%v err=%v", candidates, err)
	}
	if _, err := expectedHelmTarget(mustTarget(t, inv, key), helmServicePatch); err != nil {
		t.Fatalf("exact Service candidate rejected: %v", err)
	}
	withLegacySelector := strings.Replace(helmServiceTarget, "selector: {app: old}", "selector: {app: old, legacy: yes}", 1)
	legacyInv, err := ParseHelmInventory([]byte(withLegacySelector), "app", testHelmScope)
	if err != nil {
		t.Fatal(err)
	}
	legacyBase := mustTarget(t, legacyInv, key)
	legacyExpected, err := expectedHelmTarget(legacyBase, helmServicePatch)
	if err != nil {
		t.Fatal(err)
	}
	selector := legacyExpected["spec"].(map[string]any)["selector"].(map[string]any)
	if !reflect.DeepEqual(selector, map[string]any{"app": "new"}) {
		t.Fatalf("selector replacement retained stale labels: %#v", selector)
	}
	bad := strings.Replace(helmServicePatch, "targetPort: 9090", "targetPort: 9090, nodePort: 30080", 1)
	if _, err := expectedHelmTarget(mustTarget(t, inv, key), bad); err == nil {
		t.Fatal("unapproved Service field accepted")
	}
}

func mustTarget(t *testing.T, inv HelmInventory, key ResourceKey) map[string]any {
	t.Helper()
	obj, err := inv.Target(key)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestHelmServicePortIdentityRejectsAmbiguousOrReorderedPorts(t *testing.T) {
	base, err := ParseHelmInventory([]byte("apiVersion: v1\nkind: Service\nmetadata: {name: api, namespace: app}\nspec:\n  type: ClusterIP\n  selector: {app: old}\n  ports:\n  - {name: http, port: 80, protocol: TCP, targetPort: 8080}\n  - {name: metrics, port: 9091, protocol: TCP, targetPort: 9091}\n"), "app", testHelmScope)
	if err != nil {
		t.Fatal(err)
	}
	obj, _ := base.Target(ResourceKey{"v1", "Service", "app", "api"})
	if err := validateHelmServicePortOrder(obj, obj); err != nil {
		t.Fatal(err)
	}
	reordered := copyHelmObject(obj)
	ports := reordered["spec"].(map[string]any)["ports"].([]any)
	ports[0], ports[1] = ports[1], ports[0]
	if err := validateHelmServicePortOrder(obj, reordered); err == nil {
		t.Fatal("reordered Service ports accepted")
	}
	duplicate := copyHelmObject(obj)
	duplicate["spec"].(map[string]any)["ports"] = []any{ports[0], ports[0]}
	if err := validateHelmServicePortOrder(duplicate, duplicate); err == nil {
		t.Fatal("duplicate Service port identity accepted")
	}
	mergeDuplicate := copyHelmObject(obj)
	mergePorts := mergeDuplicate["spec"].(map[string]any)["ports"].([]any)
	mergePorts[1].(map[string]any)["name"] = "another-name"
	mergePorts[1].(map[string]any)["port"] = float64(80)
	if err := validateHelmService(mergeDuplicate); err == nil {
		t.Fatal("duplicate strategic-merge port key accepted")
	}
	for name, mutate := range map[string]func(map[string]any){
		"nodeport":     func(x map[string]any) { x["spec"].(map[string]any)["type"] = "NodePort" },
		"headless":     func(x map[string]any) { x["spec"].(map[string]any)["clusterIP"] = "None" },
		"selectorless": func(x map[string]any) { x["spec"].(map[string]any)["selector"] = map[string]any{} },
		"udp": func(x map[string]any) {
			x["spec"].(map[string]any)["ports"].([]any)[0].(map[string]any)["protocol"] = "UDP"
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := copyHelmObject(obj)
			mutate(bad)
			if err := validateHelmService(bad); err == nil {
				t.Fatalf("unsupported Service accepted: %s", name)
			}
		})
	}
}

func TestHelmServiceAllowsUnnamedDefaultTCPPortTargetPortChange(t *testing.T) {
	baseRaw := "apiVersion: v1\nkind: Service\nmetadata: {name: api, namespace: app}\nspec:\n  type: ClusterIP\n  selector: {app: old}\n  ports:\n  - {port: 80, targetPort: 8080}\n"
	patch := "apiVersion: v1\nkind: Service\nmetadata: {name: api, namespace: app}\nspec:\n  ports:\n  - {name: '', port: 80, protocol: TCP, targetPort: 9090}\n"
	inv, err := ParseHelmInventory([]byte(baseRaw), "app", testHelmScope)
	if err != nil {
		t.Fatal(err)
	}
	base, err := inv.Target(ResourceKey{"v1", "Service", "app", "api"})
	if err != nil {
		t.Fatal(err)
	}
	expected, err := expectedHelmTarget(base, patch)
	if err != nil {
		t.Fatalf("semantically unchanged port identity fields were rejected: %v", err)
	}
	afterRaw := strings.Replace(baseRaw, "targetPort: 8080", "targetPort: 9090", 1)
	after, err := ParseHelmInventory([]byte(afterRaw), "app", testHelmScope)
	if err != nil {
		t.Fatal(err)
	}
	if !exactHelmEffect(inv, after, ResourceKey{"v1", "Service", "app", "api"}, expected) {
		t.Fatal("semantically identical unnamed/default-TCP port identity did not match rendered effect")
	}
}

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
