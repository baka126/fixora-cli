package ai

import (
	"fmt"
	"strings"
)

type Protocol string

const (
	ProtocolOpenAIChat      Protocol = "openai-chat-completions"
	ProtocolOpenAIResponses Protocol = "openai-responses"
	ProtocolOllama          Protocol = "ollama-chat"
	ProtocolAnthropic       Protocol = "anthropic-messages"
	ProtocolGemini          Protocol = "gemini-generate-content"
	ProtocolAzureOpenAI     Protocol = "azure-openai-chat-completions"
	ProtocolCohere          Protocol = "cohere-chat"
	ProtocolHuggingFace     Protocol = "huggingface-inference"
	ProtocolVertex          Protocol = "vertex-generate-content"
	ProtocolBedrock         Protocol = "bedrock-invoke"
	ProtocolBedrockConverse Protocol = "bedrock-converse"
	ProtocolSageMaker       Protocol = "sagemaker-invoke"
	ProtocolOCI             Protocol = "oci-generative-ai"
	ProtocolWatsonx         Protocol = "watsonx-chat"
	ProtocolNoop            Protocol = "noop"
)

type OutputMode string

const (
	OutputModePromptJSON OutputMode = "prompt-json"
	OutputModeJSON       OutputMode = "json"
	OutputModeJSONSchema OutputMode = "json-schema"
)

type ProviderSpec struct {
	ID              string
	ConfiguredID    string
	Protocol        Protocol
	DefaultBaseURL  string
	AuthMode        string
	OutputModes     []OutputMode
	EndpointOptions map[string]string
}

var providerSpecs = map[string]ProviderSpec{
	"openai":                {ID: "openai", Protocol: ProtocolOpenAIChat, DefaultBaseURL: "https://api.openai.com/v1", AuthMode: "bearer", OutputModes: []OutputMode{OutputModePromptJSON, OutputModeJSON, OutputModeJSONSchema}},
	"ollama":                {ID: "ollama", Protocol: ProtocolOllama, DefaultBaseURL: "http://localhost:11434", AuthMode: "none", OutputModes: []OutputMode{OutputModePromptJSON}},
	"anthropic":             {ID: "anthropic", Protocol: ProtocolAnthropic, DefaultBaseURL: "https://api.anthropic.com", AuthMode: "api-key", OutputModes: []OutputMode{OutputModePromptJSON}},
	"gemini":                {ID: "gemini", Protocol: ProtocolGemini, DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta", AuthMode: "google-api-key", OutputModes: []OutputMode{OutputModePromptJSON, OutputModeJSON, OutputModeJSONSchema}},
	"google":                {ID: "gemini", Protocol: ProtocolGemini, DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta", AuthMode: "google-api-key", OutputModes: []OutputMode{OutputModePromptJSON, OutputModeJSON, OutputModeJSONSchema}},
	"groq":                  {ID: "groq", Protocol: ProtocolOpenAIChat, DefaultBaseURL: "https://api.groq.com/openai/v1", AuthMode: "bearer", OutputModes: []OutputMode{OutputModePromptJSON, OutputModeJSON}},
	"localai":               {ID: "localai", Protocol: ProtocolOpenAIChat, DefaultBaseURL: "http://localhost:8080/v1", AuthMode: "optional-bearer", OutputModes: []OutputMode{OutputModePromptJSON, OutputModeJSON}},
	"azureopenai":           {ID: "azureopenai", Protocol: ProtocolAzureOpenAI, AuthMode: "azure-key", OutputModes: []OutputMode{OutputModePromptJSON}, EndpointOptions: map[string]string{"api-version": "2024-02-15-preview"}},
	"customrest":            {ID: "customrest", Protocol: ProtocolOpenAIChat, AuthMode: "optional-bearer", OutputModes: []OutputMode{OutputModePromptJSON, OutputModeJSON}},
	"cohere":                {ID: "cohere", Protocol: ProtocolCohere, DefaultBaseURL: "https://api.cohere.com", AuthMode: "bearer", OutputModes: []OutputMode{OutputModePromptJSON}},
	"huggingface":           {ID: "huggingface", Protocol: ProtocolHuggingFace, DefaultBaseURL: "https://api-inference.huggingface.co/models", AuthMode: "bearer", OutputModes: []OutputMode{OutputModePromptJSON}},
	"googlevertexai":        {ID: "googlevertexai", Protocol: ProtocolVertex, AuthMode: "google-credentials", OutputModes: []OutputMode{OutputModePromptJSON}},
	"amazonbedrock":         {ID: "amazonbedrock", Protocol: ProtocolBedrock, AuthMode: "aws-credentials", OutputModes: []OutputMode{OutputModePromptJSON}},
	"amazonbedrockconverse": {ID: "amazonbedrockconverse", Protocol: ProtocolBedrockConverse, AuthMode: "aws-credentials", OutputModes: []OutputMode{OutputModePromptJSON}},
	"amazonsagemaker":       {ID: "amazonsagemaker", Protocol: ProtocolSageMaker, AuthMode: "aws-credentials", OutputModes: []OutputMode{OutputModePromptJSON}},
	"oci":                   {ID: "oci", Protocol: ProtocolOCI, AuthMode: "oci-credentials", OutputModes: []OutputMode{OutputModePromptJSON}},
	"watsonxai":             {ID: "watsonxai", Protocol: ProtocolWatsonx, AuthMode: "bearer", OutputModes: []OutputMode{OutputModePromptJSON}},
	"ibmwatsonxai":          {ID: "watsonxai", Protocol: ProtocolWatsonx, AuthMode: "bearer", OutputModes: []OutputMode{OutputModePromptJSON}},
	"noop":                  {ID: "noop", Protocol: ProtocolNoop, AuthMode: "none"},
}

// SupportedProviderIDs returns configured provider IDs in stable display order.
func SupportedProviderIDs() []string {
	return []string{"openai", "ollama", "anthropic", "gemini", "google", "groq", "localai", "azureopenai", "customrest", "cohere", "huggingface", "googlevertexai", "amazonbedrock", "amazonbedrockconverse", "amazonsagemaker", "oci", "watsonxai", "ibmwatsonxai", "noop"}
}

func ResolveProvider(providerID string) (ProviderSpec, error) {
	configuredID := strings.ToLower(strings.TrimSpace(providerID))
	spec, ok := providerSpecs[configuredID]
	if !ok {
		return ProviderSpec{}, fmt.Errorf("unsupported AI provider %q; supported providers: %s", configuredID, strings.Join(SupportedProviderIDs(), ", "))
	}
	spec.ConfiguredID = configuredID
	if spec.EndpointOptions != nil {
		options := make(map[string]string, len(spec.EndpointOptions))
		for key, value := range spec.EndpointOptions {
			options[key] = value
		}
		spec.EndpointOptions = options
	}
	spec.OutputModes = append([]OutputMode(nil), spec.OutputModes...)
	return spec, nil
}
