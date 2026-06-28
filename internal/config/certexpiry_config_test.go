package config

import (
	"encoding/json"
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
}
