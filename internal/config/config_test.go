package config

import (
	"path/filepath"
	"testing"
)

func TestPublicAndExportRedactSecrets(t *testing.T) {
	cfg := Default()
	cfg.AIAPIKey = "secret"

	public := Public(cfg)
	if public["aiApiKeySet"] != true {
		t.Fatalf("expected public config to expose only key presence, got %#v", public)
	}
	if _, ok := public["aiApiKey"]; ok {
		t.Fatalf("public config leaked aiApiKey: %#v", public)
	}

	exported := Export(cfg, false)
	if exported["aiApiKey"] != "REDACTED" {
		t.Fatalf("expected redacted export, got %#v", exported["aiApiKey"])
	}
	withSecrets := Export(cfg, true)
	if withSecrets["aiApiKey"] != "secret" {
		t.Fatalf("expected secret export only when requested, got %#v", withSecrets["aiApiKey"])
	}
}

func TestValidateWarnsAboutPlaintextSecrets(t *testing.T) {
	cfg := Default()
	cfg.AIAPIKey = "secret"
	cfg.Redact = false

	result := Validate(cfg)
	if !result.Valid {
		t.Fatalf("expected config to be valid, got %#v", result)
	}
	if len(result.Warnings) < 2 {
		t.Fatalf("expected plaintext/redaction warnings, got %#v", result.Warnings)
	}
}

func TestSetUnsetAndResolvedSources(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	if err := Set([]string{"ai.provider", "anthropic"}); err != nil {
		t.Fatal(err)
	}
	if err := Set([]string{"timeout", "30s"}); err != nil {
		t.Fatal(err)
	}

	resolved, err := Resolved()
	if err != nil {
		t.Fatal(err)
	}
	if resolved["aiProvider"].Value != "anthropic" || resolved["aiProvider"].Source != "config" {
		t.Fatalf("expected ai provider from config, got %#v", resolved["aiProvider"])
	}
	if resolved["timeout"].Value != "30s" || resolved["timeout"].Source != "config" {
		t.Fatalf("expected timeout from config, got %#v", resolved["timeout"])
	}

	t.Setenv("FIXORA_AI_PROVIDER", "openai")
	resolved, err = Resolved()
	if err != nil {
		t.Fatal(err)
	}
	if resolved["aiProvider"].Value != "openai" || resolved["aiProvider"].Source != "FIXORA_AI_PROVIDER" {
		t.Fatalf("expected ai provider from env, got %#v", resolved["aiProvider"])
	}

	if err := Unset([]string{"timeout"}); err != nil {
		t.Fatal(err)
	}
	resolved, err = Resolved()
	if err != nil {
		t.Fatal(err)
	}
	if resolved["timeout"].Value != Default().Timeout {
		t.Fatalf("expected default timeout after unset, got %#v", resolved["timeout"])
	}

	if err := Reset(); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AIProvider != Default().AIProvider || cfg.Timeout != Default().Timeout || cfg.Redact != Default().Redact {
		t.Fatalf("expected default config after reset, got %#v", cfg)
	}
}

func TestSetRejectsAPIKeyAndInvalidValues(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	if err := Set([]string{"ai.api_key", "secret"}); err == nil {
		t.Fatal("expected config set to reject API keys")
	}
	if err := Set([]string{"timeout", "soon"}); err == nil {
		t.Fatal("expected invalid timeout error")
	}
	if err := Set([]string{"log_tail", "many"}); err == nil {
		t.Fatal("expected invalid integer error")
	}
	if err := Set([]string{"redact", "maybe"}); err == nil {
		t.Fatal("expected invalid boolean error")
	}
}
