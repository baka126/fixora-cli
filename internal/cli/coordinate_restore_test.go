package cli

import (
	"strings"
	"testing"
)

// capturedDeployment is representative of what `kubectl get deployment/x -o yaml`
// returns: the spec you want back, plus server-owned metadata that must not be
// replayed.
const capturedDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    deployment.kubernetes.io/revision: "3"
  creationTimestamp: "2026-08-31T15:00:00Z"
  generation: 7
  labels:
    app: api
  managedFields:
  - apiVersion: apps/v1
    manager: kubectl-client-side-apply
    operation: Update
  name: api
  namespace: prod
  resourceVersion: "48213"
  uid: 4f1c9d2e-0000-4000-8000-abcdef123456
spec:
  replicas: 2
  selector:
    matchLabels:
      app: api
  template:
    metadata:
      labels:
        app: api
    spec:
      containers:
      - image: ghcr.io/acme/api:v1.2.3
        name: api
status:
  availableReplicas: 2
  observedGeneration: 7
`

// TestSanitizeCapturedManifestStripsServerOwnedFields pins the fix for the
// coordinate rollback path.
//
// Capture stores raw `kubectl get -o yaml`, and Restore feeds those bytes back
// through apply. resourceVersion is the dangerous one: fixora's own apply bumps
// it, so by rollback time the captured value is stale and optimistic
// concurrency rejects the restore — the rollback silently fails exactly when it
// is needed. managedFields is rejected outright by server-side apply.
func TestSanitizeCapturedManifestStripsServerOwnedFields(t *testing.T) {
	out, err := sanitizeCapturedManifest([]byte(capturedDeployment))
	if err != nil {
		t.Fatalf("sanitize failed: %v", err)
	}
	got := string(out)

	for _, banned := range []string{
		"resourceVersion",
		"uid:",
		"managedFields",
		"creationTimestamp",
		"generation",
		"status:",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("captured manifest still carries %q, which apply must not replay:\n%s", banned, got)
		}
	}
}

// TestSanitizeCapturedManifestKeepsRestorableState is the other half: stripping
// too much would make the rollback restore the wrong thing, which is worse than
// not rolling back at all.
func TestSanitizeCapturedManifestKeepsRestorableState(t *testing.T) {
	out, err := sanitizeCapturedManifest([]byte(capturedDeployment))
	if err != nil {
		t.Fatalf("sanitize failed: %v", err)
	}
	got := string(out)

	for _, required := range []string{
		"apiVersion: apps/v1",
		"kind: Deployment",
		"name: api",
		"namespace: prod",
		"replicas: 2",
		"matchLabels",
		"ghcr.io/acme/api:v1.2.3",
		"deployment.kubernetes.io/revision",
	} {
		if !strings.Contains(got, required) {
			t.Errorf("sanitize dropped %q, so a rollback would not restore prior state:\n%s", required, got)
		}
	}
}

// TestSanitizeCapturedManifestRejectsGarbage keeps Restore from writing
// unparseable bytes to a file and handing them to apply.
func TestSanitizeCapturedManifestRejectsGarbage(t *testing.T) {
	if _, err := sanitizeCapturedManifest([]byte("\t\tnot: [valid: yaml")); err == nil {
		t.Fatal("unparseable capture must be an error, not silently restored")
	}
}

// TestSanitizeCapturedManifestEmptyCaptureIsAnError guards the case where the
// kubectl read succeeded but returned nothing: restoring an empty file would be
// a no-op that reports success.
func TestSanitizeCapturedManifestEmptyCaptureIsAnError(t *testing.T) {
	if _, err := sanitizeCapturedManifest([]byte("   \n")); err == nil {
		t.Fatal("empty capture must be an error; restoring it would be a silent no-op")
	}
}
