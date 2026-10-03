package shadow

import "testing"

func TestValidateCandidatePatchRejectsInjectedFields(t *testing.T) {
	good := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    spec:
      containers:
      - name: api
        image: ghcr.io/acme/api:v2
`
	if err := ValidateCandidatePatch(good, "image", "Deployment/api", "prod"); err != nil {
		t.Fatalf("good candidate rejected: %v", err)
	}
	injected := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    spec:
      containers:
      - name: api
        image: ghcr.io/acme/api:v2
        securityContext:
          privileged: true
`
	if err := ValidateCandidatePatch(injected, "image", "Deployment/api", "prod"); err == nil {
		t.Fatal("privileged injection accepted")
	}
	private := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    spec:
      containers:
      - name: api
        image: untrusted.invalid/acme/api:v2
`
	if err := ValidateCandidatePatch(private, "image", "Deployment/api", "prod"); err == nil {
		t.Fatal("unapproved image registry accepted")
	}
}

func TestValidateCandidatePatchRejectsSecretEnvironmentAndExtraResources(t *testing.T) {
	secretEnv := `apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: prod}
spec:
  template:
    spec:
      containers:
      - name: api
        env:
        - name: TOKEN
          valueFrom:
            secretKeyRef: {name: db, key: token}
`
	if err := ValidateCandidatePatch(secretEnv, "env", "Deployment/api", "prod"); err == nil {
		t.Fatal("Secret env ref accepted")
	}
	extraResource := `apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: prod}
spec:
  template:
    spec:
      containers:
      - name: api
        resources:
          limits:
            nvidia.com/gpu: "8"
`
	if err := ValidateCandidatePatch(extraResource, "resources", "Deployment/api", "prod"); err == nil {
		t.Fatal("unexpected resource dimension accepted")
	}
}

func TestValidateCandidatePatchRequiresStrategyField(t *testing.T) {
	noImage := `apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: prod}
spec:
  template:
    spec:
      containers:
      - name: api
`
	if err := ValidateCandidatePatch(noImage, "image", "Deployment/api", "prod"); err == nil {
		t.Fatal("image strategy without image accepted")
	}
}

func TestValidateCandidatePatchRejectsUnexpectedShapeAndEmptyResourceChange(t *testing.T) {
	for _, patch := range []string{
		`apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: prod}
status: {replicas: 9}
spec:
  template:
    spec:
      containers: [{name: api, image: ghcr.io/acme/api:v2}]
`,
		`apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: prod}
spec:
  replicas: 9
  template:
    spec:
      containers: [{name: api, image: ghcr.io/acme/api:v2}]
`,
	} {
		if err := ValidateCandidatePatch(patch, "image", "Deployment/api", "prod"); err == nil {
			t.Fatal("unexpected patch field accepted")
		}
	}
	empty := `apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: prod}
spec:
  template:
    spec:
      containers:
      - name: api
        resources:
          limits: {}
`
	if err := ValidateCandidatePatch(empty, "resources", "Deployment/api", "prod"); err == nil {
		t.Fatal("empty resource change accepted")
	}
}
