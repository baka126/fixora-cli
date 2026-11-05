package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type openAIResponsesAdapter struct{ httpClient *http.Client }

func NewOpenAIResponsesAdapter(httpClient *http.Client) Adapter {
	return openAIResponsesAdapter{httpClient: defaultHTTPClient(httpClient)}
}

type openAIResponsesRequest struct {
	Model        string         `json:"model"`
	Instructions string         `json:"instructions"`
	Input        string         `json:"input"`
	Text         map[string]any `json:"text,omitempty"`
}

func (a openAIResponsesAdapter) Explain(ctx context.Context, request Request) (*analyzer.AIResult, error) {
	if request.Provider.Protocol != ProtocolOpenAIResponses {
		return nil, &ProviderError{Kind: ErrorUnsupported}
	}
	endpoint, err := openAIEndpoint(request, "responses")
	if err != nil {
		return nil, err
	}
	body := openAIResponsesRequest{
		Model: request.Model, Instructions: request.SystemPrompt, Input: request.UserPrompt,
	}
	if hasOutputMode(request.Provider, OutputModeJSONSchema) {
		schema := request.Contract.Schema
		if schema == nil {
			schema = remediationSchema()
		}
		body.Text = map[string]any{"format": map[string]any{
			"type": "json_schema", "name": "fixora_remediation", "strict": true, "schema": schema,
		}}
	} else if hasOutputMode(request.Provider, OutputModeJSON) {
		body.Text = map[string]any{"format": map[string]any{"type": "json_object"}}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(data))
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
	responseBody, err := readProviderResponse(resp)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Status            string `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error  json.RawMessage `json:"error"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return nil, &ProviderError{Kind: ErrorContract}
	}
	if len(decoded.Error) > 0 && string(decoded.Error) != "null" {
		return nil, providerErrorFromResponse(http.StatusBadRequest, 0, responseBody)
	}
	if decoded.Status == "incomplete" || decoded.IncompleteDetails.Reason != "" {
		return nil, &ProviderError{Kind: ErrorIncomplete}
	}
	var content strings.Builder
	refused := false
	for _, item := range decoded.Output {
		for _, part := range item.Content {
			switch part.Type {
			case "refusal":
				refused = true
			case "output_text":
				content.WriteString(part.Text)
			}
		}
	}
	if refused {
		return nil, &ProviderError{Kind: ErrorRefusal}
	}
	return ValidateAIResult([]byte(content.String()))
}
