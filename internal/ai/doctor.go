package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

const doctorLiveProbeTimeout = 30 * time.Second

type DoctorResult struct {
	Provider string `json:"provider"`
	Protocol string `json:"protocol"`
	Model    string `json:"model"`
	BaseURL  string `json:"baseURL"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
}

func Doctor(_ context.Context) DoctorResult {
	result, _, err := doctorClient()
	if err != nil {
		return result
	}
	return result
}

// DoctorLive sends one synthetic generation request through the configured
// provider adapter. It does not inspect or send Kubernetes data.
func DoctorLive(ctx context.Context) DoctorResult {
	result, client, err := doctorClient()
	if err != nil {
		return result
	}
	if strings.EqualFold(client.Provider, "noop") {
		result.Status = "error"
		result.Detail = "AI provider does not support live generation"
		return result
	}
	client.singleAttempt = true
	probeCtx, cancel := context.WithTimeout(ctx, doctorLiveProbeTimeout)
	defer cancel()
	probe := analyzer.Finding{
		ID:           "fixora-ai-doctor-probe",
		Namespace:    "fixora-doctor",
		ResourceKind: "Provider",
		ResourceName: "doctor-probe",
		Status:       "ProviderProbe",
		Severity:     "info",
		Category:     "diagnostic",
		Summary:      "Synthetic Fixora provider check. Confirm this small probe was received; do not invent Kubernetes observations.",
	}
	answer, err := client.explainOnce(probeCtx, probe)
	if err != nil {
		result.Status = "error"
		result.Detail = doctorFailureSummary(err)
		return result
	}
	if answer == nil || answer.Unstructured {
		result.Status = "error"
		result.Detail = doctorFailureSummary(&ProviderError{Kind: ErrorContract})
		return result
	}
	encoded, err := json.Marshal(answer)
	if err == nil {
		_, err = ValidateAIResult(encoded)
	}
	if err != nil {
		result.Status = "error"
		result.Detail = doctorFailureSummary(err)
		return result
	}
	result.Status = "ok"
	return result
}

func doctorFailureSummary(err error) string {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		// Provider code/param fields are untrusted and can contain API keys or
		// other caller-controlled strings even when they match safeDetail's shape.
		redacted := *providerErr
		redacted.Code = ""
		redacted.Param = ""
		return FailureSummary(&redacted)
	}
	return FailureSummary(err)
}

func doctorClient() (DoctorResult, Client, error) {
	client, err := NewFromEnv()
	if err != nil {
		return DoctorResult{Status: "error", Detail: err.Error()}, Client{}, err
	}
	result := DoctorResult{Provider: client.Provider, Protocol: string(client.Protocol), Model: client.Model, BaseURL: safeEndpointIdentity(client.BaseURL), Status: "configured"}
	return result, client, nil
}

func safeEndpointIdentity(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return ""
	}
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" {
		host += ":" + port
	}
	return u.Scheme + "://" + host
}
