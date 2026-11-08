package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type vertexAdapter struct {
	httpClient  *http.Client
	tokenSource oauth2.TokenSource
}

func NewVertexAdapter(client *http.Client, tokenSource oauth2.TokenSource) Adapter {
	if tokenSource == nil {
		tokenSource, _ = google.DefaultTokenSource(context.Background(), "https://www.googleapis.com/auth/cloud-platform")
	}
	return vertexAdapter{httpClient: defaultHTTPClient(client), tokenSource: tokenSource}
}

func (a vertexAdapter) Explain(ctx context.Context, request Request) (*analyzer.AIResult, error) {
	if request.Provider.Protocol != ProtocolVertex || a.tokenSource == nil {
		return nil, &ProviderError{Kind: ErrorConfiguration}
	}
	project, location := strings.TrimSpace(request.Provider.EndpointOptions["project"]), strings.TrimSpace(request.Provider.EndpointOptions["location"])
	if safeDetail(project) == "" || safeDetail(location) == "" {
		return nil, &ProviderError{Kind: ErrorConfiguration}
	}
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(request.BaseURL), "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, &ProviderError{Kind: ErrorConfiguration}
	}
	base.Path = path.Join(base.Path, "v1", "projects", project, "locations", location, "publishers", "google", "models", request.Model+":generateContent")
	token, err := a.tokenSource.Token()
	if err != nil || token == nil || strings.TrimSpace(token.AccessToken) == "" {
		return nil, &ProviderError{Kind: ErrorAuthentication}
	}
	body := map[string]any{"systemInstruction": map[string]any{"parts": []map[string]string{{"text": request.SystemPrompt}}}, "contents": []map[string]any{{"role": "user", "parts": []map[string]string{{"text": request.UserPrompt}}}}}
	generationConfig := map[string]any{}
	if hasOutputMode(request.Provider, OutputModeJSON) || hasOutputMode(request.Provider, OutputModeJSONSchema) {
		generationConfig["responseMimeType"] = "application/json"
	}
	if hasOutputMode(request.Provider, OutputModeJSONSchema) {
		schema := request.Contract.Schema
		if schema == nil {
			schema = remediationSchema()
		}
		generationConfig["responseJsonSchema"] = schema
	}
	if len(generationConfig) > 0 {
		body["generationConfig"] = generationConfig
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
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
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(responseBody, &decoded) != nil {
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
	case "STOP", "":
	case "MAX_TOKENS":
		return nil, &ProviderError{Kind: ErrorIncomplete}
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT":
		return nil, &ProviderError{Kind: ErrorRefusal}
	default:
		return nil, &ProviderError{Kind: ErrorIncomplete}
	}
	var answer strings.Builder
	for _, part := range candidate.Content.Parts {
		if !part.Thought {
			answer.WriteString(part.Text)
		}
	}
	return ValidateAIResult([]byte(answer.String()))
}
