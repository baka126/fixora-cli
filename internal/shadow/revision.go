package shadow

import (
	"context"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

func revisePatch(ctx context.Context, aiProvider ai.Provider, patch string, attempt Attempt) (string, bool) {
	if aiProvider == nil {
		return patch, false
	}
	finding := analyzer.Finding{
		Summary: "Shadow pod verification failed. Exit reason: " + attempt.ExitReason,
		Status:  attempt.Phase,
	}
	for _, l := range attempt.Logs {
		finding.Logs = append(finding.Logs, analyzer.LogSnippet{Source: attempt.CloneName, Text: l})
	}
	for _, e := range attempt.Events {
		finding.Evidence = append(finding.Evidence, analyzer.Evidence{Label: "Event", Value: e})
	}
	finding.Evidence = append(finding.Evidence, analyzer.Evidence{Label: "Current Patch", Value: patch})
	res, err := aiProvider.Explain(ctx, finding)
	if err == nil && res.RecommendedFix != "" && !strings.Contains(res.RecommendedFix, "Review the response manually") {
		revised := res.RecommendedFix
		if strings.HasPrefix(revised, "```yaml") {
			revised = strings.TrimPrefix(revised, "```yaml")
		} else if strings.HasPrefix(revised, "```") {
			revised = strings.TrimPrefix(revised, "```")
		}
		revised = strings.TrimSuffix(revised, "```")
		return strings.TrimSpace(revised), true
	}
	return patch, false
}
