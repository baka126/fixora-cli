package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoctorReportsProtocolAndSanitizesEndpoint(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("FIXORA_AI_PROVIDER", "openai")
	t.Setenv("FIXORA_AI_API_KEY", "secret-key")
	t.Setenv("FIXORA_AI_BASE_URL", "https://user:pass@example.test/v1?token=query-secret")
	t.Setenv("FIXORA_AI_PROTOCOL", string(ProtocolOpenAIResponses))
	result := Doctor(context.Background())
	encoded, _ := json.Marshal(result)
	if result.Protocol != string(ProtocolOpenAIResponses) || result.BaseURL != "https://example.test" {
		t.Fatalf("doctor result = %#v", result)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "user") || strings.Contains(string(encoded), "pass") {
		t.Fatalf("doctor output leaked endpoint credentials: %s", encoded)
	}
}

func TestDoctorDefaultConfigurationOnly(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	setDoctorEnvironment(t, server.URL)

	result := Doctor(context.Background())
	if result.Status != "configured" || requests != 0 {
		t.Fatalf("default doctor result=%#v requests=%d; want configured and no provider request", result, requests)
	}
}

func TestDoctorLiveValidatesGeneration(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/chat/completions" {
			t.Errorf("request path = %q, want /chat/completions", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer doctor-secret-key" {
			t.Errorf("configured credential was not used")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"summary\":\"probe ok\",\"rootCause\":\"synthetic\",\"recommendedFix\":\"none\"}"}}]}`))
	}))
	defer server.Close()
	setDoctorEnvironment(t, server.URL)

	result := DoctorLive(context.Background())
	if result.Status != "ok" || requests != 1 {
		t.Fatalf("live doctor result=%#v requests=%d; want ok and one provider request", result, requests)
	}
}

func TestDoctorLiveRejectsUnstructuredGeneration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"not JSON"}}`))
	}))
	defer server.Close()
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("FIXORA_AI_PROVIDER", "ollama")
	t.Setenv("FIXORA_AI_API_KEY", "")
	t.Setenv("FIXORA_AI_BASE_URL", server.URL)
	t.Setenv("FIXORA_AI_MODEL", "doctor-test-model")
	t.Setenv("FIXORA_AI_PROTOCOL", "")
	t.Setenv("FIXORA_AI_OUTPUT_MODE", "")

	result := DoctorLive(context.Background())
	if result.Status != "error" || !strings.Contains(result.Detail, "response contract") {
		t.Fatalf("live doctor result = %#v; want response contract error", result)
	}
}

func TestDoctorLiveRejectsNoopProvider(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("FIXORA_AI_PROVIDER", "noop")
	t.Setenv("FIXORA_AI_API_KEY", "")
	t.Setenv("FIXORA_AI_BASE_URL", "")
	t.Setenv("FIXORA_AI_MODEL", "")
	t.Setenv("FIXORA_AI_PROTOCOL", "")
	t.Setenv("FIXORA_AI_OUTPUT_MODE", "")

	result := DoctorLive(context.Background())
	if result.Status != "error" || !strings.Contains(result.Detail, "does not support live generation") {
		t.Fatalf("live doctor result = %#v; want no-op provider refusal", result)
	}
}

func TestDoctorLiveBedrockUsesSingleGenerationAttempt(t *testing.T) {
	for _, provider := range []string{"amazonbedrockconverse", "amazonbedrock"} {
		t.Run(provider, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"message":"temporarily unavailable"}`))
			}))
			defer server.Close()
			t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
			t.Setenv("FIXORA_AI_PROVIDER", provider)
			t.Setenv("FIXORA_AI_API_KEY", "")
			t.Setenv("FIXORA_AI_BASE_URL", "")
			t.Setenv("FIXORA_AI_MODEL", "anthropic.claude-test")
			t.Setenv("FIXORA_AI_PROTOCOL", "")
			t.Setenv("FIXORA_AI_OUTPUT_MODE", "")
			t.Setenv("AWS_REGION", "us-east-1")
			t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")
			t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
			t.Setenv("AWS_ENDPOINT_URL", server.URL)

			result := DoctorLive(context.Background())
			if result.Status != "error" || requests != 1 {
				t.Fatalf("result=%#v requests=%d; want one failed generation attempt", result, requests)
			}
		})
	}
}

func TestDoctorLiveSanitizesProviderErrors(t *testing.T) {
	const sentinel = "provider-secret-response-body"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"` + sentinel + `","code":"` + sentinel + `","param":"` + sentinel + `"}}`))
	}))
	defer server.Close()
	setDoctorEnvironment(t, server.URL)

	result := DoctorLive(context.Background())
	encoded, _ := json.Marshal(result)
	if result.Status != "error" || requests != 1 {
		t.Fatalf("live doctor result=%#v requests=%d; want error and one request", result, requests)
	}
	if strings.Contains(string(encoded), sentinel) || strings.Contains(string(encoded), "doctor-secret-key") {
		t.Fatalf("live doctor leaked provider details: %s", encoded)
	}
}

func TestDoctorLiveHonorsParentDeadline(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestStarted <- struct{}{}
		<-releaseHandler
	}))
	defer func() {
		close(releaseHandler)
		server.Close()
	}()
	setDoctorEnvironment(t, server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result := DoctorLive(ctx)
	select {
	case <-requestStarted:
	default:
		t.Fatal("live doctor did not send its provider request")
	}
	if result.Status != "error" || result.Detail != "request deadline exceeded" {
		t.Fatalf("live doctor result = %#v; want sanitized deadline error", result)
	}
}

func TestDoctorDoesNotLeakOllamaProbeURL(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("FIXORA_AI_PROVIDER", "ollama")
	t.Setenv("FIXORA_AI_BASE_URL", "http://127.0.0.1:0?token=query-secret")
	t.Setenv("FIXORA_AI_API_KEY", "")
	result := Doctor(context.Background())
	encoded, _ := json.Marshal(result)
	if result.Status != "configured" || strings.Contains(string(encoded), "query-secret") {
		t.Fatalf("unsafe Ollama doctor result: %s", encoded)
	}
}

func setDoctorEnvironment(t *testing.T, baseURL string) {
	t.Helper()
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("FIXORA_AI_PROVIDER", "openai")
	t.Setenv("FIXORA_AI_API_KEY", "doctor-secret-key")
	t.Setenv("FIXORA_AI_BASE_URL", baseURL)
	t.Setenv("FIXORA_AI_MODEL", "doctor-test-model")
	t.Setenv("FIXORA_AI_PROTOCOL", "")
	t.Setenv("FIXORA_AI_OUTPUT_MODE", "")
}
