package coordinate

import (
	"strings"
	"testing"

	"encoding/json"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
)

func rollbackFixture(t *testing.T) (map[string]any, string) {
	obj, _ := kube.WorkloadPatch("Deployment", "worker", "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "old:v1", "env": []any{map[string]any{"name": "EXISTING", "value": "old"}}}}})
	obj["metadata"].(map[string]any)["uid"] = "owner-1"
	patch, _ := kube.WorkloadPatch("Deployment", "worker", "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "new:v2", "env": []any{map[string]any{"name": "NEW", "value": "added"}, map[string]any{"name": "EXISTING", "value": "new"}}}}})
	raw, _ := yaml.Marshal(patch)
	return obj, string(raw)
}
func applySpec(t *testing.T, obj map[string]any, patch string) map[string]any {
	t.Helper()
	var change map[string]any
	if err := yaml.Unmarshal([]byte(patch), &change); err != nil {
		t.Fatal(err)
	}
	before, _ := kube.WorkloadPodSpec(obj)
	delta, _ := kube.WorkloadPodSpec(change)
	br, _ := json.Marshal(map[string]any{"spec": before})
	pr, _ := json.Marshal(map[string]any{"spec": delta})
	raw, err := strategicpatch.StrategicMergePatch(br, pr, corev1.Pod{})
	if err != nil {
		t.Fatal(err)
	}
	var pod map[string]any
	_ = json.Unmarshal(raw, &pod)
	out, _ := kube.WorkloadPatch("Deployment", "worker", "demo", pod["spec"].(map[string]any))
	out["metadata"].(map[string]any)["uid"] = "owner-1"
	return out
}
func TestFieldRollbackPreservesUnrelatedChangesAndDeletesAddedFields(t *testing.T) {
	before, patch := rollbackFixture(t)
	inverse, err := BuildInversePatch(before, patch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inverse, "$patch: delete") {
		t.Fatal("added env entry has no deletion inverse")
	}
	live := applySpec(t, before, patch)
	spec, _ := kube.WorkloadPodSpec(live)
	spec["containers"].([]any)[0].(map[string]any)["resources"] = map[string]any{"limits": map[string]any{"memory": "512Mi"}}
	if err = CheckAppliedFields(live, patch, "owner-1"); err != nil {
		t.Fatal(err)
	}
	restored := applySpec(t, live, inverse)
	raw, _ := yaml.Marshal(restored)
	text := string(raw)
	if !strings.Contains(text, "old:v1") || !strings.Contains(text, "512Mi") || strings.Contains(text, "name: NEW") {
		t.Fatal(text)
	}
}
func TestRollbackRefusesChangedTouchedFieldsOrReplacement(t *testing.T) {
	before, patch := rollbackFixture(t)
	live := applySpec(t, before, patch)
	spec, _ := kube.WorkloadPodSpec(live)
	spec["containers"].([]any)[0].(map[string]any)["image"] = "other:v3"
	if err := CheckAppliedFields(live, patch, "owner-1"); err == nil {
		t.Fatal("concurrent touched field accepted")
	}
	live = applySpec(t, before, patch)
	live["metadata"].(map[string]any)["uid"] = "replacement"
	if err := CheckAppliedFields(live, patch, "owner-1"); err == nil {
		t.Fatal("replacement accepted")
	}
}

func TestRollbackRefusesUnrelatedEditInsideAddedNamedEntry(t *testing.T) {
	before, patch := rollbackFixture(t)
	live := applySpec(t, before, patch)
	spec, _ := kube.WorkloadPodSpec(live)
	env := spec["containers"].([]any)[0].(map[string]any)["env"].([]any)
	for _, raw := range env {
		entry := raw.(map[string]any)
		if entry["name"] == "NEW" {
			entry["extra"] = "concurrent"
		}
	}
	if err := CheckRollbackFields(live, before, patch, "owner-1"); err == nil {
		t.Fatal("would delete a concurrent edit inside a newly added entry")
	}
}

func TestRollbackRemovesNewValueFromAndRefusesConcurrentNestedEdits(t *testing.T) {
	before, _ := rollbackFixture(t)
	patchObj, _ := kube.WorkloadPatch("Deployment", "worker", "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "env": []any{map[string]any{"name": "EXISTING", "value": nil, "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "connection", "key": "host"}}}}}}})
	raw, _ := yaml.Marshal(patchObj)
	patch := string(raw)
	inverse, err := BuildInversePatch(before, patch)
	if err != nil {
		t.Fatal(err)
	}
	live := applySpec(t, before, patch)
	restored := applySpec(t, live, inverse)
	spec, _ := kube.WorkloadPodSpec(restored)
	entry := spec["containers"].([]any)[0].(map[string]any)["env"].([]any)[0].(map[string]any)
	if _, exists := entry["valueFrom"]; exists || entry["value"] != "old" {
		t.Fatalf("invalid literal restoration: %#v; inverse %s", entry, inverse)
	}
	spec, _ = kube.WorkloadPodSpec(live)
	entry = spec["containers"].([]any)[0].(map[string]any)["env"].([]any)[0].(map[string]any)
	entry["valueFrom"].(map[string]any)["configMapKeyRef"].(map[string]any)["optional"] = true
	if err := CheckRollbackFields(live, before, patch, "owner-1"); err == nil {
		t.Fatal("rollback would delete a concurrent nested edit")
	}
}

func TestRollbackAddedListPreservesConcurrentSibling(t *testing.T) {
	before, patch := rollbackFixture(t)
	spec, _ := kube.WorkloadPodSpec(before)
	delete(spec["containers"].([]any)[0].(map[string]any), "env")
	inverse, err := BuildInversePatch(before, patch)
	if err != nil {
		t.Fatal(err)
	}
	live := applySpec(t, before, patch)
	spec, _ = kube.WorkloadPodSpec(live)
	container := spec["containers"].([]any)[0].(map[string]any)
	container["env"] = append(container["env"].([]any), map[string]any{"name": "CONCURRENT", "value": "keep"})
	if err := CheckRollbackFields(live, before, patch, "owner-1"); err != nil {
		t.Fatal(err)
	}
	restored := applySpec(t, live, inverse)
	raw, _ := yaml.Marshal(restored)
	if !strings.Contains(string(raw), "CONCURRENT") || strings.Contains(string(raw), "name: NEW") {
		t.Fatal(string(raw))
	}
}

func TestAppliedFieldsAcceptResourceQuantityNormalization(t *testing.T) {
	before, _ := rollbackFixture(t)
	obj, _ := kube.WorkloadPatch("Deployment", "worker", "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "resources": map[string]any{"requests": map[string]any{"cpu": "0.5"}}}}})
	raw, _ := yaml.Marshal(obj)
	patch := string(raw)
	live := applySpec(t, before, patch)
	spec, _ := kube.WorkloadPodSpec(live)
	container := spec["containers"].([]any)[0].(map[string]any)
	container["resources"].(map[string]any)["requests"].(map[string]any)["cpu"] = "500m"
	if err := CheckAppliedFields(live, patch, "owner-1"); err != nil {
		t.Fatal(err)
	}
	if err := CheckRollbackFields(live, before, patch, "owner-1"); err != nil {
		t.Fatal(err)
	}
	container["resources"].(map[string]any)["requests"].(map[string]any)["cpu"] = "700m"
	if err := CheckAppliedFields(live, patch, "owner-1"); err == nil {
		t.Fatal("changed quantity accepted")
	}
}
