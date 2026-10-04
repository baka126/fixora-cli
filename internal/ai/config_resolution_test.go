package ai

import (
	"strings"
	"testing"
)

func TestNewFromEnvResolvesExplicitProtocolAndOutputMode(t *testing.T) {
	t.Setenv("FIXORA_AI_PROVIDER", "openai")
	t.Setenv("FIXORA_AI_API_KEY", "test-key")
	t.Setenv("FIXORA_AI_PROTOCOL", string(ProtocolOpenAIResponses))
	t.Setenv("FIXORA_AI_OUTPUT_MODE", string(OutputModeJSON))
	client, err := NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if client.Protocol != ProtocolOpenAIResponses || client.OutputMode != OutputModeJSON {
		t.Fatalf("resolved protocol/mode = %q/%q", client.Protocol, client.OutputMode)
	}
}

func TestUnsupportedProviderGivesMigrationHintBeforeCredentials(t *testing.T) {
	t.Setenv("FIXORA_AI_PROVIDER", "watsonxai")
	t.Setenv("FIXORA_AI_API_KEY", "")
	_, err := NewFromEnv()
	if err == nil || !strings.Contains(err.Error(), "OpenAI-compatible gateway") {
		t.Fatalf("expected migration hint, got %v", err)
	}
}

func TestNewFromEnvRejectsIncompatibleProtocolWithoutNetwork(t *testing.T) {
	t.Setenv("FIXORA_AI_PROVIDER", "anthropic")
	t.Setenv("FIXORA_AI_API_KEY", "test-key")
	t.Setenv("FIXORA_AI_PROTOCOL", string(ProtocolOpenAIResponses))
	if _, err := NewFromEnv(); err == nil {
		t.Fatal("expected incompatible protocol error")
	}
}

func TestNewFromEnvResolvesVertexADCWithoutAPIKey(t *testing.T) {
	t.Setenv("FIXORA_AI_PROVIDER", "googlevertexai")
	t.Setenv("FIXORA_AI_API_KEY", "")
	t.Setenv("FIXORA_AI_PROJECT", "project-a")
	t.Setenv("FIXORA_AI_LOCATION", "us-central1")
	t.Setenv("FIXORA_AI_MODEL", "gemini-test")
	t.Setenv("FIXORA_AI_BASE_URL", "https://us-central1-aiplatform.googleapis.com")
	client, err := NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if client.Spec.EndpointOptions["project"] != "project-a" || client.BaseURL != "https://us-central1-aiplatform.googleapis.com" {
		t.Fatalf("vertex selection = %#v, endpoint %q", client.Spec.EndpointOptions, client.BaseURL)
	}
}
