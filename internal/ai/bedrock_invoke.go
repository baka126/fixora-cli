package ai

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type BedrockInvokeAPI interface {
	InvokeModel(context.Context, *bedrockruntime.InvokeModelInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
}

type bedrockInvokeAdapter struct{ client BedrockInvokeAPI }

func NewBedrockInvokeAdapter(client BedrockInvokeAPI) Adapter {
	return bedrockInvokeAdapter{client: client}
}

func (a bedrockInvokeAdapter) Explain(ctx context.Context, request Request) (*analyzer.AIResult, error) {
	if request.Provider.Protocol != ProtocolBedrock {
		return nil, &ProviderError{Kind: ErrorUnsupported}
	}
	client := a.client
	if client == nil {
		region := strings.TrimSpace(request.Provider.EndpointOptions["region"])
		if region == "" {
			return nil, &ProviderError{Kind: ErrorConfiguration}
		}
		cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
		if err != nil {
			return nil, &ProviderError{Kind: ErrorConfiguration}
		}
		client = bedrockruntime.NewFromConfig(cfg)
	}
	body, err := bedrockInvokeBody(request)
	if err != nil {
		return nil, err
	}
	response, err := client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{ModelId: aws.String(request.Model), ContentType: aws.String("application/json"), Accept: aws.String("application/json"), Body: body})
	if err != nil {
		return nil, normalizeBedrockError(err)
	}
	if response == nil {
		return nil, &ProviderError{Kind: ErrorIncomplete}
	}
	text, err := parseBedrockInvokeResponse(request.Model, response.Body)
	if err != nil {
		return nil, err
	}
	return ValidateAIResult([]byte(text))
}

func bedrockInvokeBody(request Request) ([]byte, error) {
	model := bedrockModelFamily(request.Model)
	prompt := request.SystemPrompt + "\n\n" + request.UserPrompt
	switch {
	case strings.HasPrefix(model, "anthropic."):
		return json.Marshal(map[string]any{"anthropic_version": "bedrock-2023-05-31", "max_tokens": 2048, "system": request.SystemPrompt, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": request.UserPrompt}}}}})
	case strings.HasPrefix(model, "amazon.titan-text"):
		return json.Marshal(map[string]any{"inputText": prompt, "textGenerationConfig": map[string]any{"maxTokenCount": 2048}})
	case strings.HasPrefix(model, "meta.llama"):
		return json.Marshal(map[string]any{"prompt": prompt, "max_gen_len": 2048})
	case strings.HasPrefix(model, "mistral."):
		return json.Marshal(map[string]any{"prompt": prompt, "max_tokens": 2048})
	case strings.HasPrefix(model, "cohere."):
		return json.Marshal(map[string]any{"prompt": prompt, "max_tokens": 2048, "stream": false})
	default:
		return nil, &ProviderError{Kind: ErrorUnsupported}
	}
}

func parseBedrockInvokeResponse(model string, data []byte) (string, error) {
	var result string
	model = bedrockModelFamily(model)
	switch {
	case strings.HasPrefix(strings.ToLower(model), "anthropic."):
		var v struct {
			StopReason string `json:"stop_reason"`
			Content    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(data, &v) != nil {
			return "", &ProviderError{Kind: ErrorContract}
		}
		if v.StopReason == "max_tokens" {
			return "", &ProviderError{Kind: ErrorIncomplete}
		}
		if v.StopReason != "end_turn" && v.StopReason != "stop_sequence" {
			return "", &ProviderError{Kind: ErrorIncomplete}
		}
		for _, b := range v.Content {
			if b.Type == "text" {
				result += b.Text
			}
		}
	case strings.HasPrefix(strings.ToLower(model), "amazon.titan-text"):
		var v struct {
			Results []struct {
				Output string `json:"outputText"`
			} `json:"results"`
		}
		if json.Unmarshal(data, &v) != nil || len(v.Results) == 0 {
			return "", &ProviderError{Kind: ErrorContract}
		}
		result = v.Results[0].Output
	case strings.HasPrefix(strings.ToLower(model), "meta.llama"):
		var v struct {
			Generation string `json:"generation"`
		}
		if json.Unmarshal(data, &v) != nil {
			return "", &ProviderError{Kind: ErrorContract}
		}
		result = v.Generation
	case strings.HasPrefix(strings.ToLower(model), "mistral."):
		var v struct {
			Outputs []struct {
				Text string `json:"text"`
			} `json:"outputs"`
		}
		if json.Unmarshal(data, &v) != nil || len(v.Outputs) == 0 {
			return "", &ProviderError{Kind: ErrorContract}
		}
		result = v.Outputs[0].Text
	case strings.HasPrefix(strings.ToLower(model), "cohere."):
		var v struct {
			Generations []struct {
				Text string `json:"text"`
			} `json:"generations"`
		}
		if json.Unmarshal(data, &v) != nil || len(v.Generations) == 0 {
			return "", &ProviderError{Kind: ErrorContract}
		}
		result = v.Generations[0].Text
	default:
		return "", &ProviderError{Kind: ErrorUnsupported}
	}
	return result, nil
}

func bedrockModelFamily(model string) string {
	model = strings.ToLower(model)
	for _, family := range []string{"anthropic.", "amazon.titan-text", "meta.llama", "mistral.", "cohere."} {
		if index := strings.Index(model, family); index >= 0 {
			return model[index:]
		}
	}
	return model
}
