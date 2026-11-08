package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type anthropicAdapter struct{ httpClient *http.Client }

func NewAnthropicAdapter(httpClient *http.Client) Adapter {
	return anthropicAdapter{httpClient: defaultHTTPClient(httpClient)}
}

func (a anthropicAdapter) Explain(ctx context.Context, request Request) (*analyzer.AIResult, error) {
	if request.Provider.Protocol != ProtocolAnthropic {
		return nil, &ProviderError{Kind: ErrorUnsupported}
	}
	endpoint, err := providerEndpoint(request.BaseURL, "/v1/messages")
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"model":      request.Model,
		"max_tokens": 2048,
		"system":     request.SystemPrompt,
		"messages":   []map[string]string{{"role": "user", "content": request.UserPrompt}},
	}
	if hasOutputMode(request.Provider, OutputModeJSONSchema) {
		schema := request.Contract.Schema
		if schema == nil {
			schema = remediationSchema()
		}
		body["output_config"] = map[string]any{"format": map[string]any{"type": "json_schema", "schema": schema}}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", request.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
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
		StopReason string          `json:"stop_reason"`
		Error      json.RawMessage `json:"error"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return nil, &ProviderError{Kind: ErrorContract}
	}
	if len(decoded.Error) > 0 && string(decoded.Error) != "null" {
		return nil, providerErrorFromResponse(http.StatusBadRequest, 0, responseBody)
	}
	switch decoded.StopReason {
	case "refusal":
		return nil, &ProviderError{Kind: ErrorRefusal}
	case "max_tokens", "model_context_window_exceeded":
		return nil, &ProviderError{Kind: ErrorIncomplete}
	case "end_turn", "stop_sequence":
	default:
		return nil, &ProviderError{Kind: ErrorIncomplete}
	}
	var answer strings.Builder
	for _, block := range decoded.Content {
		if block.Type == "text" {
			answer.WriteString(block.Text)
		}
	}
	return ValidateAIResult([]byte(answer.String()))
}

func providerEndpoint(baseURL, endpoint string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, &ProviderError{Kind: ErrorConfiguration}
	}
	if !strings.HasSuffix(strings.TrimRight(base.Path, "/"), endpoint) {
		base.Path = strings.TrimRight(base.Path, "/") + endpoint
	}
	return base, nil
}
