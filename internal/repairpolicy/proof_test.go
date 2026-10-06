package repairpolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func fixture(t *testing.T) Snapshot {
	t.Helper()
	obj, err := kube.WorkloadPatch("Deployment", "worker", "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "image:v1", "command": []any{"old"}}}})
	if err != nil {
		t.Fatal(err)
	}
	obj["metadata"].(map[string]any)["uid"] = "owner-1"
	return Snapshot{Target: Target{Kind: "Deployment", Name: "worker", Namespace: "demo", UID: "owner-1"}, Object: obj}
}
func TestProofRejectsChangedIdentityTemplateCandidateAndAuthority(t *testing.T) {
	s := fixture(t)
	p, err := BindCommon(s, "candidate", "image", func(string, string, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Check(s, "candidate"); err != nil {
		t.Fatal(err)
	}
	if err = p.Check(s, "edited"); err == nil {
		t.Fatal("edited candidate accepted")
	}
	s.Target.UID = "owner-2"
	if err = p.Check(s, "candidate"); err == nil {
		t.Fatal("replaced target accepted")
	}
	s = fixture(t)
	spec, _ := kube.WorkloadPodSpec(s.Object)
	spec["containers"].([]any)[0].(map[string]any)["image"] = "new:v2"
	if err = p.Check(s, "candidate"); err == nil {
		t.Fatal("changed template accepted")
	}
	// Binding must not share the caller's mutable object.
	if err = p.Check(fixture(t), "candidate"); err != nil {
		t.Fatal(err)
	}
	if _, err = BindCommon(fixture(t), "candidate", "restore-command", func(string, string, string, string) error { return nil }); err == nil {
		t.Fatal("common validator authorized startup")
	}
	if _, err = BindCommon(fixture(t), "candidate", "image", nil); err == nil {
		t.Fatal("missing validator accepted")
	}
}
func TestProofAndReceiptCannotRoundTripThroughJSON(t *testing.T) {
	p, err := BindCommon(fixture(t), "candidate", "image", func(string, string, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(p)
	var replay Proof
	if err = json.Unmarshal(data, &replay); err != nil {
		t.Fatal(err)
	}
	if err = replay.Check(fixture(t), "candidate"); err == nil {
		t.Fatal("serialized proof trusted")
	}
	var receipt Receipt
	if err = receipt.Check(p, "candidate"); err == nil {
		t.Fatal("empty receipt trusted")
	}
	if strings.Contains(string(data), "old") || strings.Contains(string(data), "owner-1") {
		t.Fatal("private evidence leaked")
	}
}
func baselineFile(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "baseline.yaml")
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

const baselineYAML = `apiVersion: apps/v1
kind: Deployment
metadata: {name: worker, namespace: demo}
spec:
  template:
    spec:
      containers:
      - name: app
        image: image:v1
        command: [approved]
`

func TestBaselineSingleDocumentSizeAndSecretRefusals(t *testing.T) {
	if _, err := ReadBaseline(baselineFile(t, baselineYAML)); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{baselineYAML + "---\n{}\n", strings.Repeat("x", 1024*1024+1), baselineYAML + "        envFrom: [{secretRef: {name: private}}]\n", "{}"} {
		if _, err := ReadBaseline(baselineFile(t, text)); err == nil {
			t.Fatal("invalid baseline accepted")
		}
	}
}
func TestAuthorityDoesNotAuthorizeOmittedFields(t *testing.T) {
	a, err := ReadBaseline(baselineFile(t, baselineYAML))
	if err != nil {
		t.Fatal(err)
	}
	if !a.authorizes([]string{"containers", "app", "command"}, []any{"approved"}) {
		t.Fatal("explicit command not authorized")
	}
	if a.authorizes([]string{"containers", "app", "args"}, []any{}) {
		t.Fatal("omission authorized removal")
	}
	if a.authorizes([]string{"containers", "app", "command"}, []any{"guessed"}) {
		t.Fatal("different value authorized")
	}
}
