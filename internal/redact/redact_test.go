package redact

import (
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
