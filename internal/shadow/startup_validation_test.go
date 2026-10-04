package shadow

import (
	"strings"
	"testing"
)

const startupPatch = `apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: prod}
spec:
  template:
    spec:
      containers:
      - name: app
        command: [httpd, -f, -p, "8080"]
        args: []
`

func TestStartupCandidateAndBaselineLock(t *testing.T) {
	if err := ValidateCandidatePatch(startupPatch, "restore-command", "Deployment/api", "prod"); err != nil {
		t.Fatal(err)
	}
	altered := strings.Replace(startupPatch, "httpd", "sh", 1)
	if err := ValidateRevisedPatch(startupPatch, altered, "restore-command"); err == nil {
		t.Fatal("AI cannot revise operator baseline")
	}
	if err := ValidateReviewedPatch(startupPatch, altered, "restore-command"); err == nil {
		t.Fatal("edited patch cannot diverge from explicit baseline")
	}
	if err := ValidateReviewedPatch(startupPatch, startupPatch, "restore-command"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCandidatePatch(strings.Replace(startupPatch, "args: []", "args: []\n        image: attacker", 1), "restore-command", "Deployment/api", "prod"); err == nil {
		t.Fatal("startup candidate must not change image")
	}
}
func TestEnvCandidateAllowsOnlyNullLiteralRemoval(t *testing.T) {
	patch := strings.Replace(startupPatch, "command: [httpd, -f, -p, \"8080\"]\n        args: []", `env:
        - name: DB_HOST
          value: null
          valueFrom:
            configMapKeyRef: {name: config, key: DB_HOST}`, 1)
	if err := ValidateCandidatePatch(patch, "env", "Deployment/api", "prod"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCandidatePatch(strings.Replace(patch, "value: null", "value: unsafe", 1), "env", "Deployment/api", "prod"); err == nil {
		t.Fatal("literal addition not allowed")
	}
}
