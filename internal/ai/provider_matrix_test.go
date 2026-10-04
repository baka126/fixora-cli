package ai

import (
	"reflect"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/config"
)

func TestResolveProviderConfigurationMatrix(t *testing.T) {
	cases := []struct {
		id       string
		protocol Protocol
		auth     string
	}{
		{"openai", ProtocolOpenAIChat, "bearer"},
		{"ollama", ProtocolOllama, "none"},
		{"anthropic", ProtocolAnthropic, "api-key"},
		{"gemini", ProtocolGemini, "google-api-key"},
		{"google", ProtocolGemini, "google-api-key"},
		{"groq", ProtocolOpenAIChat, "bearer"},
		{"localai", ProtocolOpenAIChat, "optional-bearer"},
		{"azureopenai", ProtocolAzureOpenAI, "azure-key"},
		{"customrest", ProtocolOpenAIChat, "optional-bearer"},
		{"cohere", ProtocolCohere, "bearer"},
		{"huggingface", ProtocolHuggingFace, "bearer"},
		{"googlevertexai", ProtocolVertex, "google-credentials"},
		{"amazonbedrock", ProtocolBedrock, "aws-credentials"},
		{"amazonbedrockconverse", ProtocolBedrockConverse, "aws-credentials"},
		{"amazonsagemaker", ProtocolSageMaker, "aws-credentials"},
		{"oci", ProtocolOCI, "oci-credentials"},
		{"watsonxai", ProtocolWatsonx, "bearer"},
		{"ibmwatsonxai", ProtocolWatsonx, "bearer"},
		{"noop", ProtocolNoop, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			spec, err := ResolveProvider(tc.id)
			if err != nil {
				t.Fatalf("ResolveProvider(%q): %v", tc.id, err)
			}
			if spec.Protocol != tc.protocol || spec.AuthMode != tc.auth {
				t.Fatalf("spec = %#v, want protocol %q auth %q", spec, tc.protocol, tc.auth)
			}
			if spec.ConfiguredID != tc.id {
				t.Fatalf("configured ID = %q, want %q", spec.ConfiguredID, tc.id)
			}
		})
	}
}

func TestResolveProviderRejectsUnknown(t *testing.T) {
	if _, err := ResolveProvider("not-a-provider"); err == nil {
		t.Fatal("expected unknown provider to be rejected")
	}
}

func TestConfigValidationProviderIDsMatchMatrix(t *testing.T) {
	if got, want := config.AIProviders(), SupportedProviderIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("config provider IDs = %v, AI matrix = %v", got, want)
	}
	for _, id := range SupportedProviderIDs() {
		cfg := config.Default()
		cfg.AIProvider = id
		if result := config.Validate(cfg); !result.Valid {
			t.Errorf("config validation rejects matrix provider %q: %v", id, result.Errors)
		}
	}
}
