package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	provider := firstNonEmpty(os.Getenv("FIXORA_AI_PROVIDER"), cfg.AIProvider, "openai")
	key := strings.TrimSpace(os.Getenv("FIXORA_AI_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(cfg.AIAPIKey)
	}
	if key == "" && provider != "ollama" && provider != "noop" {
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
	return Client{
		Provider: strings.ToLower(provider),
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
	payloadWithProfile := config.ProfilePrompt(cfg.Profile) + "\n\nFinding:\n" + string(payload)
	switch c.Provider {
	case "ollama":
		return c.explainOllama(ctx, payloadWithProfile)
	case "anthropic":
		return c.explainAnthropic(ctx, payloadWithProfile)
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
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
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
		return nil, fmt.Errorf(decoded.Error.Message)
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
		return nil, fmt.Errorf(decoded.Error)
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
	default:
		return "gpt-4o-mini"
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
