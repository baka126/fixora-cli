package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	AIProvider      string   `json:"aiProvider,omitempty"`
	AIBaseURL       string   `json:"aiBaseURL,omitempty"`
	AIModel         string   `json:"aiModel,omitempty"`
	AIAPIKey        string   `json:"aiApiKey,omitempty"`
	Profile         string   `json:"profile,omitempty"`
	CacheEnabled    bool     `json:"cacheEnabled"`
	CustomAnalyzers []string `json:"customAnalyzers,omitempty"`
}

func Default() Config {
	return Config{
		AIProvider:   "openai",
		AIModel:      "gpt-4o-mini",
		Profile:      "sre",
		CacheEnabled: true,
	}
}

func Load() (Config, error) {
	cfg := Default()
	path, err := Path()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func Path() (string, error) {
	if p := strings.TrimSpace(os.Getenv("FIXORA_CONFIG")); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "fixora", "cli-config.json"), nil
}

func Public(cfg Config) map[string]any {
	return map[string]any{
		"aiProvider":      cfg.AIProvider,
		"aiBaseURL":       cfg.AIBaseURL,
		"aiModel":         cfg.AIModel,
		"aiApiKeySet":     strings.TrimSpace(cfg.AIAPIKey) != "",
		"profile":         cfg.Profile,
		"cacheEnabled":    cfg.CacheEnabled,
		"customAnalyzers": cfg.CustomAnalyzers,
	}
}

func Set(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("config set requires key and value")
	}
	cfg, err := Load()
	if err != nil {
		return err
	}
	key, value := strings.ToLower(args[0]), args[1]
	switch key {
	case "ai.provider", "provider":
		cfg.AIProvider = value
	case "ai.base_url", "base_url", "baseurl":
		cfg.AIBaseURL = value
	case "ai.model", "model":
		cfg.AIModel = value
	case "cache.enabled":
		cfg.CacheEnabled = value == "true" || value == "1" || strings.EqualFold(value, "yes")
	case "profile", "ai.profile":
		cfg.Profile = value
	default:
		return fmt.Errorf("unknown config key %q", args[0])
	}
	return Save(cfg)
}

func Profiles() []string {
	return []string{"sre", "security", "finops", "platform", "beginner"}
}

func ProfilePrompt(profile string) string {
	switch strings.ToLower(profile) {
	case "security":
		return "Prioritize least privilege, policy failures, secret safety, container hardening, and admission controller evidence."
	case "finops":
		return "Prioritize resource usage, right-sizing, cost impact, and safe reductions before scale-ups."
	case "platform":
		return "Prioritize controllers, GitOps source, rollout mechanics, SLO impact, and reusable platform fixes."
	case "beginner":
		return "Explain Kubernetes concepts plainly and include exact next commands."
	default:
		return "Prioritize concise SRE root cause, proof, risk, rollback, and GitOps-safe remediation."
	}
}

func AddCustomAnalyzer(path string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("custom analyzer path is required")
	}
	for _, existing := range cfg.CustomAnalyzers {
		if existing == path {
			return Save(cfg)
		}
	}
	cfg.CustomAnalyzers = append(cfg.CustomAnalyzers, path)
	return Save(cfg)
}

func Auth(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("auth set requires provider and api key")
	}
	cfg, err := Load()
	if err != nil {
		return err
	}
	cfg.AIProvider = args[0]
	cfg.AIAPIKey = args[1]
	if len(args) > 2 {
		cfg.AIBaseURL = args[2]
	}
	if len(args) > 3 {
		cfg.AIModel = args[3]
	}
	return Save(cfg)
}
