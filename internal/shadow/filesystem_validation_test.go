package shadow

import (
	"strings"
	"testing"
)

const cachePatch = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    spec:
      securityContext:
        fsGroup: 1000
      volumes:
      - name: fixora-cache
        emptyDir: {}
      containers:
      - name: app
        volumeMounts:
        - name: fixora-cache
          mountPath: /var/cache
`

func TestFilesystemCandidateAllowsOnlyEphemeralCache(t *testing.T) {
	if err := ValidateCandidatePatch(cachePatch, "filesystem", "Deployment/api", "prod"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(cachePatch, "emptyDir: {}", "hostPath: {path: /}", 1),
		strings.Replace(cachePatch, "mountPath: /var/cache", "mountPath: /etc", 1),
		strings.Replace(cachePatch, "fsGroup: 1000", "fsGroup: 0", 1),
		strings.Replace(cachePatch, "fsGroup: 1000", "fsGroup: 1000\n        runAsUser: 0", 1),
		strings.Replace(cachePatch, "emptyDir: {}", "emptyDir: {medium: Memory}", 1),
		strings.Replace(cachePatch, "name: app", "name: app\n        command: [sh]", 1),
	} {
		if err := ValidateCandidatePatch(bad, "filesystem", "Deployment/api", "prod"); err == nil {
			t.Fatalf("unsafe candidate accepted: %s", bad)
		}
	}
}

func TestFilesystemCandidateAllowsWorkloadSpecificEphemeralMount(t *testing.T) {
	patch := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    spec:
      securityContext:
        fsGroup: 2001
      volumes:
      - name: app-runtime-cache
        emptyDir: {}
      containers:
      - name: web
        volumeMounts:
        - name: app-runtime-cache
          mountPath: /opt/nebula/cache
      - name: metrics
        volumeMounts:
        - name: app-runtime-cache
          mountPath: /var/cache/metrics
`
	if err := ValidateCandidatePatch(patch, "filesystem", "Deployment/api", "prod"); err != nil {
		t.Fatalf("workload-specific ephemeral mount should be eligible: %v", err)
	}
}

func TestFilesystemCandidateRejectsUnsafeDynamicMounts(t *testing.T) {
	for _, bad := range []string{
		strings.Replace(cachePatch, "name: fixora-cache", "name: app-runtime", 1), // mismatched mount reference
		strings.Replace(cachePatch, "mountPath: /var/cache", "mountPath: /", 1),
		strings.Replace(cachePatch, "mountPath: /var/cache", "mountPath: /etc", 1),
		strings.Replace(cachePatch, "emptyDir: {}", "hostPath: {path: /}", 1),
	} {
		if err := ValidateCandidatePatch(bad, "filesystem", "Deployment/api", "prod"); err == nil {
			t.Fatalf("unsafe filesystem candidate accepted: %s", bad)
		}
	}
}
