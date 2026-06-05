package redact

import (
	"strings"
	"testing"
)

func TestTextRedaction(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "token key-value",
			input:    `{"token": "super-secret"}`,
			expected: `{"token": "[REDACTED]"}`,
		},
		{
			name:     "password key-value",
			input:    `password = 'my-password-123'`,
			expected: `password = '[REDACTED]'`,
		},
		{
			name:     "secret key-value",
			input:    `secret: "shh"`,
			expected: `secret: "[REDACTED]"`,
		},
		{
			name:     "api_key key-value",
			input:    `api_key: "abc123xyz"`,
			expected: `api_key: "[REDACTED]"`,
		},
		{
			name:     "authorization header",
			input:    `Authorization: "Basic YWRtaW46cGFzc3dvcmQ="`,
			expected: `Authorization: "[REDACTED]"`,
		},
		{
			name:     "jwt token",
			input:    `Bearer eyJhbGciOiJIUzI1NiIsInR5cCI.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c`,
			expected: `Bearer [REDACTED]`,
		},
		{
			name:     "bearer token",
			input:    `Authorization: Bearer 12345-abcde`,
			expected: `Authorization: [REDACTED]`,
		},
		{
			name:     "email",
			input:    `user@example.com`,
			expected: `[REDACTED]`,
		},
		{
			name:     "aws access key",
			input:    `AKIAIOSFODNN7EXAMPLE`,
			expected: `[REDACTED]`,
		},
		{
			name:     "ssh private key",
			input:    "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\n-----END RSA PRIVATE KEY-----",
			expected: `[REDACTED]`,
		},
		{
			name:     "url basic auth",
			input:    `https://user:password@example.com/api`,
			expected: `https://[REDACTED]@example.com/api`,
		},
		{
			name:     "no redaction needed",
			input:    `{"name": "test-pod", "status": "Running"}`,
			expected: `{"name": "test-pod", "status": "Running"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Text(tt.input)
			if got != tt.expected {
				t.Errorf("Text() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestKubernetesTextStructuredRedaction(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		forbidden []string
	}{
		{
			name: "secret data",
			input: `apiVersion: v1
kind: Secret
metadata:
  name: db
data:
  password: cGFzc3dvcmQ=
stringData:
  token: plain-token
`,
			forbidden: []string{"cGFzc3dvcmQ=", "plain-token"},
		},
		{
			name: "kubeconfig auth fields",
			input: `apiVersion: v1
kind: Config
users:
- name: admin
  user:
    token: token-value
    client-key-data: super-private-key
    client-certificate-data: super-private-cert
    certificate-authority-data: super-private-ca
    username: kube-user
    password: kube-password
    auth-provider:
      config:
        access-token: auth-provider-token
    exec:
      env:
      - name: PASSWORD
        value: exec-password
`,
			forbidden: []string{"token-value", "super-private-key", "super-private-cert", "super-private-ca", "kube-user", "kube-password", "auth-provider-token", "exec-password"},
		},
		{
			name: "pod env secret values",
			input: `apiVersion: v1
kind: Pod
spec:
  containers:
  - name: app
    env:
    - name: PASSWORD
      value: password-value
    - name: TOKEN
      value: token-value
    - name: SECRET
      value: secret-value
    - name: API_KEY
      value: api-key-value
    - name: DATABASE_URL
      value: postgres://user:pass@db/prod
    - name: REDIS_URL
      value: redis://:redis-pass@cache
    - name: MONGO_URL
      value: mongodb://user:mongo-pass@mongo/db
    - name: POSTGRES_URL
      value: postgresql://user:pg-pass@pg/db
    - name: NORMAL_NAME
      value: public
`,
			forbidden: []string{"password-value", "token-value", "secret-value", "api-key-value", "postgres://user:pass@db/prod", "redis-pass", "mongo-pass", "pg-pass"},
		},
		{
			name: "deployment template init and ephemeral env values",
			input: `apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - name: app
        env:
        - name: API_KEY
          value: abcdefghijklmnopqrstuvwxyz1234567890
      initContainers:
      - name: init
        env:
        - name: PRIVATE_KEY
          value: init-private-key
      ephemeralContainers:
      - name: debug
        env:
        - name: ACCESS_KEY
          value: ephemeral-access-key
`,
			forbidden: []string{"abcdefghijklmnopqrstuvwxyz1234567890", "init-private-key", "ephemeral-access-key"},
		},
		{
			name: "multi document yaml",
			input: `apiVersion: v1
kind: ConfigMap
metadata:
  name: safe
---
apiVersion: v1
kind: Secret
stringData:
  password: multi-doc-secret
`,
			forbidden: []string{"multi-doc-secret"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := KubernetesText(tt.input)
			for _, secret := range tt.forbidden {
				if strings.Contains(got, secret) {
					t.Fatalf("secret %q was not redacted:\n%s", secret, got)
				}
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("expected redaction marker, got:\n%s", got)
			}
		})
	}
}

func TestKubernetesTextRedactsConnectionStringsAndHighEntropy(t *testing.T) {
	input := strings.Join([]string{
		"postgres://user:pass@host/db",
		"mysql://user:pass@host/db",
		"mongodb://user:pass@host/db",
		"redis://:pass@host",
		"jdbc:postgresql://host/db?user=app&password=secret",
		"highentropy=AbCdEfGhIjKlMnOpQrStUvWxYz1234567890",
	}, "\n")
	got := KubernetesText(input)
	for _, forbidden := range []string{"user:pass", ":pass@host", "password=secret", "AbCdEfGhIjKlMnOpQrStUvWxYz1234567890"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("expected %q redacted, got:\n%s", forbidden, got)
		}
	}
}

func TestKubernetesTextFallsBackOnInvalidYAML(t *testing.T) {
	got := KubernetesText("not: [valid\npassword=hunter2\nBearer abcdefghijklmnop")
	for _, forbidden := range []string{"hunter2", "abcdefghijklmnop"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("fallback text redaction did not run for %q: %s", forbidden, got)
		}
	}
}
