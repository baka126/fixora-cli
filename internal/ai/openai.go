package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/config"
)

type Client struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	HTTP     *http.Client
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func NewFromEnv() (Client, error) {
	cfg, _ := config.Load()
	provider := strings.ToLower(firstNonEmpty(os.Getenv("FIXORA_AI_PROVIDER"), cfg.AIProvider, "openai"))
	key := strings.TrimSpace(os.Getenv("FIXORA_AI_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(cfg.AIAPIKey)
	}
	if key == "" && !providerAllowsNoKey(provider) {
		return Client{}, fmt.Errorf("FIXORA_AI_API_KEY is not set")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(firstNonEmpty(os.Getenv("FIXORA_AI_BASE_URL"), cfg.AIBaseURL)), "/")
	model := strings.TrimSpace(firstNonEmpty(os.Getenv("FIXORA_AI_MODEL"), cfg.AIModel))
	if model == "" {
		model = defaultModel(provider)
	}
	if baseURL == "" {
		baseURL = defaultBaseURL(provider)
	}
	if (provider == "azureopenai" || provider == "customrest") && baseURL == "" {
		return Client{}, fmt.Errorf("FIXORA_AI_BASE_URL is required for %s", provider)
	}
	return Client{
		Provider: provider,
		BaseURL:  baseURL,
		APIKey:   key,
		Model:    model,
		HTTP:     &http.Client{Timeout: 45 * time.Second},
	}, nil
}

func (c Client) Explain(ctx context.Context, finding analyzer.Finding) (*analyzer.AIResult, error) {
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 45 * time.Second}
	}
	payload, err := json.MarshalIndent(finding, "", "  ")
	if err != nil {
		return nil, err
	}
	cfg, _ := config.Load()
	if profile := strings.TrimSpace(os.Getenv("FIXORA_AI_PROFILE")); profile != "" {
		cfg.Profile = profile
	}
	payloadWithProfile := config.ProfilePrompt(cfg.Profile) + "\n\nFinding:\n" + string(payload)
	switch c.Provider {
	case "ollama":
		return c.explainOllama(ctx, payloadWithProfile)
	case "anthropic":
		return c.explainAnthropic(ctx, payloadWithProfile)
	case "gemini", "google":
		return c.explainGemini(ctx, payloadWithProfile)
	case "azureopenai":
		return c.explainAzureOpenAI(ctx, payloadWithProfile)
	case "noop":
		return &analyzer.AIResult{Summary: finding.Summary, RootCause: "AI provider is set to noop.", RecommendedFix: "Use deterministic recommendations only."}, nil
	default:
		return c.explainOpenAI(ctx, payloadWithProfile)
	}
}

func (c Client) explainOpenAI(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	reqBody := chatRequest{
		Model:       c.Model,
		Temperature: 0.1,
		Messages: []chatMessage{
			{
				Role:    "system",
				Content: "You are Fixora CLI, a local Kubernetes SRE assistant. Be precise, safe, and do not invent secret values. Return only JSON with keys summary, rootCause, recommendedFix, commands, warnings.",
			},
			{
				Role:    "user",
				Content: "Analyze this redacted Kubernetes finding. Prefer GitOps-safe advice and explicit verification steps. Finding:\n" + payload,
			},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var decoded chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("%s", decoded.Error.Message)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("AI provider returned HTTP %d", resp.StatusCode)
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("AI provider returned no choices")
	}
	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	var result analyzer.AIResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		result = analyzer.AIResult{
			Summary:        "AI returned a non-JSON response.",
			RootCause:      content,
			RecommendedFix: "Review the response manually before acting.",
			Warnings:       []string{"Non-JSON AI response could not be structured."},
		}
	}
	return &result, nil
}

func (c Client) explainAzureOpenAI(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	reqBody := chatRequest{
		Model:       c.Model,
		Temperature: 0.1,
		Messages: []chatMessage{
			{Role: "system", Content: "You are Fixora CLI, a local Kubernetes SRE assistant. Return only JSON with keys summary, rootCause, recommendedFix, commands, warnings."},
			{Role: "user", Content: "Analyze this redacted Kubernetes finding:\n" + payload},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(c.BaseURL, "/")
	if !strings.Contains(url, "/chat/completions") {
		url += "/chat/completions"
	}
	if !strings.Contains(url, "api-version=") {
		separator := "?"
		if strings.Contains(url, "?") {
			separator = "&"
		}
		url += separator + "api-version=2024-02-15-preview"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("api-key", c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("AI provider returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded chatResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("%s", decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("AI provider returned no choices")
	}
	return parseAIContent(decoded.Choices[0].Message.Content), nil
}

func (c Client) explainGemini(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	body, err := json.Marshal(map[string]any{
		"systemInstruction": map[string]any{
			"parts": []map[string]string{{"text": "You are Fixora CLI, a local Kubernetes SRE assistant. Return only JSON with summary, rootCause, recommendedFix, commands, warnings."}},
		},
		"contents": []map[string]any{{
			"role":  "user",
			"parts": []map[string]string{{"text": "Analyze this redacted Kubernetes finding:\n" + payload}},
		}},
		"generationConfig": map[string]any{
			"temperature":      0.1,
			"responseMimeType": "application/json",
		},
	})
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/models/%s:generateContent", strings.TrimRight(c.BaseURL, "/"), c.Model)
	if c.APIKey != "" {
		endpoint += "?key=" + url.QueryEscape(c.APIKey)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("AI provider returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("%s", decoded.Error.Message)
	}
	if len(decoded.Candidates) == 0 || len(decoded.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("AI provider returned no content")
	}
	return parseAIContent(decoded.Candidates[0].Content.Parts[0].Text), nil
}

func (c Client) explainOllama(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	body, err := json.Marshal(map[string]any{
		"model":  c.Model,
		"stream": false,
		"messages": []chatMessage{
			{Role: "system", Content: "Return only JSON with summary, rootCause, recommendedFix, commands, warnings."},
			{Role: "user", Content: "Analyze this Kubernetes finding:\n" + payload},
		},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var decoded struct {
		Message chatMessage `json:"message"`
		Error   string      `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if decoded.Error != "" {
		return nil, fmt.Errorf("%s", decoded.Error)
	}
	return parseAIContent(decoded.Message.Content), nil
}

func (c Client) explainAnthropic(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	body, err := json.Marshal(map[string]any{
		"model":      c.Model,
		"max_tokens": 1200,
		"system":     "You are Fixora CLI. Return only JSON with summary, rootCause, recommendedFix, commands, warnings.",
		"messages": []map[string]string{
			{"role": "user", "content": "Analyze this redacted Kubernetes finding:\n" + payload},
		},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("AI provider returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if len(decoded.Content) == 0 {
		return nil, fmt.Errorf("AI provider returned no content")
	}
	return parseAIContent(decoded.Content[0].Text), nil
}

func parseAIContent(content string) *analyzer.AIResult {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	var result analyzer.AIResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return &analyzer.AIResult{
			Summary:        "AI returned a non-JSON response.",
			RootCause:      content,
			RecommendedFix: "Review the response manually before acting.",
			Warnings:       []string{"Non-JSON AI response could not be structured."},
		}
	}
	return &result
}

func defaultBaseURL(provider string) string {
	switch strings.ToLower(provider) {
	case "ollama":
		return "http://localhost:11434"
	case "anthropic":
		return "https://api.anthropic.com"
	case "gemini", "google":
		return "https://generativelanguage.googleapis.com/v1beta"
	case "groq":
		return "https://api.groq.com/openai/v1"
	case "localai":
		return "http://localhost:8080/v1"
	case "azureopenai", "customrest":
		return ""
	default:
		return "https://api.openai.com/v1"
	}
}

func defaultModel(provider string) string {
	switch strings.ToLower(provider) {
	case "ollama":
		return "llama3.1"
	case "anthropic":
		return "claude-3-5-sonnet-latest"
	case "gemini", "google":
		return "gemini-1.5-flash"
	case "groq":
		return "llama-3.1-70b-versatile"
	default:
		return "gpt-4o-mini"
	}
}

func providerAllowsNoKey(provider string) bool {
	switch strings.ToLower(provider) {
	case "ollama", "noop", "localai", "customrest":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
