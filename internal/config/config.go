package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const SchemaVersion = 1

type Config struct {
	SchemaVersion   int                 `json:"schemaVersion"`
	AIProvider      string              `json:"aiProvider,omitempty"`
	AIBaseURL       string              `json:"aiBaseURL,omitempty"`
	AIModel         string              `json:"aiModel,omitempty"`
	AIAPIKey        string              `json:"aiApiKey,omitempty"`
	Profile         string              `json:"profile,omitempty"`
	CacheEnabled    bool                `json:"cacheEnabled"`
	Timeout         string              `json:"timeout,omitempty"`
	LogTail         int                 `json:"logTail,omitempty"`
	MaxLogBytes     int                 `json:"maxLogBytes,omitempty"`
	DefaultOutput   string              `json:"defaultOutput,omitempty"`
	Redact          bool                `json:"redact"`
	Paranoid        bool                `json:"paranoid,omitempty"`
	ApplyDryRun     bool                `json:"applyRequiresDryRun"`
	CustomAnalyzers []string            `json:"customAnalyzers,omitempty"`
	ActiveProfile   string              `json:"activeProfile,omitempty"`
	Profiles        map[string]Settings `json:"profiles,omitempty"`
	Contexts        map[string]Settings `json:"contexts,omitempty"`
}

type Settings struct {
	Namespace     string `json:"namespace,omitempty"`
	AIProvider    string `json:"aiProvider,omitempty"`
	AIBaseURL     string `json:"aiBaseURL,omitempty"`
	AIModel       string `json:"aiModel,omitempty"`
	Profile       string `json:"profile,omitempty"`
	CacheEnabled  *bool  `json:"cacheEnabled,omitempty"`
	Timeout       string `json:"timeout,omitempty"`
	LogTail       *int   `json:"logTail,omitempty"`
	MaxLogBytes   *int   `json:"maxLogBytes,omitempty"`
	DefaultOutput string `json:"defaultOutput,omitempty"`
	Redact        *bool  `json:"redact,omitempty"`
	Paranoid      *bool  `json:"paranoid,omitempty"`
	ApplyDryRun   *bool  `json:"applyRequiresDryRun,omitempty"`
}

type ResolvedValue struct {
	Value  any    `json:"value"`
	Source string `json:"source"`
}

type ValidationResult struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

func Default() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		AIProvider:    "openai",
		AIModel:       "gpt-4o-mini",
		Profile:       "sre",
		CacheEnabled:  true,
		Timeout:       "90s",
		LogTail:       120,
		MaxLogBytes:   24000,
		DefaultOutput: "text",
		Redact:        true,
		ApplyDryRun:   true,
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
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = SchemaVersion
	}
	if cfg.ActiveProfile != "" {
		cfg.ApplySettings(cfg.Profiles[cfg.ActiveProfile])
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
		"schemaVersion":   cfg.SchemaVersion,
		"aiProvider":      cfg.AIProvider,
		"aiBaseURL":       cfg.AIBaseURL,
		"aiModel":         cfg.AIModel,
		"aiApiKeySet":     strings.TrimSpace(cfg.AIAPIKey) != "",
		"profile":         cfg.Profile,
		"cacheEnabled":    cfg.CacheEnabled,
		"timeout":         cfg.Timeout,
		"logTail":         cfg.LogTail,
		"maxLogBytes":     cfg.MaxLogBytes,
		"defaultOutput":   cfg.DefaultOutput,
		"redact":          cfg.Redact,
		"paranoid":        cfg.Paranoid,
		"applyDryRun":     cfg.ApplyDryRun,
		"customAnalyzers": cfg.CustomAnalyzers,
		"activeProfile":   cfg.ActiveProfile,
		"profiles":        profileNames(cfg.Profiles),
		"contexts":        profileNames(cfg.Contexts),
	}
}

func Export(cfg Config, showSecrets bool) map[string]any {
	out := Public(cfg)
	if showSecrets {
		out["aiApiKey"] = cfg.AIAPIKey
	} else if strings.TrimSpace(cfg.AIAPIKey) != "" {
		out["aiApiKey"] = "REDACTED"
	}
	return out
}

func Resolved() (map[string]ResolvedValue, error) {
	cfg, err := Load()
	if err != nil {
		return nil, err
	}
	raw, _ := fileKeys()
	out := map[string]ResolvedValue{}
	add := func(key string, value any, source string) {
		out[key] = ResolvedValue{Value: value, Source: source}
	}
	addString := func(key, env, cfgValue, defValue string) {
		if value := strings.TrimSpace(os.Getenv(env)); value != "" {
			add(key, value, env)
			return
		}
		if raw[key] {
			add(key, cfgValue, "config")
			return
		}
		add(key, defValue, "default")
	}
	addString("aiProvider", "FIXORA_AI_PROVIDER", cfg.AIProvider, Default().AIProvider)
	addString("aiBaseURL", "FIXORA_AI_BASE_URL", cfg.AIBaseURL, Default().AIBaseURL)
	addString("aiModel", "FIXORA_AI_MODEL", cfg.AIModel, Default().AIModel)
	if strings.TrimSpace(os.Getenv("FIXORA_AI_API_KEY")) != "" {
		add("aiApiKeySet", true, "FIXORA_AI_API_KEY")
	} else if strings.TrimSpace(cfg.AIAPIKey) != "" {
		add("aiApiKeySet", true, "config")
	} else {
		add("aiApiKeySet", false, "none")
	}
	addString("profile", "FIXORA_AI_PROFILE", cfg.Profile, Default().Profile)
	add("cacheEnabled", cfg.CacheEnabled, sourceFor(raw, "cacheEnabled"))
	add("timeout", cfg.Timeout, sourceFor(raw, "timeout"))
	add("logTail", cfg.LogTail, sourceFor(raw, "logTail"))
	add("maxLogBytes", cfg.MaxLogBytes, sourceFor(raw, "maxLogBytes"))
	add("defaultOutput", cfg.DefaultOutput, sourceFor(raw, "defaultOutput"))
	add("redact", cfg.Redact, sourceFor(raw, "redact"))
	add("paranoid", cfg.Paranoid, sourceFor(raw, "paranoid"))
	add("applyDryRun", cfg.ApplyDryRun, sourceFor(raw, "applyRequiresDryRun"))
	add("customAnalyzers", cfg.CustomAnalyzers, sourceFor(raw, "customAnalyzers"))
	add("activeProfile", cfg.ActiveProfile, sourceFor(raw, "activeProfile"))
	return out, nil
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
	case "ai.api_key", "api_key", "apikey":
		return fmt.Errorf("refusing to set API keys through config set; use auth set or FIXORA_AI_API_KEY")
	case "ai.base_url", "base_url", "baseurl":
		cfg.AIBaseURL = value
	case "ai.model", "model":
		cfg.AIModel = value
	case "cache.enabled":
		v, err := parseBool(value)
		if err != nil {
			return err
		}
		cfg.CacheEnabled = v
	case "profile", "ai.profile":
		cfg.Profile = value
	case "timeout":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid timeout %q: %w", value, err)
		}
		cfg.Timeout = value
	case "log_tail", "logtail", "log-tail":
		n, err := parseInt(value)
		if err != nil {
			return err
		}
		cfg.LogTail = n
	case "max_log_bytes", "maxlogbytes", "max-logs-bytes":
		n, err := parseInt(value)
		if err != nil {
			return err
		}
		cfg.MaxLogBytes = n
	case "default_output", "defaultoutput", "output":
		cfg.DefaultOutput = value
	case "redact":
		v, err := parseBool(value)
		if err != nil {
			return err
		}
		cfg.Redact = v
	case "paranoid":
		v, err := parseBool(value)
		if err != nil {
			return err
		}
		cfg.Paranoid = v
	case "apply_requires_dry_run", "applydryrun":
		v, err := parseBool(value)
		if err != nil {
			return err
		}
		cfg.ApplyDryRun = v
	default:
		return fmt.Errorf("unknown config key %q", args[0])
	}
	return Save(cfg)
}

func ProfileCommand(args []string) (any, error) {
	if len(args) == 0 || args[0] == "list" {
		cfg, err := loadStored()
		if err != nil {
			return nil, err
		}
		return map[string]any{"active": cfg.ActiveProfile, "profiles": profileNames(cfg.Profiles)}, nil
	}
	cfg, err := loadStored()
	if err != nil {
		return nil, err
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Settings{}
	}
	switch args[0] {
	case "create":
		if len(args) < 2 {
			return nil, fmt.Errorf("config profile create requires a name")
		}
		cfg.Profiles[args[1]] = settingsFromConfig(LoadOrDefault(cfg))
	case "use":
		if len(args) < 2 {
			return nil, fmt.Errorf("config profile use requires a name")
		}
		if _, ok := cfg.Profiles[args[1]]; !ok {
			return nil, fmt.Errorf("profile %q does not exist", args[1])
		}
		cfg.ActiveProfile = args[1]
	case "delete":
		if len(args) < 2 {
			return nil, fmt.Errorf("config profile delete requires a name")
		}
		delete(cfg.Profiles, args[1])
		if cfg.ActiveProfile == args[1] {
			cfg.ActiveProfile = ""
		}
	case "set":
		if len(args) < 4 {
			return nil, fmt.Errorf("config profile set requires name, key, and value")
		}
		settings := cfg.Profiles[args[1]]
		if err := setSetting(&settings, args[2], args[3]); err != nil {
			return nil, err
		}
		cfg.Profiles[args[1]] = settings
	default:
		return nil, fmt.Errorf("unknown config profile command %q", args[0])
	}
	return map[string]any{"active": cfg.ActiveProfile, "profiles": profileNames(cfg.Profiles)}, saveStored(cfg)
}

func ContextCommand(args []string) (any, error) {
	if len(args) == 0 || args[0] == "list" {
		cfg, err := loadStored()
		if err != nil {
			return nil, err
		}
		return map[string]any{"contexts": cfg.Contexts}, nil
	}
	cfg, err := loadStored()
	if err != nil {
		return nil, err
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]Settings{}
	}
	switch args[0] {
	case "set":
		if len(args) < 4 {
			return nil, fmt.Errorf("config context set requires context, key, and value")
		}
		settings := cfg.Contexts[args[1]]
		if err := setSetting(&settings, args[2], args[3]); err != nil {
			return nil, err
		}
		cfg.Contexts[args[1]] = settings
	case "unset":
		if len(args) < 2 {
			return nil, fmt.Errorf("config context unset requires a context")
		}
		delete(cfg.Contexts, args[1])
	default:
		return nil, fmt.Errorf("unknown config context command %q", args[0])
	}
	return map[string]any{"contexts": cfg.Contexts}, saveStored(cfg)
}

func (cfg *Config) ApplySettings(s Settings) {
	if s.AIProvider != "" {
		cfg.AIProvider = s.AIProvider
	}
	if s.AIBaseURL != "" {
		cfg.AIBaseURL = s.AIBaseURL
	}
	if s.AIModel != "" {
		cfg.AIModel = s.AIModel
	}
	if s.Profile != "" {
		cfg.Profile = s.Profile
	}
	if s.CacheEnabled != nil {
		cfg.CacheEnabled = *s.CacheEnabled
	}
	if s.Timeout != "" {
		cfg.Timeout = s.Timeout
	}
	if s.LogTail != nil {
		cfg.LogTail = *s.LogTail
	}
	if s.MaxLogBytes != nil {
		cfg.MaxLogBytes = *s.MaxLogBytes
	}
	if s.DefaultOutput != "" {
		cfg.DefaultOutput = s.DefaultOutput
	}
	if s.Redact != nil {
		cfg.Redact = *s.Redact
	}
	if s.Paranoid != nil {
		cfg.Paranoid = *s.Paranoid
	}
	if s.ApplyDryRun != nil {
		cfg.ApplyDryRun = *s.ApplyDryRun
	}
}

func (cfg Config) ContextSettings(name string) Settings {
	if name == "" {
		return Settings{}
	}
	return cfg.Contexts[name]
}

func Unset(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("config unset requires a key")
	}
	cfg, err := Load()
	if err != nil {
		return err
	}
	def := Default()
	key := strings.ToLower(args[0])
	switch key {
	case "ai.provider", "provider":
		cfg.AIProvider = def.AIProvider
	case "ai.api_key", "api_key", "apikey":
		cfg.AIAPIKey = ""
	case "ai.base_url", "base_url", "baseurl":
		cfg.AIBaseURL = ""
	case "ai.model", "model":
		cfg.AIModel = def.AIModel
	case "cache.enabled":
		cfg.CacheEnabled = def.CacheEnabled
	case "profile", "ai.profile":
		cfg.Profile = def.Profile
	case "timeout":
		cfg.Timeout = def.Timeout
	case "log_tail", "logtail", "log-tail":
		cfg.LogTail = def.LogTail
	case "max_log_bytes", "maxlogbytes", "max-logs-bytes":
		cfg.MaxLogBytes = def.MaxLogBytes
	case "default_output", "defaultoutput", "output":
		cfg.DefaultOutput = def.DefaultOutput
	case "redact":
		cfg.Redact = def.Redact
	case "paranoid":
		cfg.Paranoid = def.Paranoid
	case "apply_requires_dry_run", "applydryrun":
		cfg.ApplyDryRun = def.ApplyDryRun
	default:
		return fmt.Errorf("unknown config key %q", args[0])
	}
	return Save(cfg)
}

func Reset() error {
	path, err := Path()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func Validate(cfg Config) ValidationResult {
	result := ValidationResult{Valid: true}
	providers := []string{"openai", "ollama", "anthropic", "gemini", "google", "groq", "localai", "azureopenai", "customrest", "cohere", "huggingface", "googlevertexai", "amazonbedrock", "amazonbedrockconverse", "amazonsagemaker", "oci", "watsonxai", "ibmwatsonxai", "noop"}
	if !slices.Contains(providers, strings.ToLower(cfg.AIProvider)) {
		result.Errors = append(result.Errors, "aiProvider must be one of "+strings.Join(providers, ", "))
	}
	if !slices.Contains(Profiles(), strings.ToLower(cfg.Profile)) {
		result.Errors = append(result.Errors, "profile must be one of "+strings.Join(Profiles(), ", "))
	}
	if cfg.Timeout == "" {
		result.Errors = append(result.Errors, "timeout is required")
	} else if _, err := time.ParseDuration(cfg.Timeout); err != nil {
		result.Errors = append(result.Errors, "timeout must be a valid duration like 30s or 2m")
	}
	if cfg.LogTail < 0 {
		result.Errors = append(result.Errors, "logTail cannot be negative")
	}
	if cfg.MaxLogBytes < 0 {
		result.Errors = append(result.Errors, "maxLogBytes cannot be negative")
	}
	outputs := []string{"text", "json", "yaml", "markdown", "sarif", "junit", "prometheus", "metrics"}
	if !slices.Contains(outputs, strings.ToLower(cfg.DefaultOutput)) {
		result.Errors = append(result.Errors, "defaultOutput must be one of "+strings.Join(outputs, ", "))
	}
	if strings.TrimSpace(cfg.AIAPIKey) != "" {
		result.Warnings = append(result.Warnings, "aiApiKey is stored in plaintext config; prefer FIXORA_AI_API_KEY for production")
	}
	if !cfg.Redact {
		result.Warnings = append(result.Warnings, "redact is disabled; production clusters should keep redaction enabled")
	}
	if !cfg.ApplyDryRun {
		result.Warnings = append(result.Warnings, "applyRequiresDryRun is disabled; production clusters should keep server dry-run enabled")
	}
	result.Valid = len(result.Errors) == 0
	return result
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
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		if !strings.Contains(path, ".") && !strings.Contains(path, "localhost") {
			return fmt.Errorf("custom analyzer URL must include a valid host")
		}
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

func loadStored() (Config, error) {
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
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = SchemaVersion
	}
	return cfg, nil
}

func saveStored(cfg Config) error {
	return Save(cfg)
}

func LoadOrDefault(cfg Config) Config {
	def := Default()
	if cfg.AIProvider == "" {
		cfg.AIProvider = def.AIProvider
	}
	if cfg.AIModel == "" {
		cfg.AIModel = def.AIModel
	}
	if cfg.Profile == "" {
		cfg.Profile = def.Profile
	}
	if cfg.Timeout == "" {
		cfg.Timeout = def.Timeout
	}
	if cfg.LogTail == 0 {
		cfg.LogTail = def.LogTail
	}
	if cfg.MaxLogBytes == 0 {
		cfg.MaxLogBytes = def.MaxLogBytes
	}
	if cfg.DefaultOutput == "" {
		cfg.DefaultOutput = def.DefaultOutput
	}
	return cfg
}

func settingsFromConfig(cfg Config) Settings {
	cacheEnabled := cfg.CacheEnabled
	logTail := cfg.LogTail
	maxLogBytes := cfg.MaxLogBytes
	redact := cfg.Redact
	paranoid := cfg.Paranoid
	applyDryRun := cfg.ApplyDryRun
	return Settings{
		AIProvider:    cfg.AIProvider,
		AIBaseURL:     cfg.AIBaseURL,
		AIModel:       cfg.AIModel,
		Profile:       cfg.Profile,
		CacheEnabled:  &cacheEnabled,
		Timeout:       cfg.Timeout,
		LogTail:       &logTail,
		MaxLogBytes:   &maxLogBytes,
		DefaultOutput: cfg.DefaultOutput,
		Redact:        &redact,
		Paranoid:      &paranoid,
		ApplyDryRun:   &applyDryRun,
	}
}

func setSetting(settings *Settings, key, value string) error {
	switch strings.ToLower(key) {
	case "namespace":
		settings.Namespace = value
	case "ai.provider", "provider":
		settings.AIProvider = value
	case "ai.base_url", "base_url", "baseurl":
		settings.AIBaseURL = value
	case "ai.model", "model":
		settings.AIModel = value
	case "profile", "ai.profile":
		settings.Profile = value
	case "cache.enabled":
		v, err := parseBool(value)
		if err != nil {
			return err
		}
		settings.CacheEnabled = &v
	case "timeout":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid timeout %q: %w", value, err)
		}
		settings.Timeout = value
	case "log_tail", "logtail", "log-tail":
		v, err := parseInt(value)
		if err != nil {
			return err
		}
		settings.LogTail = &v
	case "max_log_bytes", "maxlogbytes", "max-logs-bytes":
		v, err := parseInt(value)
		if err != nil {
			return err
		}
		settings.MaxLogBytes = &v
	case "default_output", "defaultoutput", "output":
		settings.DefaultOutput = value
	case "redact":
		v, err := parseBool(value)
		if err != nil {
			return err
		}
		settings.Redact = &v
	case "paranoid":
		v, err := parseBool(value)
		if err != nil {
			return err
		}
		settings.Paranoid = &v
	case "apply_requires_dry_run", "applydryrun":
		v, err := parseBool(value)
		if err != nil {
			return err
		}
		settings.ApplyDryRun = &v
	default:
		return fmt.Errorf("unknown setting key %q", key)
	}
	return nil
}

func profileNames(values map[string]Settings) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

func fileKeys() (map[string]bool, error) {
	out := map[string]bool{}
	path, err := Path()
	if err != nil {
		return out, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return out, err
	}
	for key := range raw {
		out[key] = true
	}
	return out, nil
}

func sourceFor(raw map[string]bool, key string) string {
	if raw[key] {
		return "config"
	}
	return "default"
}

func parseBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("expected boolean, got %q", value)
	}
}

func parseInt(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("expected integer, got %q", value)
	}
	return n, nil
}
