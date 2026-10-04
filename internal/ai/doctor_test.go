package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDoctorReportsProtocolAndSanitizesEndpoint(t *testing.T) {
	t.Setenv("FIXORA_AI_PROVIDER", "openai")
	t.Setenv("FIXORA_AI_API_KEY", "secret-key")
	t.Setenv("FIXORA_AI_BASE_URL", "https://user:pass@example.test/v1?token=query-secret")
	t.Setenv("FIXORA_AI_PROTOCOL", string(ProtocolOpenAIResponses))
	result := Doctor(context.Background())
	encoded, _ := json.Marshal(result)
	if result.Protocol != string(ProtocolOpenAIResponses) || result.BaseURL != "https://example.test" {
		t.Fatalf("doctor result = %#v", result)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "user") || strings.Contains(string(encoded), "pass") {
		t.Fatalf("doctor output leaked endpoint credentials: %s", encoded)
	}
}
