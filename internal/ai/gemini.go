package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type geminiAdapter struct{ httpClient *http.Client }

func NewGeminiAdapter(httpClient *http.Client) Adapter {
	return geminiAdapter{httpClient: defaultHTTPClient(httpClient)}
}

func (a geminiAdapter) Explain(ctx context.Context, request Request) (*analyzer.AIResult, error) {
	if request.Provider.Protocol != ProtocolGemini {
		return nil, &ProviderError{Kind: ErrorUnsupported}
	}
	endpoint, err := providerEndpoint(request.BaseURL, "")
	if err != nil {
		return nil, err
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/models/" + url.PathEscape(request.Model) + ":generateContent"
	query := endpoint.Query()
	if strings.TrimSpace(request.APIKey) != "" {
		// Google accepts the API key in this header; keeping it out of the URL
		// avoids leaking credentials through request logs and diagnostics.
	}
	endpoint.RawQuery = query.Encode()
	body := map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]string{{"text": request.SystemPrompt}}},
		"contents":          []map[string]any{{"role": "user", "parts": []map[string]string{{"text": request.UserPrompt}}}},
		"generationConfig":  map[string]any{"responseMimeType": "application/json"},
	}
	if hasOutputMode(request.Provider, OutputModeJSONSchema) {
		schema := request.Contract.Schema
		if schema == nil {
			schema = remediationSchema()
		}
		body["generationConfig"].(map[string]any)["responseJsonSchema"] = schema
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
	if strings.TrimSpace(request.APIKey) != "" {
		req.Header.Set("x-goog-api-key", request.APIKey)
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
		Error      json.RawMessage `json:"error"`
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return nil, &ProviderError{Kind: ErrorContract}
	}
	if len(decoded.Error) > 0 && string(decoded.Error) != "null" {
		return nil, providerErrorFromResponse(http.StatusBadRequest, 0, responseBody)
	}
	if len(decoded.Candidates) == 0 {
		return nil, &ProviderError{Kind: ErrorIncomplete}
	}
	candidate := decoded.Candidates[0]
	switch candidate.FinishReason {
	case "STOP", "FINISH_REASON_UNSPECIFIED", "":
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return nil, &ProviderError{Kind: ErrorRefusal}
	case "MAX_TOKENS":
		return nil, &ProviderError{Kind: ErrorIncomplete}
	default:
		return nil, &ProviderError{Kind: ErrorIncomplete}
	}
	var answer strings.Builder
	for _, part := range candidate.Content.Parts {
		if !part.Thought {
			answer.WriteString(part.Text)
		}
	}
	if answer.Len() == 0 {
		return nil, fmt.Errorf("%w", &ProviderError{Kind: ErrorIncomplete})
	}
	return ValidateAIResult([]byte(answer.String()))
}
