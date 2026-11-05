package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
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

func TestSupportedProviderDocsMatchRegistry(t *testing.T) {
	docs, err := os.ReadFile("../../docs/guides/ai.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range SupportedProviderIDs() {
		if !strings.Contains(string(docs), "`"+id+"`") {
			t.Errorf("supported provider %q is missing from AI guide", id)
		}
	}
}

func TestUnknownProviderFailsBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	client := Client{Provider: "openaai", BaseURL: server.URL, APIKey: "test", Model: "test", HTTP: server.Client()}
	_, err := client.Explain(context.Background(), analyzer.Finding{Summary: "finding"})
	if err == nil || calls != 0 {
		t.Fatalf("unknown provider request count=%d error=%v", calls, err)
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
