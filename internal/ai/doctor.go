package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type DoctorResult struct {
	Provider string `json:"provider"`
	Protocol string `json:"protocol"`
	Model    string `json:"model"`
	BaseURL  string `json:"baseURL"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
}

func Doctor(ctx context.Context) DoctorResult {
	client, err := NewFromEnv()
	if err != nil {
		return DoctorResult{Status: "error", Detail: err.Error()}
	}
	result := DoctorResult{Provider: client.Provider, Protocol: string(client.Protocol), Model: client.Model, BaseURL: safeEndpointIdentity(client.BaseURL), Status: "ok"}
	if client.Provider == "ollama" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(client.BaseURL, "/")+"/api/tags", nil)
		if err != nil {
			result.Status = "error"
			result.Detail = err.Error()
			return result
		}
		httpClient := &http.Client{Timeout: 3 * time.Second}
		resp, err := httpClient.Do(req)
		if err != nil {
			result.Status = "error"
			result.Detail = err.Error()
			return result
		}
		defer resp.Body.Close()
		if resp.StatusCode > 299 {
			result.Status = "error"
			result.Detail = fmt.Sprintf("ollama returned HTTP %d", resp.StatusCode)
		}
	}
	return result
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
