package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type cohereAdapter struct{ httpClient *http.Client }

func NewCohereAdapter(client *http.Client) Adapter {
	return cohereAdapter{httpClient: defaultHTTPClient(client)}
}

func (a cohereAdapter) Explain(ctx context.Context, request Request) (*analyzer.AIResult, error) {
	if request.Provider.Protocol != ProtocolCohere {
		return nil, &ProviderError{Kind: ErrorUnsupported}
	}
	endpoint, err := providerEndpoint(request.BaseURL, "/v2/chat")
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{"model": request.Model, "stream": false, "messages": []map[string]string{{"role": "system", "content": request.SystemPrompt}, {"role": "user", "content": request.UserPrompt}}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+request.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := readProviderResponse(resp)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &decoded) != nil {
		return nil, &ProviderError{Kind: ErrorContract}
	}
	if len(decoded.Error) > 0 && string(decoded.Error) != "null" {
		return nil, providerErrorFromResponse(http.StatusBadRequest, 0, data)
	}
	if strings.EqualFold(decoded.FinishReason, "MAX_TOKENS") {
		return nil, &ProviderError{Kind: ErrorIncomplete}
	}
	var answer strings.Builder
	for _, block := range decoded.Message.Content {
		if block.Type == "text" {
			answer.WriteString(block.Text)
		}
	}
	return ValidateAIResult([]byte(answer.String()))
}

type huggingFaceAdapter struct{ httpClient *http.Client }

func NewHuggingFaceAdapter(client *http.Client) Adapter {
	return huggingFaceAdapter{httpClient: defaultHTTPClient(client)}
}

func (a huggingFaceAdapter) Explain(ctx context.Context, request Request) (*analyzer.AIResult, error) {
	if request.Provider.Protocol != ProtocolHuggingFace {
		return nil, &ProviderError{Kind: ErrorUnsupported}
	}
	return NewOpenAIChatAdapter(a.httpClient).Explain(ctx, request)
}
