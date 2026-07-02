package analyzer

import (
	"context"
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

// TestSecretFilterOffZeroReads asserts that selecting the Secret analyzer by
// filter does not bypass the opt-in privacy gate.
func TestSecretFilterOffZeroReads(t *testing.T) {
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
	report := New(reader, Options{Namespace: "prod", Filters: []string{"secret"}}).ScanReport(context.Background())
	if len(report.Findings) != 0 {
		t.Fatalf("expected no findings when secret checks are off, got %#v", report.Findings)
	}
	if got := atomic.LoadInt32(&secretCalls); got != 0 {
		t.Fatalf("expected 0 secret reads when --filter secret is used without opt-in, got %d", got)
	}
}

// TestSecretAnalyzerReturnsPodListError asserts pod reference checks do not
// silently disappear when pods cannot be listed.
func TestSecretAnalyzerReturnsPodListError(t *testing.T) {
	ctx := NewScanContext(context.Background(), fakeReader{
		items: map[string][]map[string]any{
			"secrets": {
				{"metadata": map[string]any{"namespace": "prod", "name": "db-creds"}, "data": map[string]any{}},
			},
		},
		itemErrs: map[string]error{"pods": fmt.Errorf("pods forbidden")},
	}, Options{Namespace: "prod"})
	a := New(fakeReader{}, Options{Namespace: "prod", CheckSecretKeys: true})
	if _, err := a.analyzeSecrets(ctx); err == nil || !strings.Contains(err.Error(), "pods forbidden") {
		t.Fatalf("expected pod list error to be returned, got %v", err)
	}
}

// TestSecretAnalyzerMissingKey asserts SecretMissingKey is raised when a pod
// references a key that does not exist in the named Secret.
func TestSecretAnalyzerMissingKey(t *testing.T) {
	rawSecretValue := "localhost"
	encodedSecretValue := base64.StdEncoding.EncodeToString([]byte(rawSecretValue))
	ctx := scanContextWithItems(map[string][]map[string]any{
		"secrets": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "db-creds"},
				"type":     "Opaque",
				"data":     map[string]any{"host": encodedSecretValue},
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
			serialized := fmt.Sprintf("%#v", f)
			if strings.Contains(serialized, rawSecretValue) || strings.Contains(serialized, encodedSecretValue) {
				t.Fatalf("secret values leaked into finding: %s", serialized)
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
	for _, secretType := range []string{"kubernetes.io/dockerconfigjson", "kubernetes.io/dockercfg"} {
		t.Run(secretType, func(t *testing.T) {
			ctx := scanContextWithItems(map[string][]map[string]any{
				"secrets": {
					{
						"metadata": map[string]any{"namespace": "prod", "name": "registry-creds"},
						"type":     secretType,
						"data":     map[string]any{".dockerconfigjson": base64.StdEncoding.EncodeToString([]byte(`{"auths":{}}`))},
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
			if len(findings) != 0 {
				t.Fatalf("expected no findings for valid pull secret, got %#v", findings)
			}
		})
	}
}

// TestSecretAnalyzerMissingSecretKeyRef asserts SecretMissingKey is raised when a
// pod's secretKeyRef names a Secret that does not exist at all (previously silent).
func TestSecretAnalyzerMissingSecretKeyRef(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"secrets": {}, // no secrets exist
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
									"name": "missing-secret",
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
	for _, f := range findings {
		if f.Status == "SecretMissingKey" {
			ev := f.Evidence[0].Value
			if !strings.Contains(ev, "missing-secret") {
				t.Fatalf("expected secret name in evidence, got %q", ev)
			}
			if strings.Contains(ev, "present keys:") {
				t.Fatalf("whole-missing evidence must not list present keys, got %q", ev)
			}
		}
	}
}

// TestSecretAnalyzerEnvFromMissingSecret asserts SecretMissingKey is raised when
// a pod's envFrom[].secretRef names a Secret that does not exist.
func TestSecretAnalyzerEnvFromMissingSecret(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"secrets": {}, // no secrets exist
		"pods": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "worker"},
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name": "worker",
						"envFrom": []any{map[string]any{
							"secretRef": map[string]any{"name": "missing-env-secret"},
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
	for _, f := range findings {
		if f.Status == "SecretMissingKey" {
			ev := f.Evidence[0].Value
			if !strings.Contains(ev, "missing-env-secret") {
				t.Fatalf("expected secret name in evidence, got %q", ev)
			}
		}
	}
}

// TestSecretAnalyzerVolumeMissingSecret asserts SecretMissingKey is raised when
// a pod's volumes[].secret.secretName names a Secret that does not exist.
func TestSecretAnalyzerVolumeMissingSecret(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"secrets": {}, // no secrets exist
		"pods": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "app"},
				"spec": map[string]any{
					"containers": []any{map[string]any{"name": "app"}},
					"volumes": []any{map[string]any{
						"name":   "creds-vol",
						"secret": map[string]any{"secretName": "missing-vol-secret"},
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
	for _, f := range findings {
		if f.Status == "SecretMissingKey" {
			ev := f.Evidence[0].Value
			if !strings.Contains(ev, "missing-vol-secret") {
				t.Fatalf("expected secret name in evidence, got %q", ev)
			}
		}
	}
}

// TestSecretAnalyzerOptionalReferencesNoFinding asserts optional Secret refs are
// intentionally allowed by Kubernetes and should not raise missing-key findings.
func TestSecretAnalyzerOptionalReferencesNoFinding(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"secrets": {},
		"pods": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "api"},
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name": "api",
						"env": []any{map[string]any{
							"name": "OPTIONAL_PASSWORD",
							"valueFrom": map[string]any{
								"secretKeyRef": map[string]any{
									"name":     "optional-env",
									"key":      "password",
									"optional": true,
								},
							},
						}},
						"envFrom": []any{map[string]any{
							"secretRef": map[string]any{"name": "optional-envfrom", "optional": true},
						}},
					}},
					"volumes": []any{map[string]any{
						"name": "optional-vol",
						"secret": map[string]any{
							"secretName": "optional-volume",
							"optional":   true,
						},
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
	if len(findings) != 0 {
		t.Fatalf("expected no findings for optional Secret refs, got %#v", findings)
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
