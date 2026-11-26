package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type openAIResponsesStreamAdapter struct{ httpClient *http.Client }

func NewOpenAIResponsesStreamAdapter(client *http.Client) Adapter {
	return openAIResponsesStreamAdapter{httpClient: defaultHTTPClient(client)}
}

func (a openAIResponsesStreamAdapter) Explain(ctx context.Context, request Request) (*analyzer.AIResult, error) {
	content, err := a.Generate(ctx, request)
	if err != nil {
		return nil, err
	}
	return ValidateAIResult([]byte(content))
}

func (a openAIResponsesStreamAdapter) Generate(ctx context.Context, request Request) (string, error) {
	if request.Provider.ID != "chatgpt" || request.Provider.Protocol != ProtocolOpenAIResponses || request.TokenSource == nil {
		return "", &ProviderError{Kind: ErrorUnsupported}
	}
	token, err := request.TokenSource.Token()
	if err != nil {
		return "", err
	}
	endpoint, err := openAIEndpoint(request, "responses")
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"model":        request.Model,
		"instructions": request.SystemPrompt,
		"input":        []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": request.UserPrompt}}}},
		"store":        false,
		"stream":       true,
	}
	if hasOutputMode(request.Provider, OutputModeJSONSchema) {
		schema := request.Contract.Schema
		if schema == nil {
			schema = remediationSchema()
		}
		name := request.SchemaName
		if name == "" {
			name = "fixora_remediation"
		}
		body["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": name, "strict": true, "schema": schema}}
	} else if hasOutputMode(request.Provider, OutputModeJSON) {
		body["text"] = map[string]any{"format": map[string]any{"type": "json_object"}}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", readProviderError(resp)
	}
	if contentType := strings.ToLower(resp.Header.Get("Content-Type")); !strings.Contains(contentType, "text/event-stream") {
		return "", &ProviderError{Kind: ErrorContract}
	}
	return consumeResponsesStream(resp.Body)
}

func consumeResponsesStream(reader io.Reader) (string, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxAIResponseBytes)
	var total int
	var dataLines []string
	var output strings.Builder
	completed, failed, incomplete, refused := false, false, false, false
	var streamErr *ProviderError
	consume := func(raw string) {
		if raw == "" || raw == "[DONE]" {
			return
		}
		var event struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response struct {
				ID    string `json:"id"`
				Error *struct {
					Code string `json:"code"`
				} `json:"error"`
				Output []struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(raw), &event) != nil {
			streamErr = &ProviderError{Kind: ErrorContract}
			return
		}
		switch event.Type {
		case "response.output_text.delta":
			output.WriteString(event.Delta)
		case "response.refusal.delta":
			refused = true
		case "response.completed":
			completed = true
			if output.Len() == 0 {
				for _, item := range event.Response.Output {
					for _, part := range item.Content {
						if part.Type == "output_text" {
							output.WriteString(part.Text)
						} else if part.Type == "refusal" {
							refused = true
						}
					}
				}
			}
		case "response.failed":
			failed = true
			streamErr = &ProviderError{Kind: ErrorProvider, RequestID: safeDetail(event.Response.ID)}
			if event.Response.Error != nil {
				streamErr.Code = safeDetail(event.Response.Error.Code)
			}
		case "response.incomplete":
			incomplete = true
		case "error":
			failed = true
			streamErr = &ProviderError{Kind: ErrorProvider}
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > maxAIResponseBytes {
			return "", &ProviderError{Kind: ErrorResponseSize}
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}
		if line == "" && len(dataLines) > 0 {
			consume(strings.Join(dataLines, "\n"))
			dataLines = dataLines[:0]
			if streamErr != nil || completed || failed || incomplete {
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", &ProviderError{Kind: ErrorResponseSize}
	}
	if len(dataLines) > 0 && streamErr == nil && !completed && !failed && !incomplete {
		consume(strings.Join(dataLines, "\n"))
	}
	if streamErr != nil {
		return "", streamErr
	}
	if failed {
		return "", &ProviderError{Kind: ErrorProvider}
	}
	if incomplete {
		return "", &ProviderError{Kind: ErrorIncomplete}
	}
	if !completed {
		return "", &ProviderError{Kind: ErrorIncomplete}
	}
	if refused {
		return "", &ProviderError{Kind: ErrorRefusal}
	}
	return output.String(), nil
}

func generateOpenAIResponsesText(ctx context.Context, client *http.Client, request Request) (string, error) {
	adapter := openAIResponsesStreamAdapter{httpClient: defaultHTTPClient(client)}
	return adapter.Generate(ctx, request)
}

func readProviderError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProviderErrorBytes))
	providerErr := providerErrorFromResponse(resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()), body)
	if typed, ok := providerErr.(*ProviderError); ok {
		typed.RequestID = safeDetail(resp.Header.Get("x-request-id"))
		if typed.Code == "subscription_sharing_usage_limit_exceeded" {
			typed.Kind = ErrorProvider
		}
	}
	return providerErr
}
