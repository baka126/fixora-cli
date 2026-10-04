package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicAdapterUsesMessagesAuthAndParsesBlocks(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "anthropic-test" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("request method/path/headers = %s %s %#v", r.Method, r.URL.Path, r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Error(err)
		}
		fmt.Fprintf(w, `{"stop_reason":"end_turn","content":[{"type":"text","text":%q}]}`, validAIResultJSON)
	}))
	defer server.Close()
	spec, _ := ResolveProvider("anthropic")
	spec.OutputModes = []OutputMode{OutputModeJSONSchema}
	result, err := NewAnthropicAdapter(server.Client()).Explain(context.Background(), Request{
		Provider: spec, Model: "claude-test", BaseURL: server.URL, APIKey: "anthropic-test",
		SystemPrompt: "system", UserPrompt: "finding", Contract: OutputContract{Schema: remediationSchema()},
	})
	if err != nil || result == nil || result.RootCause != "root cause" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	outputConfig, _ := requestBody["output_config"].(map[string]any)
	format, _ := outputConfig["format"].(map[string]any)
	if format["type"] != "json_schema" || format["schema"] == nil {
		t.Fatalf("Anthropic structured-output format missing: %#v", requestBody["output_config"])
	}
}

func TestAnthropicAdapterHandlesRefusalAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		stopReason string
		want       ErrorKind
	}{{"refusal", ErrorRefusal}, {"max_tokens", ErrorIncomplete}, {"model_context_window_exceeded", ErrorIncomplete}} {
		t.Run(tc.stopReason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"stop_reason":%q,"content":[{"type":"text","text":"partial"}]}`, tc.stopReason)
			}))
			defer server.Close()
			spec, _ := ResolveProvider("anthropic")
			_, err := NewAnthropicAdapter(server.Client()).Explain(context.Background(), Request{Provider: spec, Model: "claude-test", BaseURL: server.URL, APIKey: "test"})
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) || providerErr.Kind != tc.want {
				t.Fatalf("error=%v, want kind %q", err, tc.want)
			}
		})
	}
}

func TestGeminiAdapterUsesGenerateContentAndJSONMode(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta/models/gemini-3.8-flash:generateContent" || r.Header.Get("x-goog-api-key") != "google-test" {
			t.Errorf("request method/path/key = %s %s %q", r.Method, r.URL.Path, r.Header.Get("x-goog-api-key"))
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Error(err)
		}
		fmt.Fprintf(w, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":%q}]}}]}`, validAIResultJSON)
	}))
	defer server.Close()
	spec, _ := ResolveProvider("gemini")
	result, err := NewGeminiAdapter(server.Client()).Explain(context.Background(), Request{
		Provider: spec, Model: "gemini-3.8-flash", BaseURL: server.URL + "/v1beta", APIKey: "google-test",
		SystemPrompt: "system", UserPrompt: "finding", Contract: OutputContract{Schema: remediationSchema()},
	})
	if err != nil || result == nil || result.Summary != "summary" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	generationConfig, _ := requestBody["generationConfig"].(map[string]any)
	if generationConfig["responseMimeType"] != "application/json" || generationConfig["responseJsonSchema"] == nil {
		t.Fatalf("Gemini structured JSON config missing: %#v", generationConfig)
	}
}

func TestGeminiAdapterCombinesTextPartsAndSkipsThoughts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"api_key=private","thought":true},{"text":"{\"summary\":\"summary\",\"rootCause\":\"root cause\",\"recommendedFix\":\"recommended fix\",\"patchYAML\":\"\",\"strategy\":\"runtime\",\"confidence\":85,\"analyzers\":[],\"commands\":[],\"warnings\":[]}"}]}}]}`)
	}))
	defer server.Close()
	spec, _ := ResolveProvider("gemini")
	result, err := NewGeminiAdapter(server.Client()).Explain(context.Background(), Request{Provider: spec, Model: "test", BaseURL: server.URL, SystemPrompt: "system", UserPrompt: "finding"})
	if err != nil || result == nil || result.RootCause != "root cause" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if strings.Contains(result.RootCause, "private") {
		t.Fatal("thought content reached the visible result")
	}
}

func TestGeminiAdapterHandlesBlockedOrIncompleteCandidates(t *testing.T) {
	for _, tc := range []struct {
		finishReason string
		want         ErrorKind
	}{{"SAFETY", ErrorRefusal}, {"MAX_TOKENS", ErrorIncomplete}} {
		t.Run(tc.finishReason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"candidates":[{"finishReason":%q,"content":{"parts":[{"text":"partial"}]}}]}`, tc.finishReason)
			}))
			defer server.Close()
			spec, _ := ResolveProvider("gemini")
			_, err := NewGeminiAdapter(server.Client()).Explain(context.Background(), Request{Provider: spec, Model: "test", BaseURL: server.URL})
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) || providerErr.Kind != tc.want {
				t.Fatalf("error=%v, want kind %q", err, tc.want)
			}
		})
	}
}

func TestGeminiAdapterMapsPromptFeedbackBlockToRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"promptFeedback":{"blockReason":"SAFETY"}}`)
	}))
	defer server.Close()
	spec, _ := ResolveProvider("gemini")
	_, err := NewGeminiAdapter(server.Client()).Explain(context.Background(), Request{Provider: spec, Model: "test", BaseURL: server.URL})
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorRefusal {
		t.Fatalf("error=%v, want refusal", err)
	}
}
