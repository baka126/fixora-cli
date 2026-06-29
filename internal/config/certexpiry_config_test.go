package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigCheckCertExpiryRoundTrips(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"checkCertExpiry": true}`), &c); err != nil {
		t.Fatal(err)
	}
	if !c.CheckCertExpiry {
		t.Fatal("checkCertExpiry should unmarshal to true")
	}
	out, err := json.Marshal(Config{CheckCertExpiry: true})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out) || !strings.Contains(string(out), `"checkCertExpiry":true`) {
		t.Fatalf("marshal missing checkCertExpiry: %s", out)
	}
	public := Public(Config{CheckCertExpiry: true})
	if public["checkCertExpiry"] != true {
		t.Fatalf("public config missing checkCertExpiry: %#v", public)
	}
}

func TestConfigCheckCertExpiryResolved(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	cfg := Default()
	cfg.CheckCertExpiry = true
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolved()
	if err != nil {
		t.Fatal(err)
	}
	if resolved["checkCertExpiry"].Value != true || resolved["checkCertExpiry"].Source != "config" {
		t.Fatalf("resolved config missing checkCertExpiry source: %#v", resolved["checkCertExpiry"])
	}
}
