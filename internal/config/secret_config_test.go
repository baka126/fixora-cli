package config

import (
	"encoding/json"
	"testing"
)

// TestCheckSecretKeysRoundTrip verifies that CheckSecretKeys marshals and
// unmarshals correctly, and is omitted from JSON when false.
func TestCheckSecretKeysRoundTrip(t *testing.T) {
	cfg := Config{CheckSecretKeys: true}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var rawTrue map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawTrue); err != nil {
		t.Fatal(err)
	}
	if _, present := rawTrue["checkSecretKeys"]; !present {
		t.Fatalf("expected JSON payload to contain exact checkSecretKeys field, got %s", string(data))
	}

	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.CheckSecretKeys {
		t.Fatalf("expected CheckSecretKeys=true after round-trip, got false")
	}

	// When false, the field should be omitted (omitempty).
	cfgFalse := Config{CheckSecretKeys: false}
	dataFalse, err := json.Marshal(cfgFalse)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(dataFalse, &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["checkSecretKeys"]; present {
		t.Fatalf("expected checkSecretKeys to be omitted when false, but it was present in JSON")
	}
}
