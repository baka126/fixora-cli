package ai

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type DoctorResult struct {
	Provider string `json:"provider"`
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
	result := DoctorResult{Provider: client.Provider, Model: client.Model, BaseURL: client.BaseURL, Status: "ok"}
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
