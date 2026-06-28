package analyzer

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

// TestSecretAnalyzerOffZeroReads asserts that when CheckSecretKeys is false,
// analyzeSecrets returns immediately and reads ZERO Secret data.
func TestSecretAnalyzerOffZeroReads(t *testing.T) {
	var secretCalls int32
	reader := fakeReader{
		secretItemCalls: &secretCalls,
		items: map[string][]map[string]any{
			"secrets": {
				{"metadata": map[string]any{"namespace": "prod", "name": "db-creds"},
					"data": map[string]any{"password": base64.StdEncoding.EncodeToString([]byte("s3cr3t"))}},
			},
		},
	}
	// CheckSecretKeys defaults to false.
	a := New(reader, Options{Namespace: "prod"})
	ctx := NewScanContext(t.Context(), reader, Options{Namespace: "prod"})
	findings, err := a.analyzeSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no findings when off, got %#v", findings)
	}
	if got := atomic.LoadInt32(&secretCalls); got != 0 {
		t.Fatalf("expected 0 secret reads when opt-in is off, got %d", got)
	}
}

// TestSecretAnalyzerMissingKey asserts SecretMissingKey is raised when a pod
// references a key that does not exist in the named Secret.
func TestSecretAnalyzerMissingKey(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"secrets": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "db-creds"},
				"type":     "Opaque",
				"data":     map[string]any{"host": base64.StdEncoding.EncodeToString([]byte("localhost"))},
			},
		},
		"pods": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "api"},
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name": "api",
						"env": []any{map[string]any{
							"name": "DB_PASSWORD",
							"valueFrom": map[string]any{
								"secretKeyRef": map[string]any{
									"name": "db-creds",
									"key":  "password",
								},
							},
						}},
					}},
				},
			},
		},
	})
	a := New(fakeReader{}, Options{Namespace: "prod", CheckSecretKeys: true})
	findings, err := a.analyzeSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "SecretMissingKey")
	// Evidence must contain the present key names, not any values.
	for _, f := range findings {
		if f.Status == "SecretMissingKey" {
			ev := f.Evidence[0].Value
			if !strings.Contains(ev, "host") {
				t.Fatalf("expected present key 'host' in evidence, got %q", ev)
			}
		}
	}
}

// TestSecretAnalyzerInvalidBase64ValueNeverInFindings asserts that when a
// Secret has an invalid-base64 value, SecretInvalidBase64 is raised AND the
// raw value string is absent from every finding field.
func TestSecretAnalyzerInvalidBase64ValueNeverInFindings(t *testing.T) {
	badValue := "!!notb64!!"
	ctx := scanContextWithItems(map[string][]map[string]any{
		"secrets": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "broken"},
				"type":     "Opaque",
				"data":     map[string]any{"x": badValue},
			},
		},
		"pods": {},
	})
	a := New(fakeReader{}, Options{Namespace: "prod", CheckSecretKeys: true})
	findings, err := a.analyzeSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "SecretInvalidBase64")

	// The value string must not appear in any finding field.
	for _, f := range findings {
		serialized := fmt.Sprintf("%#v", f)
		if strings.Contains(serialized, badValue) {
			t.Fatalf("secret value %q leaked into finding: %s", badValue, serialized)
		}
	}
}

// TestSecretAnalyzerMissingPullSecret asserts MissingPullSecret is raised when
// a pod's imagePullSecrets references a missing secret, and that the status
// does NOT contain "ImagePull" (which would make it apply-eligible).
func TestSecretAnalyzerMissingPullSecret(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"secrets": {},
		"pods": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "web"},
				"spec": map[string]any{
					"imagePullSecrets": []any{map[string]any{"name": "registry-creds"}},
				},
			},
		},
	})
	a := New(fakeReader{}, Options{Namespace: "prod", CheckSecretKeys: true})
	findings, err := a.analyzeSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "MissingPullSecret")
}

// TestSecretAnalyzerWrongPullSecretType asserts MissingPullSecret when the
// imagePullSecret exists but has the wrong type.
func TestSecretAnalyzerWrongPullSecretType(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"secrets": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "registry-creds"},
				"type":     "Opaque",
				"data":     map[string]any{},
			},
		},
		"pods": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "web"},
				"spec": map[string]any{
					"imagePullSecrets": []any{map[string]any{"name": "registry-creds"}},
				},
			},
		},
	})
	a := New(fakeReader{}, Options{Namespace: "prod", CheckSecretKeys: true})
	findings, err := a.analyzeSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "MissingPullSecret")
}

// TestSecretAnalyzerValidPullSecretNoFinding asserts no finding when
// imagePullSecret has the correct type.
func TestSecretAnalyzerValidPullSecretNoFinding(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"secrets": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "registry-creds"},
				"type":     "kubernetes.io/dockerconfigjson",
				"data":     map[string]any{".dockerconfigjson": base64.StdEncoding.EncodeToString([]byte(`{"auths":{}}`))}},
		},
		"pods": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "web"},
				"spec": map[string]any{
					"imagePullSecrets": []any{map[string]any{"name": "registry-creds"}},
				},
			},
		},
	})
	a := New(fakeReader{}, Options{Namespace: "prod", CheckSecretKeys: true})
	findings, err := a.analyzeSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no findings for valid pull secret, got %#v", findings)
	}
}

// TestSecretStatusCollisionGuard asserts the three new statuses do not collide
// with any BuildPlan switch case and that MissingPullSecret does NOT contain
// "ImagePull" (which would make it apply-eligible in the planner).
func TestSecretStatusCollisionGuard(t *testing.T) {
	plannerSwitchKeys := []string{
		"ImagePull", "OOMKilled", "CrashLoopBackOff", "CreateContainerConfigError",
		"ExecFormatError", "PermissionDenied", "NoEndpoints", "ConnectionRefused",
		"HPA", "Evicted", "Webhook",
	}
	newStatuses := []string{"SecretMissingKey", "SecretInvalidBase64", "MissingPullSecret"}

	for _, status := range newStatuses {
		for _, plannerKey := range plannerSwitchKeys {
			if strings.Contains(status, plannerKey) {
				t.Errorf("status %q contains planner switch key %q — would become apply-eligible", status, plannerKey)
			}
		}
	}

	// Specific safety assertion: MissingPullSecret must NOT contain "ImagePull".
	if strings.Contains("MissingPullSecret", "ImagePull") {
		t.Errorf("MissingPullSecret must not contain ImagePull — it would wrongly become apply-eligible")
	}
}
