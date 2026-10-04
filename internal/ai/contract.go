package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"gopkg.in/yaml.v3"
)

var defaultOutputContract = OutputContract{
	RequiredFields: []string{"summary", "rootCause", "recommendedFix"},
}

func ValidateAIResult(content []byte) (*analyzer.AIResult, error) {
	content = []byte(stripJSONFence(strings.TrimSpace(string(content))))
	if len(bytes.TrimSpace(content)) == 0 {
		return nil, &ProviderError{Kind: ErrorContract}
	}

	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var result analyzer.AIResult
	if err := decoder.Decode(&result); err != nil {
		return nil, &ProviderError{Kind: ErrorContract}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, &ProviderError{Kind: ErrorContract}
	}

	contract := defaultOutputContract
	if len(contract.RequiredFields) == 0 {
		contract = OutputContract{RequiredFields: []string{"summary", "rootCause", "recommendedFix"}}
	}
	values := map[string]string{
		"summary":        result.Summary,
		"rootCause":      result.RootCause,
		"recommendedFix": result.RecommendedFix,
	}
	for _, field := range contract.RequiredFields {
		if strings.TrimSpace(values[field]) == "" {
			return nil, &ProviderError{Kind: ErrorContract}
		}
	}
	if result.Confidence < 0 || result.Confidence > 100 {
		return nil, &ProviderError{Kind: ErrorContract}
	}
	if strings.TrimSpace(result.PatchYAML) != "" {
		var patch map[string]any
		if err := yaml.Unmarshal([]byte(result.PatchYAML), &patch); err != nil || len(patch) == 0 {
			return nil, &ProviderError{Kind: ErrorContract}
		}
	}
	return &result, nil
}

func stripJSONFence(content string) string {
	if !strings.HasPrefix(content, "```") {
		return content
	}
	newline := strings.IndexByte(content, '\n')
	if newline < 0 {
		return content
	}
	content = content[newline+1:]
	if end := strings.LastIndex(content, "```"); end >= 0 {
		content = content[:end]
	}
	return strings.TrimSpace(content)
}

func parseAIContent(content string) (*analyzer.AIResult, error) {
	result, err := ValidateAIResult([]byte(content))
	if err == nil {
		return result, nil
	}
	return unstructuredAIResultWithReason(content, "invalid or incomplete response"), nil
}

func unstructuredAIResultWithReason(_ string, reason string) *analyzer.AIResult {
	return &analyzer.AIResult{
		Summary:      "AI response did not satisfy the JSON contract.",
		Warnings:     []string{fmt.Sprintf("AI response could not be parsed (%s); using the deterministic plan.", reason)},
		Unstructured: true,
	}
}
