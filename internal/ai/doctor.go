package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

const doctorLiveProbeTimeout = 30 * time.Second

type DoctorResult struct {
	Provider string        `json:"provider"`
	Protocol string        `json:"protocol"`
	Model    string        `json:"model"`
	BaseURL  string        `json:"baseURL"`
	Status   string        `json:"status"`
	Detail   string        `json:"detail,omitempty"`
	Checks   []DoctorCheck `json:"checks,omitempty"`
}

type DoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func Doctor(_ context.Context) DoctorResult {
	result, _, err := doctorClient()
	if err != nil {
		return result
	}
	return result
}

// DoctorLive verifies the diagnosis and structured Ask adapters with synthetic
// requests. It does not inspect or send Kubernetes data.
func DoctorLive(ctx context.Context) DoctorResult {
	result, client, err := doctorClient()
	if err != nil {
		return result
	}
	if strings.EqualFold(client.Provider, "noop") {
		result.Status = "error"
		result.Detail = "AI provider does not support live generation"
		result.Checks = []DoctorCheck{{Name: "diagnosis", Status: "error", Detail: result.Detail}, {Name: "ask", Status: "skipped", Detail: "diagnosis check failed"}}
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
		result.Checks = []DoctorCheck{{Name: "diagnosis", Status: "error", Detail: result.Detail}, {Name: "ask", Status: "skipped", Detail: "diagnosis check failed"}}
		return result
	}
	if answer == nil || answer.Unstructured {
		result.Status = "error"
		result.Detail = doctorFailureSummary(&ProviderError{Kind: ErrorContract})
		result.Checks = []DoctorCheck{{Name: "diagnosis", Status: "error", Detail: result.Detail}, {Name: "ask", Status: "skipped", Detail: "diagnosis check failed"}}
		return result
	}
	encoded, err := json.Marshal(answer)
	if err == nil {
		_, err = ValidateAIResult(encoded)
	}
	if err != nil {
		result.Status = "error"
		result.Detail = doctorFailureSummary(err)
		result.Checks = []DoctorCheck{{Name: "diagnosis", Status: "error", Detail: result.Detail}, {Name: "ask", Status: "skipped", Detail: "diagnosis check failed"}}
		return result
	}
	result.Checks = []DoctorCheck{{Name: "diagnosis", Status: "ok"}}

	askCtx, askCancel := context.WithTimeout(ctx, doctorLiveProbeTimeout)
	defer askCancel()
	content, err := client.structuredJSONOnce(
		askCtx,
		"You are checking Fixora's structured Ask response path. Return exactly the requested JSON. Do not claim to inspect a Kubernetes cluster.",
		"This is a synthetic provider health check. Return {\"ok\":true}.",
		"fixora_doctor_ask",
		doctorAskSchema(),
	)
	if err == nil {
		err = validateDoctorAskResponse(content)
	}
	if err != nil {
		result.Status = "error"
		result.Detail = doctorFailureSummary(err)
		result.Checks = append(result.Checks, DoctorCheck{Name: "ask", Status: "error", Detail: result.Detail})
		return result
	}
	result.Checks = append(result.Checks, DoctorCheck{Name: "ask", Status: "ok"})
	result.Status = "ok"
	return result
}

func doctorAskSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"ok": map[string]any{"type": "boolean"}},
		"required":             []string{"ok"},
		"additionalProperties": false,
	}
}

func validateDoctorAskResponse(content string) error {
	if len(content) == 0 || len(content) > 1024 {
		return &ProviderError{Kind: ErrorContract}
	}
	var response struct {
		OK bool `json:"ok"`
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil || !response.OK {
		return &ProviderError{Kind: ErrorContract}
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return &ProviderError{Kind: ErrorContract}
	}
	return nil
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
	if client.Provider == "chatgpt" {
		if client.AccountEmail != "" {
			result.Detail = "Signed in as " + client.AccountEmail + "; ChatGPT plan usage is enabled"
		} else {
			result.Detail = "ChatGPT plan usage is enabled"
		}
	}
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
