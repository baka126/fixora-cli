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
	if (provider == "azureopenai" || provider == "customrest" || provider == "watsonxai" || provider == "ibmwatsonxai" || provider == "googlevertexai" || provider == "amazonbedrock" || provider == "amazonbedrockconverse" || provider == "amazonsagemaker" || provider == "oci") && baseURL == "" {
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
	case "cohere":
		return c.explainCohere(ctx, payloadWithProfile)
	case "huggingface":
		return c.explainHuggingFace(ctx, payloadWithProfile)
	case "watsonxai", "ibmwatsonxai":
		return c.explainTextGeneration(ctx, payloadWithProfile, "Watsonx")
	case "googlevertexai", "amazonbedrock", "amazonbedrockconverse", "amazonsagemaker", "oci":
		return c.explainCloudGateway(ctx, payloadWithProfile)
	case "azureopenai":
		return c.explainAzureOpenAI(ctx, payloadWithProfile)
	case "noop":
		return &analyzer.AIResult{Summary: finding.Summary, RootCause: "AI provider is set to noop.", RecommendedFix: "Use deterministic recommendations only."}, nil
	default:
		return c.explainOpenAI(ctx, payloadWithProfile)
	}
}

func jsonContract() string {
	return "Return only JSON with keys summary, rootCause, recommendedFix, patchYAML, strategy, confidence, analyzers, commands, warnings. Use the provided logs, events, metrics, object status, and related analyzer findings. patchYAML must be empty unless you can produce a concrete single-document Kubernetes YAML patch with no TODO placeholders. For pod-template remediations, only propose containers/initContainers image, resources, env, or envFrom changes. For service, ingress, storage, configmap, node, policy, or controller issues, patchYAML may be a minimal review-only source patch. Never include Secret resources or secret values, metadata labels/annotations/ownerReferences, unsafe selectors unless the issue is explicitly a Service or route backend mismatch, serviceAccountName, nodeSelector, tolerations, affinity, host networking/PID/IPC, volumes, hostPath, privileged containers, or shell command overrides in patchYAML. Write rootCause and recommendedFix for an experienced SRE: precise, technical, and concise. Name the specific resource, container, and field involved. Use correct Kubernetes terminology and do not add business-impact framing or marketing language."
}

func (c Client) explainCohere(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	body, err := json.Marshal(map[string]any{
		"model":       c.Model,
		"temperature": 0.1,
		"messages": []map[string]string{
			{"role": "system", "content": jsonContract()},
			{"role": "user", "content": "Analyze this redacted Kubernetes finding:\n" + payload},
		},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v2/chat", bytes.NewReader(body))
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
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("AI provider returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if len(decoded.Message.Content) > 0 {
		return parseAIContent(decoded.Message.Content[0].Text)
	}
	if decoded.Text != "" {
		return parseAIContent(decoded.Text)
	}
	return nil, fmt.Errorf("AI provider returned no content")
}

func (c Client) explainHuggingFace(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	body, err := json.Marshal(map[string]any{
		"inputs": jsonContract() + "\nAnalyze this Kubernetes finding:\n" + payload,
		"parameters": map[string]any{
			"temperature":      0.1,
			"return_full_text": false,
			"max_new_tokens":   900,
		},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/"+c.Model, bytes.NewReader(body))
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
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("AI provider returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded []struct {
		GeneratedText string `json:"generated_text"`
	}
	if err := json.Unmarshal(data, &decoded); err == nil && len(decoded) > 0 {
		return parseAIContent(decoded[0].GeneratedText)
	}
	var single struct {
		GeneratedText string `json:"generated_text"`
	}
	if err := json.Unmarshal(data, &single); err == nil && single.GeneratedText != "" {
		return parseAIContent(single.GeneratedText)
	}
	return parseAIContent(string(data))
}

func (c Client) explainCloudGateway(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("%s requires FIXORA_AI_BASE_URL pointing at an authenticated internal gateway or cloud proxy", c.Provider)
	}
	return c.explainOpenAI(ctx, payload)
}

func (c Client) explainTextGeneration(ctx context.Context, payload, providerName string) (*analyzer.AIResult, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("%s requires FIXORA_AI_BASE_URL", providerName)
	}
	return c.explainOpenAI(ctx, payload)
}

func (c Client) explainOpenAI(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	reqBody := chatRequest{
		Model:       c.Model,
		Temperature: 0.1,
		Messages: []chatMessage{
			{
				Role:    "system",
				Content: "You are Fixora CLI, a local Kubernetes SRE assistant. Be precise, safe, and do not invent secret values. " + jsonContract(),
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
	return parseAIContent(decoded.Choices[0].Message.Content)
}

func (c Client) explainAzureOpenAI(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	reqBody := chatRequest{
		Model:       c.Model,
		Temperature: 0.1,
		Messages: []chatMessage{
			{Role: "system", Content: "You are Fixora CLI, a local Kubernetes SRE assistant. " + jsonContract()},
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
	return parseAIContent(decoded.Choices[0].Message.Content)
}

func (c Client) explainGemini(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	body, err := json.Marshal(map[string]any{
		"systemInstruction": map[string]any{
			"parts": []map[string]string{{"text": "You are Fixora CLI, a local Kubernetes SRE assistant. " + jsonContract()}},
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if c.APIKey != "" {
		req.Header.Set("x-goog-api-key", c.APIKey)
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
	return parseAIContent(decoded.Candidates[0].Content.Parts[0].Text)
}

func (c Client) explainOllama(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	body, err := json.Marshal(map[string]any{
		"model":  c.Model,
		"stream": false,
		"messages": []chatMessage{
			{Role: "system", Content: jsonContract()},
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
	return parseAIContent(decoded.Message.Content)
}

func (c Client) explainAnthropic(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	body, err := json.Marshal(map[string]any{
		"model":      c.Model,
		"max_tokens": 1200,
		"system":     "You are Fixora CLI. " + jsonContract(),
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
	return parseAIContent(decoded.Content[0].Text)
}

// parseAIContent enforces the JSON contract for every provider: malformed
// content, or a syntactically valid but empty object, yields an Unstructured
// result so callers fall back to the deterministic plan instead of failing
// hard or surfacing blank AI sections.
func parseAIContent(content string) (*analyzer.AIResult, error) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	var result analyzer.AIResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return unstructuredAIResult(content), nil
	}
	if strings.TrimSpace(result.Summary) == "" &&
		strings.TrimSpace(result.RootCause) == "" &&
		strings.TrimSpace(result.RecommendedFix) == "" {
		return unstructuredAIResult(content), nil
	}
	return &result, nil
}

func unstructuredAIResult(content string) *analyzer.AIResult {
	return &analyzer.AIResult{
		Summary:      "AI response did not satisfy the JSON contract.",
		RootCause:    content,
		Warnings:     []string{"AI response could not be parsed; falling back to the deterministic plan."},
		Unstructured: true,
	}
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
	case "cohere":
		return "https://api.cohere.com"
	case "huggingface":
		return "https://api-inference.huggingface.co/models"
	case "localai":
		return "http://localhost:8080/v1"
	case "azureopenai", "customrest", "watsonxai", "ibmwatsonxai", "googlevertexai", "amazonbedrock", "amazonbedrockconverse", "amazonsagemaker", "oci":
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
	case "cohere":
		return "command-r-plus"
	case "huggingface":
		return "mistralai/Mistral-7B-Instruct-v0.3"
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
