package shadow

import (
	"context"
	"fmt"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/redact"
)

func revisePatch(ctx context.Context, aiProvider ai.Provider, patch, planType string, attempt Attempt, redactionEnabled bool) (string, bool, error) {
	if aiProvider == nil {
		return patch, false, nil
	}
	if !redactionEnabled {
		return patch, false, fmt.Errorf("shadow AI retry disabled because redaction is not enabled")
	}
	finding := analyzer.Finding{
		Summary: "Shadow pod verification failed. Exit reason: " + attempt.ExitReason,
		Status:  attempt.Phase,
	}
	for _, l := range attempt.Logs {
		finding.Logs = append(finding.Logs, analyzer.LogSnippet{Source: attempt.CloneName, Text: redact.KubernetesText(l)})
	}
	for _, e := range attempt.Events {
		finding.Evidence = append(finding.Evidence, analyzer.Evidence{Label: "Event", Value: redact.KubernetesText(e)})
	}
	finding.Evidence = append(finding.Evidence, analyzer.Evidence{Label: "Current Patch", Value: redact.KubernetesText(patch)})
	res, err := aiProvider.Explain(ctx, finding)
	if err != nil {
		return patch, false, err
	}
	if res == nil {
		return patch, false, nil
	}
	candidate := strings.TrimSpace(res.PatchYAML)
	if candidate == "" {
		candidate = strings.TrimSpace(res.RecommendedFix)
	}
	if candidate == "" || strings.Contains(candidate, "Review the response manually") {
		return patch, false, nil
	}
	revised := trimFencedYAML(candidate)
	if err := ValidateRevisedPatch(patch, revised, planType); err != nil {
		return patch, false, err
	}
	return revised, true, nil
}

func trimFencedYAML(value string) string {
	start := strings.Index(value, "```yaml")
	if start == -1 {
		start = strings.Index(value, "```yml")
	}
	if start == -1 {
		start = strings.Index(value, "```")
	}
	if start != -1 {
		end := strings.Index(value[start+3:], "```")
		if end != -1 {
			block := value[start : start+3+end+3]
			if strings.HasPrefix(block, "```yaml") {
				block = strings.TrimPrefix(block, "```yaml")
			} else if strings.HasPrefix(block, "```yml") {
				block = strings.TrimPrefix(block, "```yml")
			} else {
				block = strings.TrimPrefix(block, "```")
			}
			block = strings.TrimSuffix(block, "```")
			return strings.TrimSpace(block)
		}
	}
	return strings.TrimSpace(value)
}
