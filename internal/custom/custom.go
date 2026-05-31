package custom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
