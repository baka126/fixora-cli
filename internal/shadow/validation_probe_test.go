package shadow

import "testing"

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
