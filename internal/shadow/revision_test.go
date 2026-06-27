package shadow

import (
	"context"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type mockAI struct {
	patch   string
	finding analyzer.Finding
}

func (m *mockAI) Explain(ctx context.Context, finding analyzer.Finding) (*analyzer.AIResult, error) {
	m.finding = finding
	return &analyzer.AIResult{RecommendedFix: m.patch}, nil
}

const originalImagePatch = `spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v1
`

func TestRevisePatchAcceptsValidImageOnlyPatch(t *testing.T) {
	mock := &mockAI{patch: `spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
`}
	revised, ok, err := revisePatch(context.Background(), mock, originalImagePatch, "image", Attempt{ExitReason: "ImagePullBackOff"}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || !strings.Contains(revised, "repo/app:v2") {
		t.Fatalf("expected accepted revision, got ok=%t patch=%s", ok, revised)
	}
}

func TestRevisePatchPrefersStructuredPatchYAML(t *testing.T) {
	mock := &mockAIResult{result: &analyzer.AIResult{
		RecommendedFix: "Use a compatible image.",
		PatchYAML: `spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
`,
	}}
	revised, ok, err := revisePatch(context.Background(), mock, originalImagePatch, "image", Attempt{ExitReason: "ExecFormatError"}, true)
	if err != nil || !ok || !strings.Contains(revised, "repo/app:v2") {
		t.Fatalf("expected PatchYAML revision, ok=%t err=%v patch=%s", ok, err, revised)
	}
}

type mockAIResult struct {
	result *analyzer.AIResult
}

func (m *mockAIResult) Explain(ctx context.Context, finding analyzer.Finding) (*analyzer.AIResult, error) {
	return m.result, nil
}

func TestRevisePatchRequiresRedaction(t *testing.T) {
	mock := &mockAI{patch: originalImagePatch}
	_, ok, err := revisePatch(context.Background(), mock, originalImagePatch, "image", Attempt{Logs: []string{"password=hunter2"}}, false)
	if ok || err == nil {
		t.Fatalf("expected redaction-disabled retry rejection, ok=%t err=%v", ok, err)
	}
}

func TestRevisePatchRedactsBeforeAI(t *testing.T) {
	mock := &mockAI{patch: `spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
`}
	_, ok, err := revisePatch(context.Background(), mock, originalImagePatch, "image", Attempt{
		Logs:   []string{"DATABASE_URL=postgres://user:secret@example.internal/db password=hunter2"},
		Events: []string{"Bearer abcdefghijklmnopqrstuvwxyz123456"},
	}, true)
	if err != nil || !ok {
		t.Fatalf("expected accepted revision, ok=%t err=%v", ok, err)
	}
	for _, log := range mock.finding.Logs {
		if strings.Contains(log.Text, "hunter2") || strings.Contains(log.Text, "secret@example") {
			t.Fatalf("sensitive log reached AI prompt: %s", log.Text)
		}
	}
	for _, ev := range mock.finding.Evidence {
		if strings.Contains(ev.Value, "abcdefghijklmnopqrstuvwxyz") {
			t.Fatalf("bearer token reached AI prompt: %s", ev.Value)
		}
	}
}

func TestValidateRevisedPatchRejectsUnsafeChanges(t *testing.T) {
	tests := map[string]string{
		"metadata label": `metadata:
  labels:
    app: changed
spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
`,
		"annotation": `metadata:
  annotations:
    team: sre
spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
`,
		"template label": `spec:
  template:
    metadata:
      labels:
        app: changed
    spec:
      containers:
      - name: app
        image: repo/app:v2
`,
		"template annotation": `spec:
  template:
    metadata:
      annotations:
        checksum/config: changed
    spec:
      containers:
      - name: app
        image: repo/app:v2
`,
		"name namespace": `metadata:
  name: other
  namespace: prod
spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
`,
		"service selector": `apiVersion: v1
kind: Service
spec:
  selector:
    app: backend
`,
		"privileged": `spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
        securityContext:
          privileged: true
`,
		"hostpath": `spec:
  template:
    spec:
      volumes:
      - name: host
        hostPath:
          path: /var/run
      containers:
      - name: app
        image: repo/app:v2
`,
		"arbitrary volume": `spec:
  template:
    spec:
      volumes:
      - name: config
        configMap:
          name: app-config
      containers:
      - name: app
        image: repo/app:v2
`,
		"service account": `spec:
  template:
    spec:
      serviceAccountName: privileged
      containers:
      - name: app
        image: repo/app:v2
`,
		"node selector": `spec:
  template:
    spec:
      nodeSelector:
        pool: prod
      containers:
      - name: app
        image: repo/app:v2
`,
		"tolerations": `spec:
  template:
    spec:
      tolerations:
      - operator: Exists
      containers:
      - name: app
        image: repo/app:v2
`,
		"affinity": `spec:
  template:
    spec:
      affinity:
        nodeAffinity: {}
      containers:
      - name: app
        image: repo/app:v2
`,
		"host network": `spec:
  template:
    spec:
      hostNetwork: true
      containers:
      - name: app
        image: repo/app:v2
`,
		"host pid": `spec:
  template:
    spec:
      hostPID: true
      containers:
      - name: app
        image: repo/app:v2
`,
		"host ipc": `spec:
  template:
    spec:
      hostIPC: true
      containers:
      - name: app
        image: repo/app:v2
`,
		"sidecar injection": `spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
      - name: debug
        image: busybox
`,
		"command override": `spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
        command: ["sh"]
`,
		"args override": `spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
        args: ["-c", "sleep 3600"]
`,
		"lifecycle hook": `spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
        lifecycle:
          preStop:
            exec:
              command: ["sh"]
`,
		"capabilities": `spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
        securityContext:
          capabilities:
            add: ["SYS_ADMIN"]
`,
		"owner refs": `metadata:
  ownerReferences:
  - kind: Deployment
    name: owner
spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
`,
		"unknown kind": `apiVersion: example.com/v1
kind: Widget
spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
`,
		"multi doc":    originalImagePatch + "\n---\nkind: Service\n",
		"invalid yaml": "spec:\n  template:\n    spec:\n      containers:\n      - name",
	}
	for name, patch := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRevisedPatch(originalImagePatch, patch, "image"); err == nil {
				t.Fatal("expected validation rejection")
			}
		})
	}
}

func TestValidateRevisedPatchRejectsUnknownStrategy(t *testing.T) {
	err := ValidateRevisedPatch(originalImagePatch, strings.Replace(originalImagePatch, "v1", "v2", 1), "runtime")
	if err == nil {
		t.Fatal("expected unknown strategy rejection")
	}
}

func TestValidateRevisedPatchAllowsAIConcreteNameFromTODOOriginal(t *testing.T) {
	original := `spec:
  template:
    spec:
      containers:
      - name: TODO_CONTAINER_NAME
        image: TODO_PINNED_MULTI_ARCH_IMAGE
`
	revised := `spec:
  template:
    spec:
      containers:
      - name: api
        image: repo/api:v2
`
	if err := ValidateRevisedPatch(original, revised, "fix-architecture"); err != nil {
		t.Fatalf("expected AI concrete image patch to validate against TODO template: %v", err)
	}
}

func TestRevisePatchIgnoresUnstructuredResult(t *testing.T) {
	// An Unstructured AI result must be ignored even when it carries a
	// syntactically valid PatchYAML; the deterministic fallback (original
	// patch) wins.
	mock := &mockAIResult{result: &analyzer.AIResult{
		Unstructured: true,
		PatchYAML: `spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
`,
	}}
	revised, ok, err := revisePatch(context.Background(), mock, originalImagePatch, "image", Attempt{ExitReason: "retry"}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("unstructured AI result must not produce a usable revision, got: %s", revised)
	}
	if revised != originalImagePatch {
		t.Fatalf("expected original patch to remain in effect, got:\n%s", revised)
	}
}

func TestValidateRevisedPatchRejectsAppendedContainerAgainstTODOOriginal(t *testing.T) {
	// Regression: a TODO_ placeholder original leaves originalNames empty, which
	// previously disabled the membership check and let a revision append extra
	// containers. The count guard must now reject that.
	original := `spec:
  template:
    spec:
      containers:
      - name: TODO_CONTAINER_NAME
        image: TODO_PINNED_MULTI_ARCH_IMAGE
`
	revised := `spec:
  template:
    spec:
      containers:
      - name: api
        image: repo/api:v2
      - name: sidecar
        image: busybox
`
	if err := ValidateRevisedPatch(original, revised, "fix-architecture"); err == nil {
		t.Fatal("expected appended-container rejection against TODO original")
	}
}

func TestValidateReviewedPatchRejectsIdentityDrift(t *testing.T) {
	original := `apiVersion: v1
kind: Pod
metadata:
  name: api
  namespace: prod
spec:
  containers:
  - name: api
    image: repo/api:v1
`
	reviewed := strings.Replace(original, "apiVersion: v1", "apiVersion: v0", 1)
	err := ValidateReviewedPatch(original, reviewed, "image")
	if err == nil || !strings.Contains(err.Error(), "apiVersion must match") {
		t.Fatalf("expected apiVersion drift rejection, got %v", err)
	}
}

func TestValidateReviewedPatchAllowsConcreteImageEdit(t *testing.T) {
	original := `apiVersion: v1
kind: Pod
metadata:
  name: api
  namespace: prod
spec:
  containers:
  - name: api
    image: repo/api:v1
`
	reviewed := strings.Replace(original, "repo/api:v1", "repo/api@sha256:abc", 1)
	if err := ValidateReviewedPatch(original, reviewed, "image"); err != nil {
		t.Fatalf("expected reviewed image change to be accepted: %v", err)
	}
}

func TestRevisePatchRejectsMaliciousAIAndPreventsApply(t *testing.T) {
	mock := &mockAI{patch: `spec:
  template:
    spec:
      containers:
      - name: app
        image: repo/app:v2
      - name: debug
        image: busybox
`}
	revised, ok, err := revisePatch(context.Background(), mock, originalImagePatch, "image", Attempt{ExitReason: "retry"}, true)
	if err == nil {
		t.Fatal("expected malicious revised patch rejection")
	}
	if ok {
		t.Fatalf("malicious patch must not be marked usable: %s", revised)
	}
	if revised != originalImagePatch {
		t.Fatalf("expected original patch to remain in effect, got:\n%s", revised)
	}
}
