package analyzer

import (
	"fmt"
	"strings"
)

func (a Analyzer) analyzeWebhooks(ctx *ScanContext) ([]Finding, error) {
	out := []Finding{}
	for _, resource := range []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"} {
		items, err := ctx.GetResourceItems("", true, resource)
		if err != nil {
			continue
		}
		kind := "MutatingWebhookConfiguration"
		if strings.HasPrefix(resource, "validating") {
			kind = "ValidatingWebhookConfiguration"
		}
		for _, cfg := range items {
			_, name := objectNamespaceName(cfg)
			if strings.HasPrefix(name, "system:") || strings.HasPrefix(name, "kube-") || strings.HasPrefix(name, "eks-") {
				continue
			}
			for _, webhook := range nestedSlice(cfg, "webhooks") {
				webhookMap, _ := webhook.(map[string]any)
				webhookName := strValue(webhookMap["name"])
				clientConfig := nestedMap(webhookMap, "clientConfig")
				service := nestedMap(clientConfig, "service")
				serviceName, serviceNS := strValue(service["name"]), strValue(service["namespace"])
				if serviceName != "" {
					state := a.objectNameState(ctx, serviceNS, "service", serviceName)
					if !state.Exists {
						status := "MissingWebhookService/" + webhookName
						summary := "Admission webhook references a Service that does not exist."
						if state.Forbidden {
							status = "WebhookServiceUnreadable/" + webhookName
							summary = "Admission webhook references a Service that exists but is not readable with current RBAC."
						}
						out = append(out, clusterFinding(kind, name, status, "high", "policy", summary, []Evidence{{Label: "Webhook", Value: webhookName}, {Label: "Service", Value: keyFor(serviceNS, serviceName)}, {Label: "State", Value: state.Message}}))
					}
				}
				if seconds := intValue(webhookMap["timeoutSeconds"]); seconds > 10 {
					out = append(out, clusterFinding(kind, name, "HighWebhookTimeout/"+webhookName, "medium", "policy", "Admission webhook has a high timeout and can slow or block API writes during incidents.", []Evidence{{Label: "Webhook", Value: webhookName}, {Label: "timeoutSeconds", Value: fmt.Sprint(seconds)}}))
				}
				if failurePolicy := strValue(webhookMap["failurePolicy"]); strings.EqualFold(failurePolicy, "Fail") {
					out = append(out, clusterFinding(kind, name, "FailClosedWebhook/"+webhookName, "low", "policy", "Admission webhook fails closed; this can block remediation when the webhook backend is unhealthy.", []Evidence{{Label: "Webhook", Value: webhookName}, {Label: "failurePolicy", Value: failurePolicy}}))
				}
			}
		}
	}
	return out, nil
}
