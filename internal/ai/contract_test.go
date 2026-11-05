package ai

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateAIResultAcceptsCompleteContract(t *testing.T) {
	result, err := ValidateAIResult([]byte(`{"summary":"s","rootCause":"r","recommendedFix":"f","patchYAML":"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n"}`))
	if err != nil || result == nil || result.Summary != "s" || result.Unstructured {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestValidateAIResultRejectsMissingRequiredFields(t *testing.T) {
	_, err := ValidateAIResult([]byte(`{"summary":"only summary"}`))
	if err == nil {
		t.Fatal("expected incomplete contract error")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorContract {
		t.Fatalf("expected contract error, got %T %v", err, err)
	}
}

func TestValidateAIResultRejectsOutOfRangeConfidence(t *testing.T) {
	for _, confidence := range []string{"-1", "101"} {
		_, err := ValidateAIResult([]byte(`{"summary":"s","rootCause":"r","recommendedFix":"f","confidence":` + confidence + `}`))
		if err == nil {
			t.Errorf("confidence %s accepted", confidence)
		}
	}
}

func TestValidateAIResultRejectsInvalidPatchYAML(t *testing.T) {
	_, err := ValidateAIResult([]byte(`{"summary":"s","rootCause":"r","recommendedFix":"f","patchYAML":"apiVersion: ["}`))
	if err == nil {
		t.Fatal("expected invalid YAML to fail validation")
	}
}

func TestValidateAIResultRejectsEmptyMalformedAndRefusalText(t *testing.T) {
	for _, content := range []string{"", "not json", "I cannot help with that request"} {
		if _, err := ValidateAIResult([]byte(content)); err == nil {
			t.Errorf("content %q accepted", content)
		}
	}
}

func TestFailureSummarySanitizesProviderResponse(t *testing.T) {
	body := []byte(`{"error":{"message":"api_key=do-not-print prompt=private-evidence","code":"invalid_request_error","param":"temperature"}}`)
	err := providerErrorFromResponse(400, 0, body)
	got := FailureSummary(err)
	for _, expected := range []string{"HTTP 400", "invalid_request_error", "temperature"} {
		if !strings.Contains(got, expected) {
			t.Errorf("FailureSummary(%v) = %q, missing %q", err, got, expected)
		}
	}
	for _, secret := range []string{"do-not-print", "private-evidence", "api_key"} {
		if strings.Contains(got, secret) {
			t.Errorf("FailureSummary leaked %q: %q", secret, got)
		}
	}
}

func TestParseAIContentDoesNotSurfaceRawProviderText(t *testing.T) {
	content := `api_key=provider-secret prompt=private-cluster-evidence`
	result, err := parseAIContent(content)
	if err != nil || result == nil || !result.Unstructured {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.RootCause != "" || strings.Contains(strings.Join(result.Warnings, " "), "provider-secret") {
		t.Fatalf("raw provider content reached fallback result: %#v", result)
	}
}
