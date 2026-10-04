package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/config"
)

type Client struct {
	Provider   string
	Protocol   Protocol
	OutputMode OutputMode
	Spec       ProviderSpec
	Profile    string
	BaseURL    string
	APIKey     string
	Model      string
	HTTP       *http.Client
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func NewFromEnv() (Client, error) {
	cfg, _ := config.Load()
	provider := strings.ToLower(firstNonEmpty(os.Getenv("FIXORA_AI_PROVIDER"), cfg.AIProvider, "openai"))
	key := strings.TrimSpace(os.Getenv("FIXORA_AI_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(cfg.AIAPIKey)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(firstNonEmpty(os.Getenv("FIXORA_AI_BASE_URL"), cfg.AIBaseURL)), "/")
	model := strings.TrimSpace(os.Getenv("FIXORA_AI_MODEL"))
	if model == "" {
		configuredModel := cfg.AIModel
		if provider != "openai" && configuredModel == config.Default().AIModel {
			configuredModel = ""
		}
		model = strings.TrimSpace(firstNonEmpty(configuredModel, defaultModel(provider)))
	}
	if model == "" {
		model = defaultModel(provider)
	}
	if baseURL == "" {
		baseURL = defaultBaseURL(provider)
	}
	spec, err := ResolveProvider(provider)
	if err != nil {
		return Client{}, err
	}
	if spec.UnsupportedReason != "" {
		return Client{}, fmt.Errorf("AI provider %q is not supported: %s", provider, spec.UnsupportedReason)
	}
	if key == "" && !providerAllowsNoKey(provider) {
		return Client{}, fmt.Errorf("FIXORA_AI_API_KEY is not set")
	}
	if provider == "googlevertexai" {
		project := strings.TrimSpace(os.Getenv("FIXORA_AI_PROJECT"))
		location := strings.TrimSpace(os.Getenv("FIXORA_AI_LOCATION"))
		if project == "" || location == "" {
			return Client{}, fmt.Errorf("googlevertexai requires FIXORA_AI_PROJECT and FIXORA_AI_LOCATION")
		}
		spec.EndpointOptions = map[string]string{"project": project, "location": location}
		if baseURL == "" {
			baseURL = "https://" + location + "-aiplatform.googleapis.com"
		}
	}
	if provider == "azureopenai" {
		if version := strings.TrimSpace(os.Getenv("FIXORA_AI_API_VERSION")); version != "" {
			if spec.EndpointOptions == nil {
				spec.EndpointOptions = map[string]string{}
			}
			spec.EndpointOptions["api-version"] = version
		}
	}
	if provider == "amazonbedrock" || provider == "amazonbedrockconverse" {
		region := firstNonEmpty(os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION"))
		if region == "" {
			return Client{}, fmt.Errorf("%s requires AWS_REGION or AWS_DEFAULT_REGION", provider)
		}
		spec.EndpointOptions = map[string]string{"region": region}
	}
	protocol := spec.Protocol
	if configured := strings.TrimSpace(os.Getenv("FIXORA_AI_PROTOCOL")); configured != "" {
		protocol = Protocol(configured)
		if protocol != spec.Protocol && !(provider == "openai" && protocol == ProtocolOpenAIResponses) {
			return Client{}, fmt.Errorf("FIXORA_AI_PROTOCOL %q is not supported for provider %q", configured, provider)
		}
	}
	outputMode := OutputModePromptJSON
	if provider == "openai" || provider == "groq" || provider == "localai" || provider == "customrest" || provider == "googlevertexai" {
		outputMode = OutputModeJSON
	}
	if configured := strings.TrimSpace(os.Getenv("FIXORA_AI_OUTPUT_MODE")); configured != "" {
		outputMode = OutputMode(configured)
	}
	if outputMode != OutputModePromptJSON && outputMode != OutputModeJSON && outputMode != OutputModeJSONSchema {
		return Client{}, fmt.Errorf("FIXORA_AI_OUTPUT_MODE must be prompt-json, json, or json-schema")
	}
	if !hasOutputMode(spec, outputMode) {
		return Client{}, fmt.Errorf("AI provider %q does not support output mode %q", provider, outputMode)
	}
	if (provider == "azureopenai" || provider == "customrest" || provider == "watsonxai" || provider == "ibmwatsonxai" || provider == "amazonsagemaker" || provider == "oci") && baseURL == "" {
		return Client{}, fmt.Errorf("FIXORA_AI_BASE_URL is required for %s", provider)
	}
	return Client{
		Provider:   provider,
		Protocol:   protocol,
		OutputMode: outputMode,
		Spec:       spec,
		Profile:    strings.TrimSpace(firstNonEmpty(os.Getenv("FIXORA_AI_PROFILE"), cfg.Profile)),
		BaseURL:    baseURL,
		APIKey:     key,
		Model:      model,
		HTTP:       &http.Client{Timeout: 60 * time.Second},
	}, nil
}

func (c Client) Explain(ctx context.Context, finding analyzer.Finding) (*analyzer.AIResult, error) {
	return retryProviderCall(ctx, func() (*analyzer.AIResult, error) {
		return c.explainOnce(ctx, finding)
	})
}

func (c Client) explainOnce(ctx context.Context, finding analyzer.Finding) (*analyzer.AIResult, error) {
	resolved, err := ResolveProvider(c.Provider)
	if err != nil {
		return nil, err
	}
	if resolved.UnsupportedReason != "" {
		return nil, fmt.Errorf("AI provider %q is not supported: %s", c.Provider, resolved.UnsupportedReason)
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	payload, err := json.MarshalIndent(finding, "", "  ")
	if err != nil {
		return nil, err
	}
	cfg, _ := config.Load()
	if c.Profile != "" {
		cfg.Profile = c.Profile
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
		return c.explainWithAdapter(ctx, NewCohereAdapter(c.HTTP), payloadWithProfile)
	case "huggingface":
		return c.explainWithAdapter(ctx, NewHuggingFaceAdapter(c.HTTP), payloadWithProfile)
	case "watsonxai", "ibmwatsonxai":
		return nil, fmt.Errorf("AI provider %q is not supported: %s", c.Provider, resolved.UnsupportedReason)
	case "googlevertexai":
		return c.explainWithAdapter(ctx, NewVertexAdapter(c.HTTP, nil), payloadWithProfile)
	case "amazonbedrockconverse":
		return c.explainWithAdapter(ctx, NewBedrockConverseAdapter(nil), payloadWithProfile)
	case "amazonbedrock":
		return c.explainWithAdapter(ctx, NewBedrockInvokeAdapter(nil), payloadWithProfile)
	case "amazonsagemaker", "oci":
		return nil, fmt.Errorf("AI provider %q is not supported: %s", c.Provider, resolved.UnsupportedReason)
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
	data, err := readProviderResponse(resp)
	if err != nil {
		return nil, err
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
	data, err := readProviderResponse(resp)
	if err != nil {
		return nil, err
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
	provider := firstNonEmpty(c.Provider, "openai")
	spec, err := ResolveProvider(provider)
	if err != nil {
		return nil, err
	}
	spec = c.providerSpec(spec)
	adapter := NewOpenAIChatAdapter(c.HTTP)
	if spec.Protocol == ProtocolOpenAIResponses {
		adapter = NewOpenAIResponsesAdapter(c.HTTP)
	}
	return adapter.Explain(ctx, Request{
		Provider: spec, Model: c.Model, BaseURL: c.BaseURL, APIKey: c.APIKey,
		SystemPrompt: "You are Fixora CLI, a local Kubernetes SRE assistant. Be precise, safe, and do not invent secret values. " + jsonContract(),
		UserPrompt:   "Analyze this redacted Kubernetes finding. Prefer GitOps-safe advice and explicit verification steps. Finding:\n" + payload,
		Contract:     OutputContract{Schema: remediationSchema()},
	})
}

func (c Client) explainAzureOpenAI(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	spec, err := ResolveProvider("azureopenai")
	if err != nil {
		return nil, err
	}
	spec = c.providerSpec(spec)
	return NewOpenAIChatAdapter(c.HTTP).Explain(ctx, Request{
		Provider: spec, Model: c.Model, BaseURL: c.BaseURL, APIKey: c.APIKey,
		SystemPrompt: "You are Fixora CLI, a local Kubernetes SRE assistant. " + jsonContract(),
		UserPrompt:   "Analyze this redacted Kubernetes finding:\n" + payload,
		Contract:     OutputContract{Schema: remediationSchema()},
	})
}

func (c Client) explainGemini(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	spec, err := ResolveProvider(firstNonEmpty(c.Provider, "gemini"))
	if err != nil {
		return nil, err
	}
	spec = c.providerSpec(spec)
	return NewGeminiAdapter(c.HTTP).Explain(ctx, Request{Provider: spec, Model: c.Model, BaseURL: c.BaseURL, APIKey: c.APIKey,
		SystemPrompt: "You are Fixora CLI, a local Kubernetes SRE assistant. " + jsonContract(),
		UserPrompt:   "Analyze this redacted Kubernetes finding:\n" + payload, Contract: OutputContract{Schema: remediationSchema()}})
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
	data, err := readProviderResponse(resp)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Message chatMessage `json:"message"`
		Error   string      `json:"error"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if decoded.Error != "" {
		return nil, fmt.Errorf("%s", decoded.Error)
	}
	return parseAIContent(decoded.Message.Content)
}

func (c Client) explainAnthropic(ctx context.Context, payload string) (*analyzer.AIResult, error) {
	spec, err := ResolveProvider("anthropic")
	if err != nil {
		return nil, err
	}
	spec = c.providerSpec(spec)
	return NewAnthropicAdapter(c.HTTP).Explain(ctx, Request{Provider: spec, Model: c.Model, BaseURL: c.BaseURL, APIKey: c.APIKey,
		SystemPrompt: "You are Fixora CLI. " + jsonContract(),
		UserPrompt:   "Analyze this redacted Kubernetes finding:\n" + payload, Contract: OutputContract{Schema: remediationSchema()}})
}

func (c Client) providerSpec(spec ProviderSpec) ProviderSpec {
	if c.Spec.ID != "" {
		spec = c.Spec
	}
	if c.Protocol != "" {
		spec.Protocol = c.Protocol
	}
	if c.OutputMode != "" {
		spec.OutputModes = []OutputMode{c.OutputMode}
	}
	return spec
}

func (c Client) explainWithAdapter(ctx context.Context, adapter Adapter, payload string) (*analyzer.AIResult, error) {
	spec, err := ResolveProvider(c.Provider)
	if err != nil {
		return nil, err
	}
	spec = c.providerSpec(spec)
	return adapter.Explain(ctx, Request{Provider: spec, Model: c.Model, BaseURL: c.BaseURL, APIKey: c.APIKey,
		SystemPrompt: "You are Fixora CLI, a local Kubernetes SRE assistant. Be precise, safe, and do not invent secret values. " + jsonContract(),
		UserPrompt:   "Analyze this redacted Kubernetes finding. Prefer GitOps-safe advice and explicit verification steps. Finding:\n" + payload,
		Contract:     OutputContract{Schema: remediationSchema()}})
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
		return "https://router.huggingface.co/v1"
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
		return "claude-sonnet-4-6"
	case "gemini", "google", "googlevertexai":
		return "gemini-3.8-flash"
	case "amazonbedrock", "amazonbedrockconverse":
		return "global.anthropic.claude-sonnet-5"
	case "groq":
		return "llama-3.1-70b-versatile"
	case "cohere":
		return "command-r-plus"
	case "huggingface":
		return "Qwen/Qwen3-Coder-480B-A35B-Instruct:fastest"
	default:
		return "gpt-4o-mini"
	}
}

func providerAllowsNoKey(provider string) bool {
	switch strings.ToLower(provider) {
	case "ollama", "noop", "localai", "customrest", "googlevertexai", "amazonbedrock", "amazonbedrockconverse", "amazonsagemaker", "oci":
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
