package analyzer

import (
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func CorrelateRecentEvents(events []kube.Event) (string, []string) {
	now := time.Now()
	var recent []string
	correlation := ""

	changeKeywords := []string{"ScalingReplicaSet", "SuccessfulRelease", "Upgrade", "Rollout", "Updated", "Created", "Deleted"}

	for _, event := range events {
		t, err := time.Parse(time.RFC3339, event.LastTime)
		if err != nil {
			continue
		}

		if now.Sub(t) <= 60*time.Minute {
			isChange := false
			for _, kw := range changeKeywords {
				if strings.Contains(event.Reason, kw) || strings.Contains(event.Message, kw) {
					isChange = true
					break
				}
			}

			if isChange {
				msg := event.Reason + ": " + event.Message
				recent = append(recent, msg)
			}
		}
	}

	if len(recent) > 0 {
		correlation = "Found recent changes within the last 60 minutes."
	}

	return correlation, recent
}
