package custom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/config"
)

type Result struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type AnalyzerConfig struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	URL     string `json:"url,omitempty"`
	Timeout string `json:"timeout,omitempty"`
}

var dnsNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func List() ([]string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return cfg.CustomAnalyzers, nil
}

func Run(ctx context.Context, finding analyzer.Finding) ([]Result, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(finding)
	if err != nil {
		return nil, err
	}
	results := []Result{}
	for _, path := range cfg.CustomAnalyzers {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
			results = append(results, RunHTTP(ctx, path, finding))
			continue
		}
		if !filepath.IsAbs(path) || strings.Contains(path, "..") {
			results = append(results, Result{Path: path, Status: "error", Error: "custom analyzer path must be absolute and cannot contain directory traversal"})
			continue
		}
		runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		cmd := exec.CommandContext(runCtx, path)
		cmd.Stdin = bytes.NewReader(payload)
		out, runErr := cmd.CombinedOutput()
		cancel()
		result := Result{Path: path, Status: "ok", Output: strings.TrimSpace(string(out))}
		if runErr != nil {
			result.Status = "error"
			result.Error = fmt.Sprint(runErr)
		}
		results = append(results, result)
	}
	return results, nil
}

func ValidateAnalyzerConfig(cfg AnalyzerConfig) error {
	if !dnsNameRE.MatchString(cfg.Name) {
		return fmt.Errorf("custom analyzer name must be DNS-safe")
	}
	switch cfg.Type {
	case "exec":
		if strings.TrimSpace(cfg.Command) == "" {
			return fmt.Errorf("exec custom analyzer requires command")
		}
		if !filepath.IsAbs(cfg.Command) || strings.Contains(cfg.Command, "..") {
			return fmt.Errorf("exec custom analyzer command must be an absolute path without directory traversal")
		}
	case "http":
		parsed, err := url.Parse(cfg.URL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("http custom analyzer requires valid URL")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("custom analyzer URL must use http or https")
		}
	default:
		return fmt.Errorf("custom analyzer type must be exec or http")
	}
	return nil
}

func RunHTTP(ctx context.Context, endpoint string, finding analyzer.Finding) Result {
	payload, err := json.Marshal(finding)
	if err != nil {
		return Result{Path: endpoint, Status: "error", Error: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return Result{Path: endpoint, Status: "error", Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Result{Path: endpoint, Status: "error", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{Path: endpoint, Status: "error", Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	return Result{Path: endpoint, Status: "ok"}
}
