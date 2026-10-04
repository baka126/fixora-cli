package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type openAIChatAdapter struct{ httpClient *http.Client }

func NewOpenAIChatAdapter(httpClient *http.Client) Adapter {
	return openAIChatAdapter{httpClient: defaultHTTPClient(httpClient)}
}

type openAIChatRequest struct {
	Model          string         `json:"model,omitempty"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
}

func (a openAIChatAdapter) Explain(ctx context.Context, request Request) (*analyzer.AIResult, error) {
	if request.Provider.Protocol != ProtocolOpenAIChat && request.Provider.Protocol != ProtocolAzureOpenAI && request.Provider.Protocol != ProtocolHuggingFace {
		return nil, &ProviderError{Kind: ErrorUnsupported}
	}
	endpoint, err := openAIEndpoint(request, "chat/completions")
	if err != nil {
		return nil, err
	}
	body := openAIChatRequest{
		Model: request.Model,
		Messages: []chatMessage{
			{Role: "system", Content: request.SystemPrompt},
			{Role: "user", Content: request.UserPrompt},
		},
		ResponseFormat: openAIResponseFormat(request.Provider, request.Contract),
	}
	if request.Provider.Protocol == ProtocolAzureOpenAI {
		body.Model = "" // Azure selects the model deployment from its URL path.
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if request.Provider.Protocol == ProtocolAzureOpenAI {
		req.Header.Set("api-key", request.APIKey)
	} else if request.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+request.APIKey)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	responseBody, err := readProviderResponse(resp)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return nil, &ProviderError{Kind: ErrorContract}
	}
	if len(decoded.Error) > 0 && string(decoded.Error) != "null" {
		return nil, providerErrorFromResponse(http.StatusBadRequest, 0, responseBody)
	}
	if len(decoded.Choices) == 0 {
		return nil, &ProviderError{Kind: ErrorContract}
	}
	choice := decoded.Choices[0]
	if choice.Message.Refusal != "" {
		return nil, &ProviderError{Kind: ErrorRefusal}
	}
	switch choice.FinishReason {
	case "length":
		return nil, &ProviderError{Kind: ErrorIncomplete}
	case "content_filter":
		return nil, &ProviderError{Kind: ErrorRefusal}
	}
	return ValidateAIResult([]byte(choice.Message.Content))
}

func openAIResponseFormat(provider ProviderSpec, contract OutputContract) map[string]any {
	if hasOutputMode(provider, OutputModeJSONSchema) {
		schema := contract.Schema
		if schema == nil {
			schema = remediationSchema()
		}
		return map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": "fixora_remediation", "strict": true, "schema": schema,
			},
		}
	}
	if hasOutputMode(provider, OutputModeJSON) {
		return map[string]any{"type": "json_object"}
	}
	return nil
}

func hasOutputMode(provider ProviderSpec, mode OutputMode) bool {
	for _, supported := range provider.OutputModes {
		if supported == mode {
			return true
		}
	}
	return false
}

func openAIEndpoint(request Request, endpoint string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(request.BaseURL), "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, &ProviderError{Kind: ErrorConfiguration}
	}
	if request.Provider.Protocol != ProtocolAzureOpenAI {
		if !strings.HasSuffix(strings.TrimRight(base.Path, "/"), "/"+endpoint) {
			base.Path = path.Join(base.Path, endpoint)
		}
		return base, nil
	}
	if endpoint != "chat/completions" {
		return nil, &ProviderError{Kind: ErrorUnsupported}
	}

	options := request.Provider.EndpointOptions
	deployment := strings.TrimSpace(options["deployment"])
	basePath := strings.TrimRight(base.Path, "/")
	if deployment != "" {
		if safeDetail(deployment) == "" {
			return nil, &ProviderError{Kind: ErrorConfiguration}
		}
		basePath = path.Join(basePath, "openai", "deployments", deployment)
	} else if !hasAzureDeploymentPath(basePath) {
		return nil, &ProviderError{Kind: ErrorConfiguration}
	}
	if !strings.HasSuffix(basePath, "/chat/completions") {
		basePath = path.Join(basePath, "chat/completions")
	}
	base.Path = basePath
	query := base.Query()
	apiVersion := strings.TrimSpace(options["api-version"])
	if apiVersion == "" {
		apiVersion = query.Get("api-version")
	}
	if apiVersion == "" {
		return nil, &ProviderError{Kind: ErrorConfiguration}
	}
	query.Set("api-version", apiVersion)
	base.RawQuery = query.Encode()
	return base, nil
}

func hasAzureDeploymentPath(value string) bool {
	segments := strings.Split(strings.Trim(value, "/"), "/")
	for i := 0; i+2 < len(segments); i++ {
		if segments[i] == "openai" && segments[i+1] == "deployments" && segments[i+2] != "" {
			return true
		}
	}
	return false
}

func defaultHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 60 * time.Second}
}
